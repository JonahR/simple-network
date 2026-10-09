package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JonahR/simple-network/internal/iso8583"
)

// stubAcquirer approves every 0100 and accepts 0400s, after failing the
// first `busy` reversals with 91.
type stubAcquirer struct {
	mu        sync.Mutex
	busy      int
	reversals []iso8583.ReversalAdvice
}

func (a *stubAcquirer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/authorize":
		var req iso8583.AuthRequest
		json.NewDecoder(r.Body).Decode(&req)
		json.NewEncoder(w).Encode(iso8583.AuthResponse{
			MTI: "0110", PAN: req.PAN, Amount: req.Amount, STAN: req.STAN, RRN: req.RRN,
			ResponseCode: "00", AuthCode: "ABC123", NetworkTxnID: "NTX-" + req.STAN, ResponseText: "Approved",
		})
	case "/reverse":
		var adv iso8583.ReversalAdvice
		json.NewDecoder(r.Body).Decode(&adv)
		a.mu.Lock()
		a.reversals = append(a.reversals, adv)
		busy := a.busy > 0
		if busy {
			a.busy--
		}
		a.mu.Unlock()
		if busy {
			http.Error(w, "acquirer busy", http.StatusServiceUnavailable)
			return
		}
		json.NewEncoder(w).Encode(iso8583.ReversalResponse{MTI: "0430", STAN: adv.STAN, ResponseCode: "00", Matched: true})
	}
}

func (a *stubAcquirer) got() []iso8583.ReversalAdvice {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]iso8583.ReversalAdvice(nil), a.reversals...)
}

func newWiredServer(t *testing.T, acq *stubAcquirer) *server {
	t.Helper()
	srv := httptest.NewServer(acq)
	t.Cleanup(srv.Close)
	s := newTestServer()
	s.acquirer = newAcquirerClient(srv.URL)
	s.reversalRetry = 5 * time.Millisecond
	return s
}

func post(t *testing.T, h http.HandlerFunc, body any) (int, Transaction) {
	t.Helper()
	b, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(b))))
	var tx Transaction
	json.NewDecoder(rec.Body).Decode(&tx)
	return rec.Code, tx
}

func TestVoidSendsAnOriginalDataReversal(t *testing.T) {
	acq := &stubAcquirer{}
	s := newWiredServer(t, acq)
	in := sale(creditPAN, iso8583.EntryManual)
	in.CVV = "123"
	_, tx := post(t, s.handleSale, in)
	if tx.Status != "APPROVED" {
		t.Fatalf("sale: %+v", tx)
	}

	code, voided := post(t, s.handleVoid, map[string]string{"stan": tx.Request.STAN})
	if code != 200 || voided.Status != "VOIDED" || voided.Reversal == nil || voided.Reversal.Status != "ACCEPTED" {
		t.Fatalf("void: %d %+v %+v", code, voided, voided.Reversal)
	}
	rr := acq.got()
	if len(rr) != 1 {
		t.Fatalf("acquirer got %d reversals", len(rr))
	}
	got := rr[0]
	if got.MTI != "0420" || got.ResponseCode != "17" || got.PAN != creditPAN || got.TerminalID != "T1" || got.MerchantID != "M1" ||
		got.OriginalData.STAN != tx.Request.STAN || got.OriginalData.TransmissionTime != tx.Request.TransmissionTime ||
		got.NetworkTxnID != "NTX-"+tx.Request.STAN || got.STAN == tx.Request.STAN || got.Amount != 1000 {
		t.Errorf("0420 = %+v", got)
	}
	if voided.Reversal.Advice.PAN != "424242******4242" {
		t.Errorf("the reversal shown to the browser must have a masked PAN, got %q", voided.Reversal.Advice.PAN)
	}

	// Voiding twice is refused.
	if code, _ := post(t, s.handleVoid, map[string]string{"stan": tx.Request.STAN}); code != http.StatusConflict {
		t.Errorf("second void: HTTP %d, want 409", code)
	}
}

func TestLostResponseIsReversedAutomatically(t *testing.T) {
	acq := &stubAcquirer{}
	s := newWiredServer(t, acq)
	in := sale(creditPAN, iso8583.EntryManual)
	in.CVV = "123"
	in.LoseResponse = true
	_, tx := post(t, s.handleSale, in)
	if tx.Status != "NO_RESPONSE" || tx.Reversal == nil || tx.Reversal.Reason != "68" || tx.Reversal.Status != "ACCEPTED" {
		t.Fatalf("lost response: %+v %+v", tx, tx.Reversal)
	}
	if rr := acq.got(); len(rr) != 1 || rr[0].NetworkTxnID != "" {
		t.Errorf("a timeout reversal can't know the network transaction ID: %+v", rr)
	}
}

func TestReversalRetriesUntilAccepted(t *testing.T) {
	acq := &stubAcquirer{busy: 2}
	s := newWiredServer(t, acq)
	in := sale(creditPAN, iso8583.EntryManual)
	in.CVV = "123"
	_, tx := post(t, s.handleSale, in)
	_, first := post(t, s.handleVoid, map[string]string{"stan": tx.Request.STAN})
	if first.Reversal.Status != "PENDING" || first.Status != "APPROVED" {
		t.Fatalf("first attempt should be pending: %+v", first.Reversal)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		done := s.history[0].Reversal.Status == "ACCEPTED"
		s.mu.Unlock()
		if done {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	got := s.snapshot(s.history[0])
	if got.Status != "VOIDED" || got.Reversal.Attempts != 3 {
		t.Errorf("after retries: %s, %+v", got.Status, got.Reversal)
	}
	// Every retry is the same 0400, so the acquirer can recognize repeats.
	rr := acq.got()
	if len(rr) != 3 || rr[0].STAN != rr[2].STAN || rr[0].TransmissionTime != rr[2].TransmissionTime {
		t.Errorf("retries changed the message: %+v", rr)
	}
}

func TestOnlyApprovedSalesCanBeVoided(t *testing.T) {
	s := newWiredServer(t, &stubAcquirer{})
	if code, _ := post(t, s.handleVoid, map[string]string{"stan": "999999"}); code != http.StatusConflict {
		t.Errorf("unknown STAN: HTTP %d", code)
	}
}
