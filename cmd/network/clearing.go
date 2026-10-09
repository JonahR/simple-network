package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/JonahR/simple-network/internal/card"
	"github.com/JonahR/simple-network/internal/clearing"
	"github.com/JonahR/simple-network/internal/iso8583"
	"github.com/JonahR/simple-network/internal/network"
)

// openingBalance funds each member's settlement-bank account ($100,000.00)
// so the settlement page can show money moving.
const openingBalance = 10_000_000

// recordAuths matches clearing records to the switch's authorization records.
type recordAuths struct {
	rec  *network.Recorder
	bins *network.BinTable
}

func (a recordAuths) Lookup(id string) (clearing.AuthInfo, bool) {
	r, ok := a.rec.Get(id)
	if !ok || r.Response == nil {
		return clearing.AuthInfo{}, false
	}
	info := clearing.AuthInfo{
		AcquirerID: r.AcquirerID,
		IssuerID:   r.IssuerID,
		Approved:   r.Status == "approved",
		Amount:     r.Response.Amount,
		Currency:   r.Response.Currency,
		AuthCode:   r.Response.AuthCode,
	}
	// The card's product comes from its BIN. The issuer leg carries the card
	// number even for wallet payments; records keep its first six digits.
	if r.IssuerRequest != nil {
		if e, ok := a.bins.Lookup(card.BIN(r.IssuerRequest.PAN)); ok {
			info.Product = e.Product
		}
	}
	info.Reversed = r.Reversed
	return info, true
}

// httpClearingIssuer delivers outgoing clearing files to an issuer.
type httpClearingIssuer struct{ url string }

func (h httpClearingIssuer) Post(ctx context.Context, f clearing.IssuerFile) (clearing.IssuerAck, error) {
	var ack clearing.IssuerAck
	err := postJSON(ctx, h.url+"/clearing/presentments", f, &ack)
	return ack, err
}

// httpClearingAcquirer asks an acquirer for its file and sends it advices.
type httpClearingAcquirer struct{ url string }

func (h httpClearingAcquirer) RequestFile(ctx context.Context) error {
	return postJSON(ctx, h.url+"/clearing/submit", struct{}{}, nil)
}

func (h httpClearingAcquirer) Advise(ctx context.Context, a clearing.Advice) error {
	return postJSON(ctx, h.url+"/settlement/advice", a, nil)
}

func postJSON(ctx context.Context, url string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("unreachable")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", res.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(res.Body).Decode(out)
}

// registerClearing sets up clearing and settlement: acquirers send files to
// POST /clearing/files, and the settlement page at /settlement/ shows each
// cycle and can run the end of day.
func registerClearing(mux *http.ServeMux, sw *network.Switch, issuers []issuerConfig, acquirerID, acquirerName, acquirerURL string) *clearing.Engine {
	eng := &clearing.Engine{
		Fees:           clearing.DefaultFees(),
		Auths:          recordAuths{rec: sw.Recorder, bins: sw.BINs},
		Issuers:        map[string]clearing.IssuerClient{},
		Acquirers:      map[string]clearing.AcquirerClient{acquirerID: httpClearingAcquirer{url: acquirerURL}},
		Names:          map[string]string{acquirerID: acquirerName, clearing.NetworkID: "simple-network"},
		Now:            time.Now,
		OpeningBalance: openingBalance,
		CallTimeout:    sw.IssuerTimeout,
	}
	for _, ic := range issuers {
		eng.Issuers[ic.ID] = httpClearingIssuer{url: ic.URL}
		eng.Names[ic.ID] = ic.Name
	}

	mux.HandleFunc("POST /clearing/files", func(w http.ResponseWriter, r *http.Request) {
		var f clearing.File
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<22)).Decode(&f); err != nil {
			http.Error(w, "invalid clearing file", http.StatusBadRequest)
			return
		}
		ack := eng.Receive(f)
		log.Printf("clearing file=%s acq=%s seq=%d status=%s accepted=%d rejected=%d",
			f.Header.FileID, f.Header.AcquirerID, f.Header.Sequence, ack.Status, ack.Accepted, len(ack.Rejected))
		writeJSON(w, http.StatusOK, ack)
	})
	mux.HandleFunc("GET /api/settlement", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"engine":        eng.View(60),
			"names":         eng.Names,
			"currencies":    iso8583.Currencies,
			"entry_modes":   iso8583.EntryModes,
			"product_names": card.ProductNames,
			"opening":       openingBalance,
			"network_id":    clearing.NetworkID,
		})
	})
	// The simulated end of day: collect clearing files, close the cycle, settle.
	mux.HandleFunc("POST /api/settlement/run", func(w http.ResponseWriter, r *http.Request) {
		c, err := eng.Run(r.Context())
		log.Printf("cycle %s %s: %d cleared, %d rejected, %d transfers", c.ID, c.Status, len(c.Items), len(c.Rejected), len(c.Transfers))
		resp := map[string]any{"cycle": c}
		if err != nil {
			resp["warning"] = err.Error()
		}
		writeJSON(w, http.StatusOK, resp)
	})
	return eng
}
