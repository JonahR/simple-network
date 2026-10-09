// Command issuer runs the simulation's issuing banks. Each bank listens on its
// own port with its own accounts and PIN key, so the network talks to them as
// separate members.
//
// Each bank also has a fault mode for demonstrating network resilience:
// "normal", "slow" (answers after the network's timeout), or "down" (refuses
// every request). Set it with POST /admin/mode {"mode": "slow"}.
package main

import (
	"encoding/json"
	"log"
	"math/rand/v2"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"github.com/JonahR/simple-network/internal/card"
	"github.com/JonahR/simple-network/internal/demokeys"
	"github.com/JonahR/simple-network/internal/iso8583"
	"github.com/JonahR/simple-network/internal/issuer"
)

const slowDelay = 7 * time.Second // Longer than the network's 5s issuer timeout

var modes = map[string]bool{"normal": true, "slow": true, "down": true}

type bankServer struct {
	bank *issuer.Bank
	mode atomic.Value // string
}

func main() {
	fsbKey, err := demokeys.Load("ISSUER_FSB_PIN_KEY", demokeys.IssuerFSB)
	if err != nil {
		log.Fatal(err)
	}
	ucbKey, err := demokeys.Load("ISSUER_UCB_PIN_KEY", demokeys.IssuerUCB)
	if err != nil {
		log.Fatal(err)
	}
	fa, fe := issuer.FirstSimpleBankAccounts()
	ua, ue := issuer.UnionCardBankAccounts()
	banks := []struct {
		bank *issuer.Bank
		addr string
	}{
		{issuer.NewBank("FSB", "First Simple Bank", fsbKey, fa, fe), env("ISSUER_FSB_ADDR", ":8091")},
		{issuer.NewBank("UCB", "Union Card Bank", ucbKey, ua, ue), env("ISSUER_UCB_ADDR", ":8092")},
	}

	errs := make(chan error)
	for _, b := range banks {
		s := &bankServer{bank: b.bank}
		s.mode.Store("normal")
		mux := http.NewServeMux()
		mux.HandleFunc("POST /authorize", s.handleAuthorize)
		mux.HandleFunc("POST /reverse", s.handleReverse)
		mux.HandleFunc("GET /admin/mode", s.handleGetMode)
		mux.HandleFunc("POST /admin/mode", s.handleSetMode)
		mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
		log.Printf("%s (%s) listening on %s", b.bank.Name, b.bank.ID, b.addr)
		go func(addr string) { errs <- http.ListenAndServe(addr, mux) }(b.addr)
	}
	log.Fatal(<-errs)
}

func (s *bankServer) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	switch s.mode.Load() {
	case "down":
		http.Error(w, "issuer down (simulated)", http.StatusServiceUnavailable)
		return
	case "slow":
		time.Sleep(slowDelay)
	default:
		// Real issuers take tens of milliseconds; make latency visible.
		time.Sleep(time.Duration(20+rand.IntN(100)) * time.Millisecond)
	}
	var req iso8583.AuthRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	resp := s.bank.Authorize(req)
	log.Printf("%s auth txn=%s pan=%s amount=%d code=%s approved=%d",
		s.bank.ID, req.NetworkTxnID, card.Mask(req.PAN), req.Amount, resp.ResponseCode, resp.Amount)
	writeJSON(w, resp)
}

func (s *bankServer) handleReverse(w http.ResponseWriter, r *http.Request) {
	if s.mode.Load() == "down" {
		http.Error(w, "issuer down (simulated)", http.StatusServiceUnavailable)
		return
	}
	var adv iso8583.ReversalAdvice
	if err := json.NewDecoder(r.Body).Decode(&adv); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	resp := s.bank.Reverse(adv)
	log.Printf("%s reversal txn=%s matched=%v", s.bank.ID, adv.NetworkTxnID, resp.Matched)
	writeJSON(w, resp)
}

func (s *bankServer) handleGetMode(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]string{"mode": s.mode.Load().(string)})
}

func (s *bankServer) handleSetMode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !modes[body.Mode] {
		http.Error(w, `mode must be "normal", "slow", or "down"`, http.StatusBadRequest)
		return
	}
	s.mode.Store(body.Mode)
	log.Printf("%s mode set to %s", s.bank.ID, body.Mode)
	writeJSON(w, map[string]string{"mode": body.Mode})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
