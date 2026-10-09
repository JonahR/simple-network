package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JonahR/simple-network/internal/demokeys"
	"github.com/JonahR/simple-network/internal/iso8583"
	"github.com/JonahR/simple-network/internal/issuer"
	"github.com/JonahR/simple-network/internal/learn"
	"github.com/JonahR/simple-network/internal/network"
)

// These tests run every drill against the real switch and issuer banks,
// in-process over HTTP, with a small stand-in for the acquirer. A drill whose
// expectations no longer match how the network behaves shows up as an
// "unexpected" row here before anyone sees it on the page.

const testAcquirerID = "100001"

func key(t *testing.T, hexKey string) []byte {
	t.Helper()
	k, err := demokeys.Load("RISKLAB_TEST_UNSET_KEY", hexKey)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

type testWorld struct {
	lab *Lab
	sw  *network.Switch
}

func newWorld(t *testing.T) *testWorld {
	t.Helper()
	acqKey := key(t, demokeys.AcquirerPIN)
	fa, fe := issuer.FirstSimpleBankAccounts()
	ua, ue := issuer.UnionCardBankAccounts()
	banks := map[string]*issuer.Bank{
		"FSB": issuer.NewBank("FSB", "First Simple Bank", key(t, demokeys.IssuerFSB), fa, fe),
		"UCB": issuer.NewBank("UCB", "Union Card Bank", key(t, demokeys.IssuerUCB), ua, ue),
	}
	issuerURLs := map[string]string{}
	sw := &network.Switch{
		BINs: network.DefaultBINs(), Vault: network.DefaultVault(), Issuers: map[string]*network.Issuer{},
		AcquirerKeys:  map[string][]byte{testAcquirerID: acqKey},
		IssuerTimeout: 300 * time.Millisecond, ReversalRetry: 50 * time.Millisecond, ReversalRetryMax: 200 * time.Millisecond,
		Recorder: network.NewRecorder(1000), Now: time.Now,
	}
	for id, b := range banks {
		srv := httptest.NewServer(issuerHandler(b))
		t.Cleanup(srv.Close)
		issuerURLs[id] = srv.URL
		sw.Issuers[id] = &network.Issuer{
			ID: id, Name: b.Name, Client: &network.HTTPIssuer{BaseURL: srv.URL}, PINKey: key(t, map[string]string{"FSB": demokeys.IssuerFSB, "UCB": demokeys.IssuerUCB}[id]),
			Breaker: network.NewBreaker(3, 15*time.Second, time.Now),
		}
	}
	t.Cleanup(sw.Close)
	netSrv := httptest.NewServer(networkHandler(sw))
	t.Cleanup(netSrv.Close)
	acqSrv := httptest.NewServer(newTestAcquirer(sw).handler())
	t.Cleanup(acqSrv.Close)

	lab := NewLab(Config{
		AcquirerURL: acqSrv.URL, NetworkURL: netSrv.URL, IssuerURLs: issuerURLs,
		AcquirerID: testAcquirerID, MerchantID: "000000000099001", TerminalID: "RISK0001", PINKey: acqKey,
	})
	t.Cleanup(lab.Close)
	return &testWorld{lab: lab, sw: sw}
}

// issuerHandler serves a bank the way cmd/issuer does, with its fault modes.
func issuerHandler(b *issuer.Bank) http.Handler {
	var mode atomic.Value
	mode.Store("normal")
	mux := http.NewServeMux()
	mux.HandleFunc("POST /authorize", func(w http.ResponseWriter, r *http.Request) {
		switch mode.Load() {
		case "down":
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		case "slow":
			time.Sleep(500 * time.Millisecond) // Longer than the switch's timeout
		}
		var req iso8583.AuthRequest
		json.NewDecoder(r.Body).Decode(&req)
		writeJSON(w, http.StatusOK, b.Authorize(req))
	})
	mux.HandleFunc("POST /reverse", func(w http.ResponseWriter, r *http.Request) {
		if mode.Load() == "down" {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		var adv iso8583.ReversalAdvice
		json.NewDecoder(r.Body).Decode(&adv)
		writeJSON(w, http.StatusOK, b.Reverse(adv))
	})
	mux.HandleFunc("GET /admin/mode", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"mode": mode.Load()})
	})
	mux.HandleFunc("POST /admin/mode", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Mode string }
		json.NewDecoder(r.Body).Decode(&body)
		mode.Store(body.Mode)
		writeJSON(w, http.StatusOK, body)
	})
	mux.HandleFunc("GET /api/overview", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, b.State())
	})
	mux.HandleFunc("POST /api/accounts/{id}/blocked", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Blocked bool }
		json.NewDecoder(r.Body).Decode(&body)
		if !b.SetBlocked(r.PathValue("id"), body.Blocked) {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, body)
	})
	return mux
}

// networkHandler serves the switch endpoints the lab uses.
func networkHandler(sw *network.Switch) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /authorize", func(w http.ResponseWriter, r *http.Request) {
		var req iso8583.AuthRequest
		json.NewDecoder(r.Body).Decode(&req)
		writeJSON(w, http.StatusOK, sw.Authorize(r.Context(), req))
	})
	mux.HandleFunc("POST /reverse", func(w http.ResponseWriter, r *http.Request) {
		var adv iso8583.ReversalAdvice
		json.NewDecoder(r.Body).Decode(&adv)
		ack, err := sw.Reverse(adv)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, ack)
	})
	mux.HandleFunc("GET /api/transactions/{id}", func(w http.ResponseWriter, r *http.Request) {
		rec, ok := sw.Recorder.Get(r.PathValue("id"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, rec)
	})
	mux.HandleFunc("POST /api/tokens/{token}/active", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Active bool }
		json.NewDecoder(r.Body).Decode(&body)
		if !sw.Vault.SetActive(r.PathValue("token"), body.Active) {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, body)
	})
	return mux
}

// testAcquirer does what the drills rely on from cmd/acquirer: it knows only
// the Risk Lab terminal, renumbers the network leg, answers a repeat from
// its cache, and turns a terminal's void into its own 0420.
type testAcquirer struct {
	sw   *network.Switch
	stan atomic.Uint32
	mu   sync.Mutex
	seen map[string]iso8583.AuthResponse
	legs map[string]iso8583.AuthRequest
}

func newTestAcquirer(sw *network.Switch) *testAcquirer {
	return &testAcquirer{sw: sw, seen: map[string]iso8583.AuthResponse{}, legs: map[string]iso8583.AuthRequest{}}
}

func (a *testAcquirer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /authorize", func(w http.ResponseWriter, r *http.Request) {
		var req iso8583.AuthRequest
		json.NewDecoder(r.Body).Decode(&req)
		k := req.MerchantID + "|" + req.TerminalID + "|" + req.STAN + "|" + req.TransmissionTime
		a.mu.Lock()
		cached, ok := a.seen[k]
		a.mu.Unlock()
		if ok {
			writeJSON(w, http.StatusOK, cached)
			return
		}
		if req.MerchantID != "000000000099001" || req.TerminalID != "RISK0001" {
			writeJSON(w, http.StatusOK, iso8583.AuthResponse{MTI: iso8583.MTIAuthResponse, STAN: req.STAN, ResponseCode: iso8583.RCInvalidMerchant})
			return
		}
		out := req
		out.AcquirerID = testAcquirerID
		out.STAN = fmt.Sprintf("%06d", a.stan.Add(1))
		resp := a.sw.Authorize(r.Context(), out)
		resp.STAN = req.STAN
		a.mu.Lock()
		a.seen[k], a.legs[k] = resp, out
		a.mu.Unlock()
		writeJSON(w, http.StatusOK, resp)
	})
	mux.HandleFunc("POST /reverse", func(w http.ResponseWriter, r *http.Request) {
		var adv iso8583.ReversalAdvice
		json.NewDecoder(r.Body).Decode(&adv)
		o := adv.OriginalData
		a.mu.Lock()
		out, ok := a.legs[adv.MerchantID+"|"+adv.TerminalID+"|"+o.STAN+"|"+o.TransmissionTime]
		a.mu.Unlock()
		if !ok {
			http.Error(w, "unknown original", http.StatusBadRequest)
			return
		}
		ack, err := a.sw.Reverse(iso8583.ReversalAdvice{
			MTI: iso8583.MTIReversalAdvice, PAN: out.PAN, Amount: out.Amount, TransmissionTime: adv.TransmissionTime,
			STAN: fmt.Sprintf("%06d", a.stan.Add(1)), AcquirerID: testAcquirerID, RRN: out.RRN, ResponseCode: adv.ResponseCode,
			OriginalData: &iso8583.OriginalData{MTI: iso8583.MTIAuthRequest, STAN: out.STAN, TransmissionTime: out.TransmissionTime, AcquirerID: testAcquirerID},
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, ack)
	})
	return mux
}

func runDrill(t *testing.T, lab *Lab, id string) Run {
	t.Helper()
	run, err := lab.Start(id)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := lab.Run(run.ID)
		if got.Status == "done" {
			return got
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("drill %s did not finish", id)
	return Run{}
}

func TestEveryDrillBehavesAsDescribed(t *testing.T) {
	terms, err := learn.Terms()
	if err != nil {
		t.Fatal(err)
	}
	w := newWorld(t)
	for _, d := range drills {
		t.Run(d.ID, func(t *testing.T) {
			if _, ok := terms[d.Term]; !ok {
				t.Errorf("drill term %q is not in the glossary", d.Term)
			}
			run := runDrill(t, w.lab, d.ID)
			if run.Probes == 0 {
				t.Error("drill sent no messages")
			}
			for _, r := range run.Results {
				if r.Verdict == VerdictUnexpected || r.Verdict == VerdictFailed {
					t.Errorf("row %d %q: %s, got %s %s (stopped at %s/%s) %s", r.Seq, r.Label, r.Verdict, r.Code, r.Text, r.StoppedAt, r.StopStep, r.Note)
				}
				if r.Term != "" {
					if _, ok := terms[r.Term]; !ok {
						t.Errorf("row %q uses unknown term %q", r.Label, r.Term)
					}
				}
			}
		})
	}
}

// A drill leaves no holds behind: every approval it made is voided and
// reaches the issuer as a release.
func TestDrillsReleaseTheirHolds(t *testing.T) {
	w := newWorld(t)
	for _, id := range []string{"cvv-guessing", "pin-guessing", "token-misuse", "replay", "rogue-sender"} {
		runDrill(t, w.lab, id)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		held := 0
		for _, iss := range w.sw.Issuers {
			var st issuer.State
			if err := w.lab.get(context.Background(), w.lab.cfg.IssuerURLs[iss.ID]+"/api/overview", &st); err != nil {
				t.Fatal(err)
			}
			for _, a := range st.Accounts {
				held += a.Holds
			}
		}
		if held == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d holds still active after the drills", held)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Drills change shared state, so only one runs at a time.
func TestOneDrillAtATime(t *testing.T) {
	w := newWorld(t)
	if _, err := w.lab.Start("issuer-timeout"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.lab.Start("replay"); err != errBusy {
		t.Fatalf("second start: got %v, want errBusy", err)
	}
}

func TestLabelsUseKnownTerms(t *testing.T) {
	bad, err := learn.UnknownTerms(webFS, "web")
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range bad {
		t.Errorf("unknown data-term: %s", b)
	}
}
