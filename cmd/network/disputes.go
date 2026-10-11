package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/JonahR/simple-network/internal/clearing"
	"github.com/JonahR/simple-network/internal/dispute"
	"github.com/JonahR/simple-network/internal/iso8583"
	"github.com/JonahR/simple-network/internal/network"
)

// saleFacts answers the dispute engine from the network's own records: the
// cleared sale, its authorization (3-D Secure, card on file, stand-in), the
// refunds linked to it, and any duplicate cleared the same day.
type saleFacts struct {
	eng *clearing.Engine
	rec *network.Recorder
}

func (f saleFacts) Facts(txnID string) (dispute.Facts, bool) {
	sale, date, ok := f.eng.ClearedSale(txnID)
	if !ok {
		return dispute.Facts{}, false
	}
	facts := dispute.Facts{Sale: sale, ClearedOn: date}
	if r, ok := f.rec.Get(txnID); ok {
		// Refunded counts approved linked refunds and drops them again if reversed.
		facts.ECI, facts.COF, facts.Refunded = r.Request.ECI, r.Request.COFIndicator, r.Refunded
		for _, s := range r.Steps {
			if s.Name == "stand_in" && s.OK {
				facts.StandIn = true
			}
		}
	}
	for _, other := range f.eng.ClearedSales() {
		if other.NetworkTxnID != txnID && other.PAN == sale.PAN && other.MerchantID == sale.MerchantID &&
			other.Amount == sale.Amount && other.TxnDate == sale.TxnDate {
			facts.DuplicateOf = other.NetworkTxnID
			break
		}
	}
	return facts, true
}

// httpDisputeNotifier tells an issuer where its cardholder's disputes stand.
type httpDisputeNotifier struct{ url string }

func (h httpDisputeNotifier) NotifyDispute(ctx context.Context, n dispute.Notice) error {
	return postJSON(ctx, h.url+"/disputes/notify", n, nil)
}

// registerDisputes sets up the dispute desk: the engine, its API for the
// Disputes department page, and a loop that enforces deadlines and resends
// notices issuers missed.
func registerDisputes(mux *http.ServeMux, eng *clearing.Engine, rec *network.Recorder, issuers []issuerConfig) *dispute.Engine {
	d := &dispute.Engine{Source: saleFacts{eng: eng, rec: rec}, Money: eng, Notifiers: map[string]dispute.Notifier{}, Now: time.Now}
	for _, ic := range issuers {
		d.Notifiers[ic.ID] = httpDisputeNotifier{url: ic.URL}
	}

	mux.HandleFunc("GET /api/disputes", func(w http.ResponseWriter, _ *http.Request) {
		v := d.View()
		sales := eng.ClearedSales()
		if sales == nil {
			sales = []clearing.Item{} // An empty list, not null, for the page
		}
		if len(sales) > 100 {
			sales = sales[:100]
		}
		writeJSON(w, http.StatusOK, map[string]any{"desk": v, "sales": sales, "names": eng.Names, "currencies": iso8583.Currencies})
	})
	mux.HandleFunc("GET /api/disputes/eligible", func(w http.ResponseWriter, r *http.Request) {
		amount, _ := strconv.ParseInt(r.URL.Query().Get("amount"), 10, 64)
		writeJSON(w, http.StatusOK, map[string]string{"refusal": d.Eligible(r.URL.Query().Get("txn"), r.URL.Query().Get("reason"), amount)})
	})

	handle := func(path string, fn func(r *http.Request, b disputeBody) (dispute.Dispute, error)) {
		handleDisputePost(mux, path, fn)
	}
	handle("/api/disputes", func(r *http.Request, b disputeBody) (dispute.Dispute, error) {
		return d.File(r.Context(), b.NetworkTxnID, b.Reason, b.Amount, b.Note)
	})
	handle("/api/disputes/{id}/accept", func(r *http.Request, b disputeBody) (dispute.Dispute, error) {
		return d.Accept(r.Context(), r.PathValue("id"), b.Actor, b.Note)
	})
	handle("/api/disputes/{id}/represent", func(r *http.Request, b disputeBody) (dispute.Dispute, error) {
		return d.Represent(r.Context(), r.PathValue("id"), b.Evidence, b.Note)
	})
	handle("/api/disputes/{id}/prearbitrate", func(r *http.Request, b disputeBody) (dispute.Dispute, error) {
		return d.PreArbitrate(r.Context(), r.PathValue("id"), b.Rebuttal, b.Note)
	})
	handle("/api/disputes/{id}/arbitrate", func(r *http.Request, b disputeBody) (dispute.Dispute, error) {
		return d.Arbitrate(r.Context(), r.PathValue("id"), b.Note)
	})
	// A simulation control: deadlines run on business days, which otherwise
	// advance only at each end of day.
	mux.HandleFunc("POST /api/disputes/advance", func(w http.ResponseWriter, r *http.Request) {
		var b disputeBody
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&b); err != nil || b.Days <= 0 || b.Days > 365 {
			http.Error(w, "days must be 1-365", http.StatusBadRequest)
			return
		}
		changed := d.Advance(r.Context(), b.Days)
		writeJSON(w, http.StatusOK, map[string]any{"today": d.Today(), "closed": len(changed)})
	})

	registerMemberDisputes(mux, d, eng, eng.Names)

	go func() {
		for range time.Tick(5 * time.Second) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			d.Sync(ctx)
			cancel()
		}
	}()
	return d
}

// disputeBody is what a dispute action posts.
type disputeBody struct {
	NetworkTxnID string `json:"network_txn_id"`
	Reason       string `json:"reason"`
	Amount       int64  `json:"amount"`
	Actor        string `json:"actor"`
	Evidence     string `json:"evidence"`
	Rebuttal     string `json:"rebuttal"`
	Note         string `json:"note"`
	Days         int    `json:"days"`
}

// handleDisputePost mounts one dispute action: it decodes the body, runs
// fn, and answers with the dispute or the reason the network refused.
func handleDisputePost(mux *http.ServeMux, path string, fn func(r *http.Request, b disputeBody) (dispute.Dispute, error)) {
	mux.HandleFunc("POST "+path, func(w http.ResponseWriter, r *http.Request) {
		var b disputeBody
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&b); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		out, err := fn(r, b)
		if err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
			return
		}
		log.Printf("dispute %s %s state=%s outcome=%s", out.ID, r.URL.Path, out.State, out.Outcome)
		writeJSON(w, http.StatusOK, out)
	})
}

// clearedSales is the clearing engine's record of cleared sales.
type clearedSales interface {
	ClearedSale(networkTxnID string) (clearing.Item, string, bool)
	ClearedSales() []clearing.Item
}

// memberActs lists the moves each side may make in the members' dispute
// system. Whose turn it is, the engine checks.
var memberActs = map[string]map[string]bool{
	dispute.ActorIssuer:   {"accept": true, "prearbitrate": true},
	dispute.ActorAcquirer: {"accept": true, "represent": true, "arbitrate": true},
}

// registerMemberDisputes is the members' side of the dispute desk, as
// issuers and acquirers reach Visa Resolve Online or Mastercom: each member
// sees only its own cases and may make only its own side's moves. Members
// name themselves in the path; a real network authenticates them.
func registerMemberDisputes(mux *http.ServeMux, d *dispute.Engine, eng clearedSales, names map[string]string) {
	const base = "/api/members/{side}/{member}/disputes"
	member := func(r *http.Request) (side, id string, err error) {
		side, id = r.PathValue("side"), r.PathValue("member")
		if _, ok := memberActs[side]; !ok || id == "" {
			return "", "", fmt.Errorf("unknown member side %q", side)
		}
		return side, id, nil
	}
	owns := func(side, id string, x dispute.Dispute) bool {
		return side == dispute.ActorIssuer && x.IssuerID == id || side == dispute.ActorAcquirer && x.AcquirerID == id
	}

	mux.HandleFunc("GET "+base, func(w http.ResponseWriter, r *http.Request) {
		side, id, err := member(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		v := d.View()
		mine := []dispute.Dispute{}
		for _, x := range v.Disputes {
			if owns(side, id, x) {
				mine = append(mine, x)
			}
		}
		v.Disputes = mine
		// An issuer files chargebacks against its own cardholders' sales.
		sales := []clearing.Item{}
		if side == dispute.ActorIssuer {
			for _, it := range eng.ClearedSales() {
				if it.IssuerID == id && len(sales) < 100 {
					sales = append(sales, it)
				}
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"side": side, "member": id, "desk": v, "sales": sales, "names": names, "currencies": iso8583.Currencies})
	})
	mux.HandleFunc("GET /api/members/issuer/{member}/disputes/eligible", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		refusal := ""
		if sale, _, ok := eng.ClearedSale(q.Get("txn")); ok && sale.IssuerID != r.PathValue("member") {
			refusal = "the sale isn't on one of this bank's cards"
		} else {
			amount, _ := strconv.ParseInt(q.Get("amount"), 10, 64)
			refusal = d.Eligible(q.Get("txn"), q.Get("reason"), amount)
		}
		writeJSON(w, http.StatusOK, map[string]string{"refusal": refusal})
	})
	handleDisputePost(mux, "/api/members/issuer/{member}/disputes", func(r *http.Request, b disputeBody) (dispute.Dispute, error) {
		if sale, _, ok := eng.ClearedSale(b.NetworkTxnID); ok && sale.IssuerID != r.PathValue("member") {
			return dispute.Dispute{}, errors.New("the sale isn't on one of this bank's cards")
		}
		return d.File(r.Context(), b.NetworkTxnID, b.Reason, b.Amount, b.Note)
	})
	handleDisputePost(mux, base+"/{id}/{act}", func(r *http.Request, b disputeBody) (dispute.Dispute, error) {
		side, id, err := member(r)
		if err != nil {
			return dispute.Dispute{}, err
		}
		act := r.PathValue("act")
		if !memberActs[side][act] {
			return dispute.Dispute{}, fmt.Errorf("an %s can't %s", side, act)
		}
		x, ok := d.Get(r.PathValue("id"))
		if !ok || !owns(side, id, x) {
			return dispute.Dispute{}, dispute.ErrNotFound
		}
		switch act {
		case "accept":
			return d.Accept(r.Context(), x.ID, side, b.Note)
		case "represent":
			return d.Represent(r.Context(), x.ID, b.Evidence, b.Note)
		case "prearbitrate":
			return d.PreArbitrate(r.Context(), x.ID, b.Rebuttal, b.Note)
		default: // arbitrate
			return d.Arbitrate(r.Context(), x.ID, b.Note)
		}
	})
}
