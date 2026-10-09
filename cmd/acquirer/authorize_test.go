package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JonahR/simple-network/internal/iso8583"
)

// fakeNetwork records what it was sent and answers with a fixed code.
// Reversal advices fail with reverseErrs in turn, then get reverseCode
// (default 00).
type fakeNetwork struct {
	code string
	err  error
	got  []iso8583.AuthRequest

	mu          sync.Mutex
	reverseErrs []error
	reverseCode string
	reversals   []iso8583.ReversalAdvice
}

func (f *fakeNetwork) Reverse(_ context.Context, adv iso8583.ReversalAdvice) (iso8583.ReversalResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reversals = append(f.reversals, adv)
	if len(f.reverseErrs) > 0 {
		err := f.reverseErrs[0]
		f.reverseErrs = f.reverseErrs[1:]
		return iso8583.ReversalResponse{}, err
	}
	code := f.reverseCode
	if code == "" {
		code = iso8583.RCApproved
	}
	return iso8583.ReversalResponse{MTI: iso8583.MTIReversalResponse, STAN: adv.STAN, ResponseCode: code, Matched: true, NetworkTxnID: "NTX1"}, nil
}

func (f *fakeNetwork) sentReversals() []iso8583.ReversalAdvice {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]iso8583.ReversalAdvice(nil), f.reversals...)
}

func (f *fakeNetwork) Authorize(_ context.Context, req iso8583.AuthRequest) (iso8583.AuthResponse, error) {
	f.got = append(f.got, req)
	if f.err != nil {
		return iso8583.AuthResponse{}, f.err
	}
	resp := reply(req, f.code)
	resp.Amount = req.Amount
	resp.NetworkTxnID = "NTX1"
	if iso8583.IsApproved(f.code) {
		resp.AuthCode = "123456"
	}
	return resp, nil
}

var shop = Merchant{ID: "M1", Name: "Shop", MCC: "5814", Terminals: []string{"T1"}, DiscountBPS: 250}

// testNow is 2026-10-09 14:30 UTC, day 282 of the year.
var testNow = time.Date(2026, 10, 9, 14, 30, 0, 0, time.UTC)

func newTestAcquirer(net Network) *acquirer {
	a := newAcquirer("100001", "Test Bank", []Merchant{shop}, net)
	a.stan.Store(0) // Network-leg STANs start at 000001
	a.now = func() time.Time { return testNow }
	a.reversalRetry, a.reversalRetryMax = time.Millisecond, 4*time.Millisecond
	return a
}

// waitFor polls until cond holds, failing the test after a few seconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !cond(); {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func request() iso8583.AuthRequest {
	return iso8583.AuthRequest{
		MTI: iso8583.MTIAuthRequest, PAN: "4242424242424242", ProcessingCode: "003000", Amount: 1000,
		TransmissionTime: "1009143000", STAN: "000042", Expiry: "3312", MCC: "5999", RRN: "628014000042",
		TerminalID: "T1", MerchantID: "M1", Currency: "840", PINData: "0123456789ABCDEF", CVV2: "123",
	}
}

func TestAuthorizeForwardsAndRestoresSTAN(t *testing.T) {
	net := &fakeNetwork{code: iso8583.RCApproved}
	a := newTestAcquirer(net)

	resp := a.Authorize(context.Background(), request())

	if len(net.got) != 1 {
		t.Fatalf("network called %d times, want 1", len(net.got))
	}
	sent := net.got[0]
	if sent.AcquirerID != "100001" {
		t.Errorf("DE32 = %q, want 100001", sent.AcquirerID)
	}
	if sent.STAN == "000042" || sent.STAN != "000001" {
		t.Errorf("network-leg STAN = %q, want the acquirer's own 000001", sent.STAN)
	}
	if sent.RRN != "628214000001" {
		t.Errorf("DE37 = %q, want 628214000001 built from the acquirer's STAN", sent.RRN)
	}
	if sent.MCC != "5814" {
		t.Errorf("DE18 = %q, want the merchant agreement's 5814", sent.MCC)
	}
	if sent.PINData != "0123456789ABCDEF" || sent.PAN != "4242424242424242" {
		t.Error("PAN and PIN block must reach the network unchanged")
	}
	if resp.STAN != "000042" || resp.ResponseCode != iso8583.RCApproved || resp.AuthCode != "123456" {
		t.Errorf("response = %+v, want approved with the terminal's STAN 000042", resp)
	}

	rec := a.Records()[0]
	if rec.Status != "APPROVED" || rec.MerchantName != "Shop" {
		t.Errorf("record = %+v", rec)
	}
	if rec.Request.PAN != "424242******4242" || rec.Request.CVV2 != "" || rec.Request.PINData != "ENCRYPTED" {
		t.Errorf("stored request is not redacted: %+v", rec.Request)
	}
	if rec.Response.PAN == "4242424242424242" {
		t.Error("stored response holds the full PAN")
	}
	var fields []string
	for _, c := range rec.Changes {
		fields = append(fields, c.Field)
	}
	if got := strings.Join(fields, ","); got != "DE11,DE32,DE37,DE18,DE11" {
		t.Errorf("changes = %s", got)
	}
	if n := len(rec.Steps); n != 4 {
		t.Errorf("got %d steps, want POS→acq, acq→net, net→acq, acq→POS", n)
	}
}

func TestAuthorizeRejectsUnknownTerminal(t *testing.T) {
	for name, mutate := range map[string]func(*iso8583.AuthRequest){
		"unknown merchant":           func(r *iso8583.AuthRequest) { r.MerchantID = "M9" },
		"terminal of other merchant": func(r *iso8583.AuthRequest) { r.TerminalID = "T9" },
	} {
		t.Run(name, func(t *testing.T) {
			net := &fakeNetwork{code: iso8583.RCApproved}
			req := request()
			mutate(&req)
			resp := newTestAcquirer(net).Authorize(context.Background(), req)
			if resp.ResponseCode != iso8583.RCInvalidMerchant || resp.Amount != req.Amount {
				t.Errorf("code = %s, DE4 = %d; want 03 echoing DE4 %d", resp.ResponseCode, resp.Amount, req.Amount)
			}
			if len(net.got) != 0 {
				t.Error("request reached the network")
			}
		})
	}
}

func TestAuthorizeRetryGetsSameAnswer(t *testing.T) {
	net := &fakeNetwork{code: iso8583.RCApproved}
	a := newTestAcquirer(net)
	first := a.Authorize(context.Background(), request())
	second := a.Authorize(context.Background(), request())
	if len(net.got) != 1 {
		t.Errorf("network called %d times for a retry, want 1", len(net.got))
	}
	if first != second {
		t.Errorf("retry got %+v, want %+v", second, first)
	}
}

func TestAuthorizeNetworkDownSendsReversal(t *testing.T) {
	for name, tc := range map[string]struct {
		err    error
		reason string
		title  string
	}{
		"refused": {errors.New("network unreachable"), iso8583.RCSuspectedMalfunc, "Network call failed"},
		"timeout": {&netError{"network did not answer", context.DeadlineExceeded}, iso8583.RCLateResponse, "No answer from the network"},
	} {
		t.Run(name, func(t *testing.T) {
			net := &fakeNetwork{err: tc.err}
			a := newTestAcquirer(net)
			defer a.Close()
			req := request()
			resp := a.Authorize(context.Background(), req)
			if resp.ResponseCode != iso8583.RCIssuerUnavailable || resp.STAN != "000042" || resp.Amount != req.Amount {
				t.Errorf("response = %+v, want 91 with the terminal's STAN and DE4", resp)
			}

			waitFor(t, "the 0430", func() bool { return a.Records()[0].Reversal == ReversalAcknowledged })
			advs := net.sentReversals()
			if len(advs) != 1 {
				t.Fatalf("sent %d reversals, want 1", len(advs))
			}
			leg := net.got[0]
			adv := advs[0]
			wantOrig := iso8583.OriginalData{MTI: "0100", STAN: leg.STAN, TransmissionTime: leg.TransmissionTime, AcquirerID: "100001"}
			if adv.MTI != "0420" || adv.PAN != req.PAN || adv.Amount != req.Amount || adv.RRN != leg.RRN ||
				adv.AcquirerID != "100001" || adv.ResponseCode != tc.reason || adv.OriginalData == nil || *adv.OriginalData != wantOrig {
				t.Errorf("0420 = %+v (DE90 %+v), want reason %s for %+v", adv, adv.OriginalData, tc.reason, wantOrig)
			}
			if adv.STAN == leg.STAN || len(adv.STAN) != 6 {
				t.Errorf("0420 STAN %q must be its own, not the 0100's %q", adv.STAN, leg.STAN)
			}
			if adv.TransmissionTime != "1009143000" {
				t.Errorf("0420 DE7 = %q, want its own send time", adv.TransmissionTime)
			}

			rec := a.Records()[0]
			if rec.Status != "DECLINED" {
				t.Error("record should be declined")
			}
			var path []string
			for _, s := range rec.Steps {
				path = append(path, s.From+">"+s.To+":"+s.MTI)
				if s.From == PartyNetwork && s.MTI == iso8583.MTIAuthResponse {
					t.Error("drew an 0110 from the network that never arrived")
				}
			}
			want := "pos>acquirer:0100,acquirer>network:0100,acquirer>acquirer:,acquirer>pos:0110,acquirer>network:0420,network>acquirer:0430"
			if got := strings.Join(path, ","); got != want {
				t.Errorf("steps = %s\nwant    %s", got, want)
			}
			if rec.Steps[2].Title != tc.title {
				t.Errorf("timeout step title = %q, want %q", rec.Steps[2].Title, tc.title)
			}
			for _, c := range rec.Changes {
				if c.Field == "DE11" && c.Name == "STAN (in the 0110)" {
					t.Error("recorded restoring the STAN of an 0110 the network never sent")
				}
			}

			// A retry of the same terminal request gets the same 91 and no second reversal.
			if again := a.Authorize(context.Background(), req); again != resp {
				t.Errorf("retry got %+v, want %+v", again, resp)
			}
			if len(net.got) != 1 || len(net.sentReversals()) != 1 {
				t.Errorf("retry reached the network: %d 0100s, %d 0420s", len(net.got), len(net.sentReversals()))
			}
		})
	}
}

func TestReversalRetriesUntilAcknowledged(t *testing.T) {
	net := &fakeNetwork{err: errors.New("network unreachable"), reverseErrs: []error{
		errors.New("network unreachable"), errors.New("network returned HTTP 503"),
	}}
	a := newTestAcquirer(net)
	defer a.Close()
	a.Authorize(context.Background(), request())
	waitFor(t, "the 0430", func() bool { return a.Records()[0].Reversal == ReversalAcknowledged })

	advs := net.sentReversals()
	if len(advs) != 3 {
		t.Fatalf("sent %d 0420s, want 3", len(advs))
	}
	if advs[0] != advs[1] || advs[1] != advs[2] {
		t.Errorf("each attempt must resend the same advice: %+v", advs)
	}
	// One 0420 step and the 0430; the failed attempts leave no trace once acknowledged.
	steps := a.Records()[0].Steps
	if n := len(steps); n != 6 || steps[4].MTI != "0420" || steps[5].MTI != "0430" {
		t.Errorf("steps = %+v", steps)
	}
}

func TestReversalRetriesNon00Response(t *testing.T) {
	net := &fakeNetwork{err: errors.New("down"), reverseCode: "96"}
	a := newTestAcquirer(net)
	a.Authorize(context.Background(), request())
	waitFor(t, "a second attempt", func() bool { return len(net.sentReversals()) >= 2 })
	a.Close()
	rec := a.Records()[0]
	if rec.Reversal != ReversalStopped {
		t.Errorf("reversal = %q, want stopped", rec.Reversal)
	}
	last := rec.Steps[len(rec.Steps)-1]
	if last.Title != "Reversal retries stopped" {
		t.Errorf("last step = %+v", last)
	}
	// The retry note is rewritten, not repeated.
	notes := 0
	for _, s := range rec.Steps {
		if s.Title == "No 0430 yet" {
			notes++
		}
	}
	if notes > 1 {
		t.Errorf("%d retry notes, want at most 1", notes)
	}
}

func TestReversalStopsWhenRejected(t *testing.T) {
	net := &fakeNetwork{err: errors.New("down"), reverseErrs: []error{
		&rejectedError{errors.New("network returned HTTP 400: invalid 0420 message")},
	}}
	a := newTestAcquirer(net)
	defer a.Close()
	a.Authorize(context.Background(), request())
	waitFor(t, "the rejection", func() bool { return a.Records()[0].Reversal == ReversalRejected })
	if n := len(net.sentReversals()); n != 1 {
		t.Errorf("sent %d 0420s, want 1: a refused advice must not be resent", n)
	}
	steps := a.Records()[0].Steps
	if last := steps[len(steps)-1]; last.Title != "Reversal rejected" {
		t.Errorf("last step = %+v", last)
	}
}

func TestCloseStopsReversals(t *testing.T) {
	net := &fakeNetwork{err: errors.New("down")}
	a := newTestAcquirer(net)
	a.Close()
	// After Close no reversal starts; the record says so.
	a.Authorize(context.Background(), request())
	if got := a.Records()[0].Reversal; got != ReversalStopped {
		t.Errorf("reversal = %q, want stopped", got)
	}
	if n := len(net.sentReversals()); n != 0 {
		t.Errorf("sent %d reversals after Close", n)
	}
}

func TestRecordsAreCapped(t *testing.T) {
	a := newTestAcquirer(&fakeNetwork{code: iso8583.RCApproved})
	req := request()
	req.MerchantID = "M9" // Declined at the acquirer: fast and network-free
	for i := range maxRecords + 5 {
		req.STAN = fmt.Sprintf("%06d", i+1)
		a.Authorize(context.Background(), req)
	}
	recs := a.Records()
	if len(recs) != maxRecords {
		t.Fatalf("kept %d records, want %d", len(recs), maxRecords)
	}
	if recs[0].TerminalSTAN != fmt.Sprintf("%06d", maxRecords+5) || recs[maxRecords-1].TerminalSTAN != "000006" {
		t.Errorf("kept STANs %s..%s, want the newest", recs[maxRecords-1].TerminalSTAN, recs[0].TerminalSTAN)
	}
}

func TestDuplicateCacheExpires(t *testing.T) {
	net := &fakeNetwork{code: iso8583.RCApproved}
	a := newTestAcquirer(net)
	now := testNow
	a.now = func() time.Time { return now }
	a.Authorize(context.Background(), request())

	now = now.Add(seenTTL - time.Minute)
	a.Authorize(context.Background(), request())
	if len(net.got) != 1 {
		t.Fatalf("a retry within %s reached the network", seenTTL)
	}

	now = now.Add(2 * time.Minute)
	other := request()
	other.STAN = "000043"
	a.Authorize(context.Background(), other) // Prunes the expired entry
	a.mu.Lock()
	n := len(a.seen)
	a.mu.Unlock()
	if n != 1 {
		t.Errorf("duplicate cache holds %d entries, want only the fresh one", n)
	}
	a.Authorize(context.Background(), request())
	if len(net.got) != 3 {
		t.Errorf("network called %d times, want 3: an expired key is a new request", len(net.got))
	}
}

func TestOverviewTotals(t *testing.T) {
	net := &fakeNetwork{code: iso8583.RCApproved}
	s := &server{acq: newTestAcquirer(net)}
	sale := request()
	sale.Amount = 10_000
	s.acq.Authorize(context.Background(), sale)
	refund := request()
	refund.STAN, refund.ProcessingCode, refund.Amount = "000043", "203000", 2_000
	s.acq.Authorize(context.Background(), refund)
	net.code = iso8583.RCInsufficientFunds
	declined := request()
	declined.STAN = "000044"
	s.acq.Authorize(context.Background(), declined)

	rec := httptest.NewRecorder()
	s.handleOverview(rec, httptest.NewRequest(http.MethodGet, "/api/overview", nil))
	var out struct {
		Stats     stats          `json:"stats"`
		Merchants []merchantView `json:"merchants"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Stats.Count != 3 || out.Stats.Approved != 2 {
		t.Errorf("stats = %+v", out.Stats)
	}
	got := out.Merchants[0].Totals
	want := Totals{Currency: "840", Approved: 2, Declined: 1, Sales: 10_000, Refunds: 2_000, Fees: 250, Net: 7_750}
	if len(got) != 1 || got[0] != want {
		t.Errorf("totals = %+v, want %+v", got, want)
	}
}

func TestHandleAuthorizeRejectsWrongMTI(t *testing.T) {
	s := &server{acq: newTestAcquirer(&fakeNetwork{code: iso8583.RCApproved})}
	rec := httptest.NewRecorder()
	s.handleAuthorize(rec, httptest.NewRequest(http.MethodPost, "/authorize", strings.NewReader(`{"mti":"0420"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestHTTPNetworkReverse(t *testing.T) {
	var got iso8583.ReversalAdvice
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/reverse" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil || got.OriginalData == nil {
			http.Error(w, "invalid 0420 message", http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(iso8583.ReversalResponse{MTI: "0430", STAN: got.STAN, ResponseCode: "00", Matched: true, NetworkTxnID: "NTX9"})
	}))
	defer srv.Close()
	n := newHTTPNetwork(srv.URL)

	adv := iso8583.ReversalAdvice{MTI: "0420", STAN: "000777", ResponseCode: "68",
		OriginalData: &iso8583.OriginalData{MTI: "0100", STAN: "000001", TransmissionTime: "1009143000", AcquirerID: "100001"}}
	ack, err := n.Reverse(context.Background(), adv)
	if err != nil || ack.ResponseCode != "00" || ack.STAN != "000777" || !ack.Matched || ack.NetworkTxnID != "NTX9" {
		t.Errorf("ack = %+v, err = %v", ack, err)
	}
	if got.STAN != "000777" || got.OriginalData.STAN != "000001" {
		t.Errorf("network got %+v", got)
	}

	adv.OriginalData = nil
	if _, err := n.Reverse(context.Background(), adv); err == nil || !strings.Contains(err.Error(), "HTTP 400: invalid 0420 message") {
		t.Errorf("malformed advice: err = %v", err)
	}
}

func TestHTTPNetworkTimeoutIsTimeout(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(block)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := newHTTPNetwork(srv.URL).Authorize(ctx, request())
	if err == nil || !isTimeout(err) {
		t.Errorf("err = %v, want a timeout", err)
	}

	refused := newHTTPNetwork("http://127.0.0.1:1")
	if _, err := refused.Authorize(context.Background(), request()); err == nil || isTimeout(err) {
		t.Errorf("refused connection: err = %v, want a non-timeout error", err)
	}
}
