// Command issuer runs the simulation's issuing banks. Each bank listens on its
// own port with its own accounts and PIN key, so the network talks to them as
// separate members. Each port also serves that bank's back-office page: its
// accounts, holds, and every authorization decision with the checks behind it.
//
// Each bank also has a fault mode for demonstrating network resilience:
// "normal", "slow" (answers after the network's timeout), or "down" (refuses
// every request). Set it with POST /admin/mode {"mode": "slow"}.
package main

import (
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"math/rand/v2"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"github.com/JonahR/simple-network/internal/card"
	"github.com/JonahR/simple-network/internal/clearing"
	"github.com/JonahR/simple-network/internal/demokeys"
	"github.com/JonahR/simple-network/internal/dispute"
	"github.com/JonahR/simple-network/internal/iso8583"
	"github.com/JonahR/simple-network/internal/issuer"
	"github.com/JonahR/simple-network/internal/jsonapi"
	"github.com/JonahR/simple-network/internal/learn"
	"github.com/JonahR/simple-network/internal/store"
	"github.com/JonahR/simple-network/internal/ui"
)

//go:embed web
var webFS embed.FS

const slowDelay = 7 * time.Second // Longer than the network's 5s issuer timeout

// maxListed caps the decisions the back office returns.
const maxListed = 200

var modes = map[string]bool{"normal": true, "slow": true, "down": true}

// bankLink is one bank's page, for the switcher between banks.
type bankLink struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

type bankServer struct {
	bank  *issuer.Bank
	banks []bankLink
	mode  atomic.Value // string

	// peers is every bank in this process, so online banking can send a
	// customer who banks elsewhere to the right bank's site.
	peers []*bankServer

	// disputes files chargebacks the bank's cardholders ask for.
	disputes *dispute.Member
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
	// Browser-facing URLs of each bank's page. In Docker they differ from
	// the URLs the network uses.
	links := []bankLink{
		{"FSB", "First Simple Bank", env("ISSUER_FSB_PUBLIC_URL", "http://localhost:8091")},
		{"UCB", "Union Card Bank", env("ISSUER_UCB_PUBLIC_URL", "http://localhost:8092")},
	}

	static, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatal(err)
	}
	learnHandler := learn.Handler(learn.Links{
		POS:       env("POS_PUBLIC_URL", "http://localhost:8080"),
		Dashboard: env("NETWORK_DASHBOARD_URL", "http://localhost:8090"),
	})

	// Saved state survives restarts: accounts, holds, posted charges and logs.
	st := store.Open(store.Dir(), "issuer")
	saved := map[string]issuer.BankState{}
	if ok, err := st.Load(&saved); err != nil {
		log.Printf("store: %v", err)
	} else if ok {
		for _, b := range banks {
			if bs, ok := saved[b.bank.ID]; ok {
				b.bank.RestoreState(bs)
			}
		}
		log.Printf("restored %d banks from %s", len(saved), st.Path())
	}
	st.Run(func() any {
		out := map[string]issuer.BankState{}
		for _, b := range banks {
			out[b.bank.ID] = b.bank.SaveState()
		}
		return out
	}, store.DefaultInterval)
	store.SaveOnExit(nil, st)

	networkURL := env("NETWORK_URL", "http://localhost:8090")
	errs := make(chan error)
	var servers []*bankServer
	for _, b := range banks {
		servers = append(servers, &bankServer{bank: b.bank, banks: links, disputes: dispute.NewIssuer(networkURL, b.bank.ID)})
	}
	for i, b := range banks {
		s := servers[i]
		s.peers = servers
		s.mode.Store("normal")
		mux := http.NewServeMux()
		mux.HandleFunc("POST /authorize", s.handleAuthorize)
		mux.HandleFunc("POST /reverse", s.handleReverse)
		mux.HandleFunc("POST /advice", s.handleAdvice)
		mux.HandleFunc("POST /disputes/notify", s.handleDisputeNotice)
		mux.HandleFunc("POST /3ds/authenticate", s.handleAuthenticate3DS)
		mux.HandleFunc("POST /3ds/challenge", s.handleChallenge3DS)
		mux.HandleFunc("POST /clearing/presentments", s.handlePresentments)
		mux.HandleFunc("GET /admin/mode", s.handleGetMode)
		mux.HandleFunc("POST /admin/mode", s.handleSetMode)
		mux.Handle("GET /", http.FileServerFS(static))
		mux.HandleFunc("GET /api/overview", s.handleOverview)
		mux.HandleFunc("POST /api/accounts/{id}/blocked", s.handleBlocked)
		// The cardholder's online banking at /banking/ (see banking.go).
		mux.HandleFunc("GET /api/banking/customers", s.handleCustomers)
		mux.HandleFunc("GET /api/banking/accounts/{id}", s.handleStatement)
		mux.HandleFunc("POST /api/banking/accounts/{id}/disputes", s.handleDisputeCharge)
		mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
		// The bank's dispute desk, through the network's dispute system.
		dispute.RegisterDesk(mux, networkURL, dispute.ActorIssuer, b.bank.ID)
		ui.Register(mux, ui.PagesFromEnv())
		mux.Handle("GET /learn/", learnHandler)
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
	writeJSON(w, http.StatusOK, resp)
}

// handleDisputeNotice applies the network's word on a chargeback: the
// cardholder's provisional credit while it's open or won, a rebill if the
// merchant wins.
func (s *bankServer) handleDisputeNotice(w http.ResponseWriter, r *http.Request) {
	if s.mode.Load() == "down" {
		http.Error(w, "issuer down (simulated)", http.StatusServiceUnavailable)
		return
	}
	if !jsonapi.Require(w, r) {
		return
	}
	var n issuer.DisputeNotice
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&n); err != nil || n.DisputeID == "" {
		http.Error(w, "invalid dispute notice", http.StatusBadRequest)
		return
	}
	if err := s.bank.ApplyDispute(n); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	log.Printf("%s dispute %s txn=%s state=%s credit=%v", s.bank.ID, n.DisputeID, n.NetworkTxnID, n.State, n.Credit)
	writeJSON(w, http.StatusOK, map[string]bool{"applied": true})
}

// handleAdvice honors the network's 0120 for an authorization it approved
// in stand-in while this bank couldn't answer.
func (s *bankServer) handleAdvice(w http.ResponseWriter, r *http.Request) {
	if s.mode.Load() == "down" {
		http.Error(w, "issuer down (simulated)", http.StatusServiceUnavailable)
		return
	}
	var adv iso8583.AuthAdvice
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&adv); err != nil || adv.MTI != iso8583.MTIAuthAdvice {
		http.Error(w, "invalid 0120 advice", http.StatusBadRequest)
		return
	}
	resp := s.bank.Advise(adv)
	log.Printf("%s advice txn=%s pan=%s amount=%d held=%v %s", s.bank.ID, adv.NetworkTxnID, adv.PAN, adv.Amount, resp.Held, resp.Note)
	writeJSON(w, http.StatusOK, resp)
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
	log.Printf("%s reversal txn=%s stan=%s reason=%s matched=%v", s.bank.ID, adv.NetworkTxnID, adv.STAN, adv.ResponseCode, resp.Matched)
	writeJSON(w, http.StatusOK, resp)
}

// handlePresentments posts the network's clearing file: holds become charges.
func (s *bankServer) handlePresentments(w http.ResponseWriter, r *http.Request) {
	if s.mode.Load() == "down" {
		http.Error(w, "issuer down (simulated)", http.StatusServiceUnavailable)
		return
	}
	var f clearing.IssuerFile
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<22)).Decode(&f); err != nil || f.IssuerID != s.bank.ID {
		http.Error(w, "invalid clearing file", http.StatusBadRequest)
		return
	}
	ack := s.bank.Post(f)
	log.Printf("%s clearing file=%s records=%d posted=%d unmatched=%d", s.bank.ID, f.FileID, len(f.Records), ack.Posted, len(ack.Unmatched))
	writeJSON(w, http.StatusOK, ack)
}

func (s *bankServer) handleGetMode(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"mode": s.mode.Load().(string)})
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
	writeJSON(w, http.StatusOK, map[string]string{"mode": body.Mode})
}

// handleBlocked freezes or unfreezes a card, as a cardholder can in their
// bank's app.
func (s *bankServer) handleBlocked(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Blocked bool `json:"blocked"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if !s.bank.SetBlocked(r.PathValue("id"), body.Blocked) {
		http.Error(w, "account not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"blocked": body.Blocked})
}

type stats struct {
	Count        int     `json:"count"`
	Approved     int     `json:"approved"`
	ApprovalRate float64 `json:"approval_rate"` // 0-1
	HeldAmount   int64   `json:"held_amount"`   // Sum of active holds, minor units
	HeldCount    int     `json:"held_count"`
	Reversals    int     `json:"reversals"`
}

func (s *bankServer) handleOverview(w http.ResponseWriter, _ *http.Request) {
	st := s.bank.State()
	var sum stats
	for _, d := range st.Decisions {
		sum.Count++
		if iso8583.IsApproved(d.ResponseCode) {
			sum.Approved++
		}
	}
	if sum.Count > 0 {
		sum.ApprovalRate = float64(sum.Approved) / float64(sum.Count)
	}
	for _, a := range st.Accounts {
		sum.HeldAmount += a.Held
		sum.HeldCount += a.Holds
	}
	sum.Reversals = len(st.Reversals)
	if len(st.Decisions) > maxListed {
		st.Decisions = st.Decisions[:maxListed]
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"bank":           bankLink{ID: s.bank.ID, Name: s.bank.Name},
		"banks":          s.banks,
		"mode":           s.mode.Load(),
		"stats":          sum,
		"accounts":       st.Accounts,
		"decisions":      st.Decisions,
		"reversals":      st.Reversals,
		"response_codes": iso8583.ResponseCodes,
		"entry_modes":    iso8583.EntryModes,
		"wallets":        iso8583.Wallets,
		"product_names":  card.ProductNames,
		"currencies":     iso8583.Currencies,
		"mccs":           iso8583.MCCs,
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
