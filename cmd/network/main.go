// Command network runs the card network switch and its operations dashboard.
//
// Acquirers send 0100 authorization requests to POST /authorize. The switch
// routes each one to an issuer by BIN and returns the 0110. The dashboard on
// the same port shows transactions moving through the network as they happen.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/JonahR/simple-network/internal/card"
	"github.com/JonahR/simple-network/internal/demokeys"
	"github.com/JonahR/simple-network/internal/iso8583"
	"github.com/JonahR/simple-network/internal/network"
)

//go:embed web
var webFS embed.FS

type issuerConfig struct {
	ID, Name, URL string
}

type server struct {
	sw      *network.Switch
	issuers []issuerConfig // In display order
	http    *http.Client
}

func main() {
	addr := env("NETWORK_ADDR", ":8090")
	acquirerID := env("ACQUIRER_ID", "100001")
	acqKey := mustKey("ACQUIRER_PIN_KEY", demokeys.AcquirerPIN)

	issuers := []issuerConfig{
		{"FSB", "First Simple Bank", env("ISSUER_FSB_URL", "http://localhost:8091")},
		{"UCB", "Union Card Bank", env("ISSUER_UCB_URL", "http://localhost:8092")},
	}
	keys := map[string][]byte{
		"FSB": mustKey("ISSUER_FSB_PIN_KEY", demokeys.IssuerFSB),
		"UCB": mustKey("ISSUER_UCB_PIN_KEY", demokeys.IssuerUCB),
	}

	sw := &network.Switch{
		BINs:          network.DefaultBINs(),
		Vault:         network.DefaultVault(),
		Issuers:       map[string]*network.Issuer{},
		AcquirerKeys:  map[string][]byte{acquirerID: acqKey},
		IssuerTimeout: 5 * time.Second,
		ReversalRetry: 2 * time.Second,
		Recorder:      network.NewRecorder(1000),
		Now:           time.Now,
	}
	for _, ic := range issuers {
		sw.Issuers[ic.ID] = &network.Issuer{
			ID:      ic.ID,
			Name:    ic.Name,
			Client:  &network.HTTPIssuer{BaseURL: ic.URL},
			PINKey:  keys[ic.ID],
			Breaker: network.NewBreaker(3, 15*time.Second, time.Now),
		}
	}

	s := &server{sw: sw, issuers: issuers, http: &http.Client{Timeout: 2 * time.Second}}
	static, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /authorize", s.handleAuthorize)
	mux.Handle("GET /", http.FileServerFS(static))
	mux.HandleFunc("GET /api/overview", s.handleOverview)
	mux.HandleFunc("GET /api/transactions", s.handleTransactions)
	mux.HandleFunc("GET /api/transactions/{id}", s.handleTransaction)
	mux.HandleFunc("GET /api/stream", s.handleStream)
	mux.HandleFunc("POST /api/tokens/{token}/active", s.handleTokenActive)
	mux.HandleFunc("POST /api/sim/issuers/{id}/mode", s.handleIssuerMode)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })

	log.Printf("network switch and dashboard listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

// handleAuthorize is the acquirer-facing endpoint.
func (s *server) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	var req iso8583.AuthRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		http.Error(w, "invalid 0100 message", http.StatusBadRequest)
		return
	}
	resp := s.sw.Authorize(r.Context(), req)
	log.Printf("auth txn=%s acq=%s pan=%s amount=%d code=%s",
		resp.NetworkTxnID, req.AcquirerID, card.Mask(req.PAN), req.Amount, resp.ResponseCode)
	writeJSON(w, http.StatusOK, resp)
}

type issuerView struct {
	ID      string              `json:"id"`
	Name    string              `json:"name"`
	Breaker string              `json:"breaker"`
	Mode    string              `json:"mode"` // Simulated fault mode, or "unreachable"
	BINs    []string            `json:"bins"`
	Stats   network.IssuerStats `json:"stats"`
}

type tokenView struct {
	Token       string `json:"token"`
	TokenMasked string `json:"token_masked"`
	PAN         string `json:"pan"` // Masked
	TokenExpiry string `json:"token_expiry"`
	Wallet      string `json:"wallet"`
	Active      bool   `json:"active"`
}

func (s *server) handleOverview(w http.ResponseWriter, r *http.Request) {
	stats := s.sw.Recorder.Stats()
	issuers := make([]issuerView, len(s.issuers))
	for i, ic := range s.issuers {
		v := issuerView{ID: ic.ID, Name: ic.Name, Breaker: s.sw.Issuers[ic.ID].Breaker.State(), Mode: s.issuerMode(r.Context(), ic), Stats: stats.ByIssuer[ic.ID]}
		for _, e := range s.sw.BINs.Entries {
			if e.IssuerID == ic.ID {
				v.BINs = append(v.BINs, e.Prefix)
			}
		}
		issuers[i] = v
	}

	var tokens []tokenView
	for _, t := range s.sw.Vault.All() {
		// The full token goes to the dashboard so an operator can suspend it;
		// tokens are useless without the device's cryptogram keys.
		tokens = append(tokens, tokenView{
			Token: t.Token, TokenMasked: card.Mask(t.Token), PAN: card.Mask(t.PAN),
			TokenExpiry: t.TokenExpiry, Wallet: iso8583.Wallets[t.Wallet], Active: t.Active,
		})
	}
	slices.SortFunc(tokens, func(a, b tokenView) int { return strings.Compare(a.PAN+a.Wallet, b.PAN+b.Wallet) })

	writeJSON(w, http.StatusOK, map[string]any{
		"issuers":        issuers,
		"bins":           s.sw.BINs,
		"tokens":         tokens,
		"stats":          stats,
		"response_codes": iso8583.ResponseCodes,
		"entry_modes":    iso8583.EntryModes,
		"wallets":        iso8583.Wallets,
		"currencies":     iso8583.Currencies,
		"product_names":  card.ProductNames,
		"issuer_timeout": s.sw.IssuerTimeout.String(),
	})
}

// issuerMode asks the issuer's simulation endpoint for its fault mode.
func (s *server) issuerMode(ctx context.Context, ic issuerConfig) string {
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ic.URL+"/admin/mode", nil)
	res, err := s.http.Do(req)
	if err != nil {
		return "unreachable"
	}
	defer res.Body.Close()
	var body struct{ Mode string }
	if json.NewDecoder(res.Body).Decode(&body) != nil {
		return "unreachable"
	}
	return body.Mode
}

func (s *server) handleTransactions(w http.ResponseWriter, r *http.Request) {
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || n <= 0 || n > 1000 {
		n = 100
	}
	writeJSON(w, http.StatusOK, s.sw.Recorder.Recent(n))
}

func (s *server) handleTransaction(w http.ResponseWriter, r *http.Request) {
	rec, ok := s.sw.Recorder.Get(r.PathValue("id"))
	if !ok {
		http.Error(w, "transaction not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

// handleStream sends transaction and issuer-health events as server-sent events.
func (s *server) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	events, stop := s.sw.Recorder.Subscribe()
	defer stop()
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			fmt.Fprint(w, ": heartbeat\n\n")
		case ev := <-events:
			data, _ := json.Marshal(ev)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, data)
		}
		flusher.Flush()
	}
}

// handleTokenActive suspends or reactivates a device token.
func (s *server) handleTokenActive(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Active bool `json:"active"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if !s.sw.Vault.SetActive(r.PathValue("token"), body.Active) {
		http.Error(w, "token not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"active": body.Active})
}

// handleIssuerMode forwards a fault-mode change to an issuer's simulation
// endpoint. It is a simulation control, not something a real network can do.
func (s *server) handleIssuerMode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	i := slices.IndexFunc(s.issuers, func(ic issuerConfig) bool { return ic.ID == id })
	if i < 0 {
		http.Error(w, "unknown issuer", http.StatusNotFound)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1024))
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, s.issuers[i].URL+"/admin/mode", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	res, err := s.http.Do(req)
	if err != nil {
		http.Error(w, "issuer unreachable", http.StatusBadGateway)
		return
	}
	defer res.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(res.StatusCode)
	io.Copy(w, res.Body)
	s.sw.Recorder.PublishHealth()
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

func mustKey(name, def string) []byte {
	k, err := demokeys.Load(name, def)
	if err != nil {
		log.Fatal(err)
	}
	return k
}
