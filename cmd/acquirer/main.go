// Command acquirer runs the merchant's bank: the acquirer. Terminals send it
// 0100 authorization requests at POST /authorize. It checks that the
// merchant and terminal are signed up, adds its own fields, forwards the
// request to the card network, and passes the 0110 back to the terminal.
// When the network doesn't answer, it declines with 91 and sends the network
// an 0420 reversal advice until the network acknowledges it.
//
// Its back-office page on the same port shows each authorization's path,
// what the acquirer changed, and what it owes each merchant.
package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/JonahR/simple-network/internal/iso8583"
	"github.com/JonahR/simple-network/internal/learn"
	"github.com/JonahR/simple-network/internal/ui"
)

//go:embed web
var webFS embed.FS

// maxListed caps the transactions the back office returns.
const maxListed = 200

type server struct {
	acq *acquirer
}

func main() {
	addr := env("ACQUIRER_ADDR", ":8081")
	discount, err := strconv.ParseInt(env("MERCHANT_DISCOUNT_BPS", "250"), 10, 64)
	if err != nil {
		log.Fatal("MERCHANT_DISCOUNT_BPS must be a whole number of basis points")
	}
	// The terminal's own merchant is signed up from the same settings it uses.
	merchants := append([]Merchant{{
		ID:          env("MERCHANT_ID", "000000000012345"),
		Name:        env("MERCHANT_NAME", "Simple Coffee Co"),
		City:        env("MERCHANT_CITY", "San Francisco"),
		Country:     env("MERCHANT_COUNTRY", "US"),
		MCC:         env("MCC", "5814"),
		Terminals:   []string{env("TERMINAL_ID", "TERM0001")},
		DiscountBPS: discount,
	}}, demoMerchants...)

	network := newHTTPNetwork(env("NETWORK_URL", "http://localhost:8090"))
	s := &server{acq: newAcquirer(env("ACQUIRER_ID", "100001"), env("ACQUIRER_NAME", "Simple Merchant Bank"), merchants, network)}

	static, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /authorize", s.handleAuthorize)
	mux.HandleFunc("POST /reverse", s.handleReverse)
	mux.Handle("GET /", http.FileServerFS(static))
	mux.HandleFunc("GET /api/overview", s.handleOverview)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	ui.Register(mux, ui.PagesFromEnv())
	// Definitions for every label (Help mode), the learning hub, and the Network KT docs.
	mux.Handle("GET /learn/", learn.Handler(learn.Links{
		POS:       env("POS_PUBLIC_URL", "http://localhost:8080"),
		Dashboard: env("NETWORK_DASHBOARD_URL", "http://localhost:8090"),
	}))

	// On SIGINT or SIGTERM, stop taking requests, then stop retrying reversals.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv := &http.Server{Addr: addr, Handler: mux}
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	log.Printf("acquirer %s (%s) listening on %s, forwarding to network %s", s.acq.id, s.acq.name, addr, network.url)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	<-drained
	s.acq.Close()
}

// handleAuthorize is the terminal-facing endpoint.
func (s *server) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	var req iso8583.AuthRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil || req.MTI != iso8583.MTIAuthRequest {
		http.Error(w, "invalid 0100 message", http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, s.acq.Authorize(r.Context(), req))
}

// handleReverse takes an 0420 reversal advice from a terminal (a void, or its
// own timeout) and acknowledges it with an 0430.
func (s *server) handleReverse(w http.ResponseWriter, r *http.Request) {
	var adv iso8583.ReversalAdvice
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&adv); err != nil {
		http.Error(w, "invalid 0420 message", http.StatusBadRequest)
		return
	}
	ack, err := s.acq.TerminalReverse(adv)
	if err != nil {
		http.Error(w, "invalid 0420: "+err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, ack)
}

// Totals is what a merchant has earned in one currency since start-up,
// pending settlement.
type Totals struct {
	Currency string `json:"currency"`
	Approved int    `json:"approved"`
	Declined int    `json:"declined"`
	Sales    int64  `json:"sales"`   // Approved purchases, minor units
	Refunds  int64  `json:"refunds"` // Approved refunds, minor units
	Fees     int64  `json:"fees"`    // Merchant discount on sales
	Net      int64  `json:"net"`     // What the acquirer will pay the merchant
}

type merchantView struct {
	Merchant
	Totals []Totals `json:"totals"`
}

type stats struct {
	Count        int     `json:"count"`
	Approved     int     `json:"approved"`
	ApprovalRate float64 `json:"approval_rate"` // 0-1
	AvgMS        float64 `json:"avg_ms"`
	NetworkMS    float64 `json:"network_ms"` // Average round trip to the network
}

func (s *server) handleOverview(w http.ResponseWriter, _ *http.Request) {
	records := s.acq.Records()
	var st stats
	var totalMS, netMS float64
	var netCount int
	byMerchant := map[string]map[string]*Totals{}
	for _, rec := range records {
		st.Count++
		totalMS += rec.TotalMS
		for _, step := range rec.Steps {
			if step.From == PartyNetwork && step.MTI == iso8583.MTIAuthResponse {
				netMS += step.MS
				netCount++
			}
		}
		cur := rec.Request.Currency
		if byMerchant[rec.MerchantID] == nil {
			byMerchant[rec.MerchantID] = map[string]*Totals{}
		}
		t := byMerchant[rec.MerchantID][cur]
		if t == nil {
			t = &Totals{Currency: cur}
			byMerchant[rec.MerchantID][cur] = t
		}
		if rec.Status == "DECLINED" {
			t.Declined++
			continue
		}
		st.Approved++
		t.Approved++
		if rec.Reversal != "" {
			continue // Voided or reversed: the hold is released and the merchant isn't paid
		}
		if strings.HasPrefix(rec.Request.ProcessingCode, iso8583.TxnRefund) {
			t.Refunds += rec.Response.Amount
		} else {
			t.Sales += rec.Response.Amount
		}
	}
	if st.Count > 0 {
		st.ApprovalRate = float64(st.Approved) / float64(st.Count)
		st.AvgMS = totalMS / float64(st.Count)
	}
	if netCount > 0 {
		st.NetworkMS = netMS / float64(netCount)
	}

	merchants := make([]merchantView, len(s.acq.merchants))
	for i, m := range s.acq.merchants {
		merchants[i] = merchantView{Merchant: m, Totals: []Totals{}}
		for _, t := range byMerchant[m.ID] {
			t.Fees = t.Sales * m.DiscountBPS / 10_000
			t.Net = t.Sales - t.Refunds - t.Fees
			merchants[i].Totals = append(merchants[i].Totals, *t)
		}
	}
	if len(records) > maxListed {
		records = records[:maxListed]
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"bank":           map[string]string{"id": s.acq.id, "name": s.acq.name},
		"stats":          st,
		"merchants":      merchants,
		"transactions":   records,
		"response_codes": iso8583.ResponseCodes,
		"mccs":           iso8583.MCCs,
		"currencies":     iso8583.Currencies,
		"entry_modes":    iso8583.EntryModes,
	})
}

// httpNetwork sends authorizations to the network switch over HTTP.
type httpNetwork struct {
	url  string
	http *http.Client
}

func newHTTPNetwork(url string) *httpNetwork {
	return &httpNetwork{url: url, http: &http.Client{Timeout: networkTimeout}}
}

func (n *httpNetwork) Authorize(ctx context.Context, req iso8583.AuthRequest) (iso8583.AuthResponse, error) {
	var resp iso8583.AuthResponse
	return resp, n.post(ctx, "/authorize", req, &resp)
}

// Reverse sends an 0420 reversal advice and returns the network's 0430.
func (n *httpNetwork) Reverse(ctx context.Context, adv iso8583.ReversalAdvice) (iso8583.ReversalResponse, error) {
	var resp iso8583.ReversalResponse
	return resp, n.post(ctx, "/reverse", adv, &resp)
}

// post sends msg as JSON to the network and decodes a 200 reply into out.
func (n *httpNetwork) post(ctx context.Context, path string, msg, out any) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, n.url+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	res, err := n.http.Do(httpReq)
	if err != nil {
		if isTimeout(err) {
			return &netError{fmt.Sprintf("network at %s did not answer within %s", n.url, networkTimeout), err}
		}
		return &netError{fmt.Sprintf("network unreachable at %s", n.url), err}
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		text, _ := io.ReadAll(io.LimitReader(res.Body, 200))
		err := fmt.Errorf("network returned HTTP %d: %s", res.StatusCode, strings.TrimSpace(string(text)))
		if res.StatusCode >= 400 && res.StatusCode < 500 {
			return &rejectedError{err}
		}
		return err
	}
	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		return fmt.Errorf("unreadable network response: %v", err)
	}
	return nil
}

// netError is a short message for the back office that still unwraps to the
// transport error, so isTimeout can tell a timeout from a refused connection.
// rejectedError is an HTTP 4xx from the network: it refused the message
// itself, so sending it again won't help.
type rejectedError struct{ err error }

func (e *rejectedError) Error() string { return e.err.Error() }
func (e *rejectedError) Unwrap() error { return e.err }

type netError struct {
	msg string
	err error
}

func (e *netError) Error() string { return e.msg }
func (e *netError) Unwrap() error { return e.err }

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
