package main

import (
	"context"
	"encoding/json"
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

	type body struct {
		NetworkTxnID string `json:"network_txn_id"`
		Reason       string `json:"reason"`
		Amount       int64  `json:"amount"`
		Actor        string `json:"actor"`
		Evidence     string `json:"evidence"`
		Rebuttal     string `json:"rebuttal"`
		Note         string `json:"note"`
		Days         int    `json:"days"`
	}
	handle := func(path string, fn func(r *http.Request, b body) (dispute.Dispute, error)) {
		mux.HandleFunc("POST "+path, func(w http.ResponseWriter, r *http.Request) {
			var b body
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&b); err != nil {
				http.Error(w, "invalid body", http.StatusBadRequest)
				return
			}
			out, err := fn(r, b)
			if err != nil {
				writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
				return
			}
			log.Printf("dispute %s %s state=%s outcome=%s", out.ID, path, out.State, out.Outcome)
			writeJSON(w, http.StatusOK, out)
		})
	}
	handle("/api/disputes", func(r *http.Request, b body) (dispute.Dispute, error) {
		return d.File(r.Context(), b.NetworkTxnID, b.Reason, b.Amount, b.Note)
	})
	handle("/api/disputes/{id}/accept", func(r *http.Request, b body) (dispute.Dispute, error) {
		return d.Accept(r.Context(), r.PathValue("id"), b.Actor, b.Note)
	})
	handle("/api/disputes/{id}/represent", func(r *http.Request, b body) (dispute.Dispute, error) {
		return d.Represent(r.Context(), r.PathValue("id"), b.Evidence, b.Note)
	})
	handle("/api/disputes/{id}/prearbitrate", func(r *http.Request, b body) (dispute.Dispute, error) {
		return d.PreArbitrate(r.Context(), r.PathValue("id"), b.Rebuttal, b.Note)
	})
	handle("/api/disputes/{id}/arbitrate", func(r *http.Request, b body) (dispute.Dispute, error) {
		return d.Arbitrate(r.Context(), r.PathValue("id"), b.Note)
	})
	// A simulation control: deadlines run on business days, which otherwise
	// advance only at each end of day.
	mux.HandleFunc("POST /api/disputes/advance", func(w http.ResponseWriter, r *http.Request) {
		var b body
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&b); err != nil || b.Days <= 0 || b.Days > 365 {
			http.Error(w, "days must be 1-365", http.StatusBadRequest)
			return
		}
		changed := d.Advance(r.Context(), b.Days)
		writeJSON(w, http.StatusOK, map[string]any{"today": d.Today(), "closed": len(changed)})
	})

	go func() {
		for range time.Tick(5 * time.Second) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			d.Sync(ctx)
			cancel()
		}
	}()
	return d
}
