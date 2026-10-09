package fraud

import (
	"encoding/json"
	"net/http"
	"slices"
	"time"

	"github.com/JonahR/simple-network/internal/iso8583"
)

// Txn is one past authorization, for backtesting a rule. Req is as the
// network recorded it, so its card number is masked: card rules group by
// the masked number, which is close enough to judge a rule.
type Txn struct {
	Time          time.Time
	ID            string
	Req           iso8583.AuthRequest
	Code          string
	Approved      int64
	FraudDeclined bool
}

// Backtest is what a rule would have done to past traffic.
type Backtest struct {
	Screened int       `json:"screened"` // Transactions replayed
	From     time.Time `json:"from,omitempty"`
	To       time.Time `json:"to,omitempty"`
	Hits     int       `json:"hits"`
	Declined int       `json:"declined"` // Hits plus what its blocks would have stopped
	// WhileBlocked is the part of Declined its blocks stopped after it fired.
	WhileBlocked int       `json:"while_blocked"`
	Cases        int       `json:"cases"`
	Top          []Example `json:"top"` // Subjects it fired for most
}

// Example is one subject a backtest fired for.
type Example struct {
	Label string `json:"label"`
	Hits  int    `json:"hits"`
	Txn   string `json:"txn"` // A network transaction it fired on
}

// RunBacktest replays txns, oldest first, through a fresh engine holding
// only r, switched on.
func RunBacktest(r Rule, txns []Txn) (Backtest, error) {
	r.Mode, r.ID = ModeOn, "backtest"
	if err := r.Validate(); err != nil {
		return Backtest{}, err
	}
	txns = slices.Clone(txns)
	slices.SortStableFunc(txns, func(a, b Txn) int { return a.Time.Compare(b.Time) })
	var cur time.Time
	e := NewEngine([]Rule{r}, func() time.Time { return cur })
	bt := Backtest{Screened: len(txns), Top: []Example{}}
	by := map[string]*Example{}
	for _, t := range txns {
		cur = t.Time
		v := e.Screen(t.Req, t.ID)
		if v.Decline {
			bt.Declined++
			if len(v.Hits) == 0 {
				bt.WhileBlocked++
			}
		}
		for range v.Hits {
			bt.Hits++
			_, label := KeyOf(r.Subject, t.Req)
			ex := by[label]
			if ex == nil {
				ex = &Example{Label: label, Txn: t.ID}
				by[label] = ex
			}
			ex.Hits++
		}
		e.Observe(t.Req, t.Code, t.Approved, t.FraudDeclined || v.Decline)
	}
	if len(txns) > 0 {
		bt.From, bt.To = txns[0].Time, txns[len(txns)-1].Time
	}
	bt.Cases = len(e.cases)
	for _, ex := range by {
		bt.Top = append(bt.Top, *ex)
	}
	slices.SortFunc(bt.Top, func(a, b Example) int { return b.Hits - a.Hits })
	if len(bt.Top) > 5 {
		bt.Top = bt.Top[:5]
	}
	return bt, nil
}

// Option is one choice in the rule builder.
type Option struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// Register mounts the fraud department's API. history returns recent
// authorizations for backtests; it may be nil.
func (e *Engine) Register(mux *http.ServeMux, history func() []Txn) {
	mux.HandleFunc("GET /api/fraud", func(w http.ResponseWriter, _ *http.Request) {
		templates := Templates()
		views := make([]RuleView, len(templates))
		for i, t := range templates {
			views[i] = RuleView{Rule: t, Sentence: t.Sentence()}
		}
		entry := make([]Option, 0, len(iso8583.EntryModes))
		for _, code := range []string{iso8583.EntryChip, iso8583.EntryContactless, iso8583.EntryMagstripe, iso8583.EntryManual, iso8583.EntryEcommerce, iso8583.EntryCredentialOnFile} {
			entry = append(entry, Option{code, iso8583.EntryModes[code]})
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"snapshot":  e.Snapshot(),
			"templates": views,
			"options": map[string]any{
				"subjects": []Option{{SubjectCard, "card"}, {SubjectTerminal, "terminal"}, {SubjectMerchant, "merchant"}, {SubjectAcquirer, "acquirer"}, {SubjectBIN, "BIN range"}},
				"measures": []Option{{MeasureDeclines, "declines"}, {MeasureAttempts, "authorizations"}, {MeasureApprovals, "approvals"}, {MeasureSpend, "approved spend ($)"}},
				"actions":  []Option{{ActionDecline, "Decline it (59)"}, {ActionBlock, "Decline and block the subject"}, {ActionReview, "Let it through, open a case"}},
				"modes":    []Option{{ModeOn, "On"}, {ModeTest, "Test"}, {ModeOff, "Off"}},
				"entry":    entry,
				"mccs":     iso8583.MCCs,
			},
		})
	})
	mux.HandleFunc("POST /api/fraud/preview", func(w http.ResponseWriter, r *http.Request) {
		var rule Rule
		if !decode(w, r, &rule) {
			return
		}
		out := map[string]any{}
		check := rule
		if err := check.Validate(); err != nil {
			out["error"] = err.Error()
			writeJSON(w, http.StatusOK, out)
			return
		}
		out["sentence"] = check.Sentence()
		if history != nil {
			bt, _ := RunBacktest(check, history())
			out["backtest"] = bt
		}
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("POST /api/fraud/rules", func(w http.ResponseWriter, r *http.Request) {
		var rule Rule
		if !decode(w, r, &rule) {
			return
		}
		added, err := e.AddRule(rule)
		reply(w, added, err)
	})
	mux.HandleFunc("PUT /api/fraud/rules/{id}", func(w http.ResponseWriter, r *http.Request) {
		var rule Rule
		if !decode(w, r, &rule) {
			return
		}
		updated, err := e.UpdateRule(r.PathValue("id"), rule)
		reply(w, updated, err)
	})
	mux.HandleFunc("POST /api/fraud/rules/{id}/mode", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Mode string `json:"mode"`
		}
		if !decode(w, r, &body) {
			return
		}
		reply(w, body, e.SetMode(r.PathValue("id"), body.Mode))
	})
	mux.HandleFunc("DELETE /api/fraud/rules/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !e.DeleteRule(r.PathValue("id")) {
			http.Error(w, "rule not found", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("DELETE /api/fraud/blocks/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !e.LiftBlock(r.PathValue("id")) {
			http.Error(w, "block not found", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /api/fraud/cases/{id}", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Status string `json:"status"`
			Note   string `json:"note"`
		}
		if !decode(w, r, &body) {
			return
		}
		reply(w, body, e.ResolveCase(r.PathValue("id"), body.Status, body.Note))
	})
	mux.HandleFunc("POST /api/fraud/forget", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Keys []SubjectKey `json:"keys"`
			Note string       `json:"note"`
		}
		if !decode(w, r, &body) {
			return
		}
		if body.Note == "" {
			body.Note = "Closed when its counters were reset"
		}
		writeJSON(w, http.StatusOK, map[string]int{"cases_closed": e.Forget(body.Keys, body.Note)})
	})
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(v); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return false
	}
	return true
}

func reply(w http.ResponseWriter, v any, err error) {
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
