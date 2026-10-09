package network

import (
	"context"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JonahR/simple-network/internal/demokeys"
	"github.com/JonahR/simple-network/internal/iso8583"
	"github.com/JonahR/simple-network/internal/issuer"
	"github.com/JonahR/simple-network/internal/pin"
)

// bankClient calls an issuer.Bank in process, optionally slowly or not at all.
type bankClient struct {
	bank  *issuer.Bank
	delay time.Duration
	down  bool
	calls atomic.Int32

	reverseFails atomic.Int32 // Fail this many 0420s before acknowledging
	mu           sync.Mutex
	advices      []iso8583.ReversalAdvice // Every 0420 received, including failed attempts
}

func (c *bankClient) received() []iso8583.ReversalAdvice {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.advices)
}

func (c *bankClient) Authorize(ctx context.Context, req iso8583.AuthRequest) (iso8583.AuthResponse, error) {
	c.calls.Add(1)
	if c.down {
		return iso8583.AuthResponse{}, ErrIssuerUnavailable
	}
	start := time.Now()
	select {
	case <-time.After(c.delay):
	case <-ctx.Done():
		// Like a real slow issuer, finish the authorization anyway.
		go func() { time.Sleep(c.delay - time.Since(start)); c.bank.Authorize(req) }()
		return iso8583.AuthResponse{}, ErrIssuerTimeout
	}
	return c.bank.Authorize(req), nil
}

func (c *bankClient) Reverse(_ context.Context, adv iso8583.ReversalAdvice) (iso8583.ReversalResponse, error) {
	c.mu.Lock()
	c.advices = append(c.advices, adv)
	c.mu.Unlock()
	if c.reverseFails.Add(-1) >= 0 {
		return iso8583.ReversalResponse{}, ErrIssuerUnavailable
	}
	return c.bank.Reverse(adv), nil
}

func key(t *testing.T, h string) []byte {
	t.Helper()
	k, err := hex.DecodeString(h)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

type fixture struct {
	sw       *Switch
	fsb, ucb *bankClient
	acqKey   []byte
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	fsbKey, ucbKey, acqKey := key(t, demokeys.IssuerFSB), key(t, demokeys.IssuerUCB), key(t, demokeys.AcquirerPIN)
	fa, fe := issuer.FirstSimpleBankAccounts()
	ua, ue := issuer.UnionCardBankAccounts()
	fsb := &bankClient{bank: issuer.NewBank("FSB", "First Simple Bank", fsbKey, fa, fe)}
	ucb := &bankClient{bank: issuer.NewBank("UCB", "Union Card Bank", ucbKey, ua, ue)}
	sw := &Switch{
		BINs:  DefaultBINs(),
		Vault: DefaultVault(),
		Issuers: map[string]*Issuer{
			"FSB": {ID: "FSB", Name: "First Simple Bank", Client: fsb, PINKey: fsbKey, Breaker: NewBreaker(3, time.Minute, time.Now)},
			"UCB": {ID: "UCB", Name: "Union Card Bank", Client: ucb, PINKey: ucbKey, Breaker: NewBreaker(3, time.Minute, time.Now)},
		},
		AcquirerKeys:     map[string][]byte{"100001": acqKey},
		IssuerTimeout:    50 * time.Millisecond,
		ReversalRetry:    time.Millisecond,
		ReversalRetryMax: 4 * time.Millisecond,
		Recorder:         NewRecorder(100),
		Now:              time.Now,
	}
	t.Cleanup(sw.Close)
	return &fixture{sw: sw, fsb: fsb, ucb: ucb, acqKey: acqKey}
}

var stan atomic.Int32

func auth(pan, expiry string, amount int64) iso8583.AuthRequest {
	s := fmt.Sprintf("%06d", stan.Add(1))
	return iso8583.AuthRequest{
		MTI: "0100", PAN: pan, ProcessingCode: "000000", Amount: amount,
		TransmissionTime: "1009143000", STAN: s, Expiry: expiry, EntryMode: iso8583.EntryManual,
		AcquirerID: "100001", RRN: "628214" + s, TerminalID: "T1", MerchantID: "M1", Currency: "840",
	}
}

func TestRoutesByBIN(t *testing.T) {
	f := newFixture(t)
	resp := f.sw.Authorize(context.Background(), auth("4242424242424242", "3312", 1000))
	if resp.ResponseCode != "00" || resp.MTI != "0110" || resp.NetworkTxnID == "" || resp.AuthCode == "" {
		t.Fatalf("%+v", resp)
	}
	rec, _ := f.sw.Recorder.Get(resp.NetworkTxnID)
	if rec.IssuerID != "FSB" || rec.Status != "approved" || f.ucb.calls.Load() != 0 {
		t.Errorf("record %+v", rec)
	}
	names := []string{}
	for _, s := range rec.Steps {
		names = append(names, s.Name)
	}
	if got := strings.Join(names, ","); got != "validate,route,send,issuer,respond" {
		t.Errorf("steps = %s", got)
	}
	if rec.Request.PAN != "424242******4242" {
		t.Errorf("record holds unmasked PAN %q", rec.Request.PAN)
	}

	if resp := f.sw.Authorize(context.Background(), auth("5555555555554444", "3208", 1000)); resp.ResponseCode != "00" || f.ucb.calls.Load() != 1 {
		t.Errorf("UCB route: %+v", resp)
	}
}

func TestNetworkDeclines(t *testing.T) {
	f := newFixture(t)
	bad := auth("4242424242424242", "3312", 1000)
	bad.STAN = "12"
	if resp := f.sw.Authorize(context.Background(), bad); resp.ResponseCode != "30" {
		t.Errorf("format error: %s", resp.ResponseCode)
	}
	// Valid Luhn, no BIN entry.
	if resp := f.sw.Authorize(context.Background(), auth("6011000990139424", "3312", 1000)); resp.ResponseCode != "15" {
		t.Errorf("no route: %s", resp.ResponseCode)
	}
	if f.fsb.calls.Load()+f.ucb.calls.Load() != 0 {
		t.Error("issuer called for a network decline")
	}
}

func wallet(token, expiry, provider string) iso8583.AuthRequest {
	r := auth(token, expiry, 700)
	r.EntryMode, r.WalletProvider, r.ARQC = iso8583.EntryContactless, provider, "A1B2C3D4E5F60718"
	return r
}

func TestDetokenizes(t *testing.T) {
	f := newFixture(t)
	resp := f.sw.Authorize(context.Background(), wallet("4895373708421368", "3411", "google_pay"))
	if resp.ResponseCode != "00" {
		t.Fatalf("%+v", resp)
	}
	if resp.PAN != "4895373708421368" {
		t.Errorf("acquirer got back %q, want the token", resp.PAN)
	}
	rec, _ := f.sw.Recorder.Get(resp.NetworkTxnID)
	if rec.IssuerRequest.PAN != "424242******4242" || rec.IssuerRequest.Expiry != "3312" || rec.IssuerRequest.Token != "489537******1368" {
		t.Errorf("issuer request %+v", rec.IssuerRequest)
	}
	if a, _ := f.fsb.bank.Snapshot("4242424242424242"); a.Available != 500_000-700 {
		t.Errorf("hold not on the card account: %d", a.Available)
	}

	tests := []struct {
		name string
		req  func() iso8583.AuthRequest
		want string
	}{
		{"wrong wallet", func() iso8583.AuthRequest { return wallet("4895373708421368", "3411", "apple_pay") }, "05"},
		{"keyed token", func() iso8583.AuthRequest {
			r := wallet("4895373708421368", "3411", "google_pay")
			r.EntryMode = iso8583.EntryManual
			return r
		}, "05"},
		{"token expiry mismatch", func() iso8583.AuthRequest { return wallet("4895373708421368", "3312", "google_pay") }, "54"},
		{"unknown token", func() iso8583.AuthRequest { return wallet("4895370000000007", "3411", "google_pay") }, "14"},
	}
	for _, tt := range tests {
		if got := f.sw.Authorize(context.Background(), tt.req()); got.ResponseCode != tt.want {
			t.Errorf("%s: %s, want %s", tt.name, got.ResponseCode, tt.want)
		}
	}

	f.sw.Vault.SetActive("4895373708421368", false)
	if got := f.sw.Authorize(context.Background(), wallet("4895373708421368", "3411", "google_pay")); got.ResponseCode != "62" {
		t.Errorf("suspended token: %s", got.ResponseCode)
	}
}

func TestTranslatesPIN(t *testing.T) {
	f := newFixture(t)
	for _, tt := range []struct{ pin, want string }{{"1234", "00"}, {"4321", "55"}} {
		r := auth("4000056655665556", "3305", 2000)
		r.EntryMode, r.ARQC = iso8583.EntryChip, "A1B2C3D4E5F60718"
		r.PINData, _ = pin.Encrypt(tt.pin, r.PAN, f.acqKey)
		resp := f.sw.Authorize(context.Background(), r)
		if resp.ResponseCode != tt.want {
			t.Errorf("PIN %s: %s %s", tt.pin, resp.ResponseCode, resp.ResponseText)
		}
		rec, _ := f.sw.Recorder.Get(resp.NetworkTxnID)
		if rec.IssuerRequest.PINData != "ENCRYPTED" {
			t.Errorf("issuer request PIN data not redacted: %q", rec.IssuerRequest.PINData)
		}
	}
}

func TestTimeoutReversesAndBreakerOpens(t *testing.T) {
	f := newFixture(t)
	f.fsb.delay = 200 * time.Millisecond

	resp := f.sw.Authorize(context.Background(), auth("4242424242424242", "3312", 5000))
	if resp.ResponseCode != "91" {
		t.Fatalf("timeout: %+v", resp)
	}
	// The issuer approves late; the 0420 must leave the account untouched.
	time.Sleep(400 * time.Millisecond)
	if a, _ := f.fsb.bank.Snapshot("4242424242424242"); a.Available != 500_000 {
		t.Errorf("orphan hold after timeout: available %d", a.Available)
	}
	rec, _ := f.sw.Recorder.Get(resp.NetworkTxnID)
	if last := rec.Steps[len(rec.Steps)-1]; last.Name != "reversal" || !last.OK {
		t.Errorf("last step %+v", last)
	}
	// The 0420 has its own STAN and DE7; the original is in DE90.
	advs := f.fsb.received()
	if len(advs) != 1 {
		t.Fatalf("%d 0420s sent", len(advs))
	}
	adv, orig := advs[0], rec.Request
	if adv.MTI != "0420" || adv.ResponseCode != "68" || adv.NetworkTxnID != resp.NetworkTxnID || adv.Amount != 5000 ||
		len(adv.STAN) != 6 || adv.STAN == orig.STAN || len(adv.TransmissionTime) != 10 {
		t.Errorf("0420 %+v", adv)
	}
	if d := adv.OriginalData; d == nil || *d != (iso8583.OriginalData{MTI: "0100", STAN: orig.STAN, TransmissionTime: orig.TransmissionTime, AcquirerID: "100001"}) {
		t.Errorf("DE90 %+v", d)
	}

	// Two more failures open the breaker; the fourth request is never sent.
	f.fsb.delay = 0
	f.fsb.down = true
	f.sw.Authorize(context.Background(), auth("4242424242424242", "3312", 100))
	f.sw.Authorize(context.Background(), auth("4242424242424242", "3312", 100))
	if f.sw.Issuers["FSB"].Breaker.State() != BreakerOpen {
		t.Fatal("breaker did not open")
	}
	calls := f.fsb.calls.Load()
	if resp := f.sw.Authorize(context.Background(), auth("4242424242424242", "3312", 100)); resp.ResponseCode != "91" || f.fsb.calls.Load() != calls {
		t.Errorf("open breaker still sent the request: %s", resp.ResponseCode)
	}
	// The other issuer is unaffected.
	if resp := f.sw.Authorize(context.Background(), auth("5555555555554444", "3208", 100)); resp.ResponseCode != "00" {
		t.Errorf("UCB affected by FSB outage: %s", resp.ResponseCode)
	}
}

func TestDuplicateRequestIsIdempotent(t *testing.T) {
	f := newFixture(t)
	r := auth("4242424242424242", "3312", 1000)
	first := f.sw.Authorize(context.Background(), r)
	second := f.sw.Authorize(context.Background(), r)
	if first != second {
		t.Errorf("responses differ:\n%+v\n%+v", first, second)
	}
	if f.fsb.calls.Load() != 1 {
		t.Errorf("issuer called %d times", f.fsb.calls.Load())
	}
	if a, _ := f.fsb.bank.Snapshot("4242424242424242"); a.Available != 500_000-1000 {
		t.Errorf("duplicate hold: available %d", a.Available)
	}
	rec, _ := f.sw.Recorder.Get(first.NetworkTxnID)
	if rec.Duplicates != 1 {
		t.Errorf("duplicates = %d", rec.Duplicates)
	}
}

func TestBinTableLongestPrefix(t *testing.T) {
	tbl := NewBinTable("t", []BinEntry{{Prefix: "424242", IssuerID: "A"}, {Prefix: "42424299", IssuerID: "B"}})
	if e, _ := tbl.Lookup("4242429912345678"); e.IssuerID != "B" {
		t.Errorf("8-digit override not used: %s", e.IssuerID)
	}
	if e, _ := tbl.Lookup("4242421234567890"); e.IssuerID != "A" {
		t.Errorf("6-digit entry: %s", e.IssuerID)
	}
}

func TestBreakerHalfOpen(t *testing.T) {
	now := time.Unix(0, 0)
	b := NewBreaker(2, time.Second, func() time.Time { return now })
	b.Failure()
	if b.Failure() != true || b.Allow() {
		t.Fatal("breaker should open after 2 failures")
	}
	now = now.Add(time.Second)
	if !b.Allow() || b.Allow() {
		t.Fatal("half-open should allow exactly one trial")
	}
	b.Success()
	if b.State() != BreakerClosed || !b.Allow() {
		t.Fatal("success should close the breaker")
	}
}

func TestStats(t *testing.T) {
	f := newFixture(t)
	f.sw.Authorize(context.Background(), auth("4242424242424242", "3312", 1000))
	f.sw.Authorize(context.Background(), auth("5555555555554444", "3208", 99_999))
	s := f.sw.Recorder.Stats()
	if s.Total != 2 || s.Approved != 1 || s.Declined != 1 || s.ApprovalRate != 0.5 || s.ByCode["51"] != 1 || s.ByIssuer["UCB"].Total != 1 {
		t.Errorf("%+v", s)
	}
}

func TestTxnIDsSortByTime(t *testing.T) {
	a := NewTxnID(time.UnixMilli(1_000))
	b := NewTxnID(time.UnixMilli(2_000))
	if !(a < b) || len(a) != 36 || a[14] != '7' {
		t.Errorf("%s, %s", a, b)
	}
}

func TestNewRecordEncodesEmptySteps(t *testing.T) {
	f := newFixture(t)
	events, stop := f.sw.Recorder.Subscribe()
	defer stop()
	f.sw.Authorize(context.Background(), auth("4242424242424242", "3312", 100))
	first := <-events
	if first.Record.Steps == nil {
		t.Error("first event has nil steps; the dashboard expects a list")
	}
}

// waitFor polls until cond holds or a second passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func reversalSteps(sw *Switch, id string) []Step {
	rec, _ := sw.Recorder.Get(id)
	var out []Step
	for _, st := range rec.Steps {
		if st.Name == "reversal" {
			out = append(out, st)
		}
	}
	return out
}

func TestReversalRetriesUntilAcknowledged(t *testing.T) {
	f := newFixture(t)
	f.fsb.delay = 100 * time.Millisecond
	f.fsb.reverseFails.Store(7) // More than the old limit of 5 attempts

	resp := f.sw.Authorize(context.Background(), auth("4242424242424242", "3312", 5000))
	if resp.ResponseCode != "91" {
		t.Fatalf("timeout: %+v", resp)
	}
	waitFor(t, "0420 acknowledged", func() bool {
		steps := reversalSteps(f.sw, resp.NetworkTxnID)
		return len(steps) > 0 && steps[len(steps)-1].OK
	})
	steps := reversalSteps(f.sw, resp.NetworkTxnID)
	if len(steps) != 8 {
		t.Errorf("%d reversal steps, want 7 failures and an acknowledgement", len(steps))
	}
	if !strings.Contains(steps[6].Detail, "retrying in 4ms") {
		t.Errorf("backoff not capped: %s", steps[6].Detail)
	}
	// Every resend is the same advice: same STAN, so the issuer can match repeats.
	advs := f.fsb.received()
	for _, a := range advs {
		if a.STAN != advs[0].STAN {
			t.Errorf("resend changed STAN: %s, %s", advs[0].STAN, a.STAN)
		}
	}
	time.Sleep(150 * time.Millisecond) // Let the issuer finish the late authorization
	if a, _ := f.fsb.bank.Snapshot("4242424242424242"); a.Available != 500_000 {
		t.Errorf("orphan hold: available %d", a.Available)
	}
}

func TestCloseStopsReversalRetries(t *testing.T) {
	f := newFixture(t)
	f.fsb.delay = 100 * time.Millisecond
	f.fsb.reverseFails.Store(1 << 30) // Never acknowledges
	resp := f.sw.Authorize(context.Background(), auth("4242424242424242", "3312", 5000))
	waitFor(t, "a few 0420 attempts", func() bool { return len(f.fsb.received()) >= 3 })
	f.sw.Close()
	n := len(f.fsb.received())
	time.Sleep(20 * time.Millisecond)
	if len(f.fsb.received()) != n {
		t.Error("0420s still sent after Close")
	}
	steps := reversalSteps(f.sw, resp.NetworkTxnID)
	if last := steps[len(steps)-1]; !strings.Contains(last.Detail, "shutting down") {
		t.Errorf("last step %+v", last)
	}
}

func TestDeclineEchoesAmount(t *testing.T) {
	f := newFixture(t)
	// Network decline (no BIN) and issuer decline (over the limit).
	if resp := f.sw.Authorize(context.Background(), auth("6011000990139424", "3312", 1234)); resp.ResponseCode != "15" || resp.Amount != 1234 {
		t.Errorf("network decline: %s DE4=%d", resp.ResponseCode, resp.Amount)
	}
	if resp := f.sw.Authorize(context.Background(), auth("5555555555554444", "3208", 99_999)); resp.ResponseCode != "51" || resp.Amount != 99_999 {
		t.Errorf("issuer decline: %s DE4=%d", resp.ResponseCode, resp.Amount)
	}
}

func TestValidatesAcquirerID(t *testing.T) {
	f := newFixture(t)
	for _, id := range []string{"", "ACQ1", "123456789012"} {
		r := auth("4242424242424242", "3312", 100)
		r.AcquirerID = id
		if resp := f.sw.Authorize(context.Background(), r); resp.ResponseCode != "30" {
			t.Errorf("DE32 %q: %s", id, resp.ResponseCode)
		}
	}
	r := auth("4242424242424242", "3312", 100)
	r.AcquirerID = "1"
	if resp := f.sw.Authorize(context.Background(), r); resp.ResponseCode == "30" {
		t.Error("1-digit DE32 rejected")
	}
}

// acquirerReversal builds the 0420 an acquirer sends for req.
func acquirerReversal(req iso8583.AuthRequest) iso8583.ReversalAdvice {
	return iso8583.ReversalAdvice{
		MTI: "0420", PAN: req.PAN, Amount: req.Amount, TransmissionTime: "1009143005",
		STAN: fmt.Sprintf("%06d", stan.Add(1)), AcquirerID: req.AcquirerID, RRN: req.RRN, ResponseCode: "68",
		OriginalData: &iso8583.OriginalData{MTI: "0100", STAN: req.STAN, TransmissionTime: req.TransmissionTime, AcquirerID: req.AcquirerID},
	}
}

func TestAcquirerReversalOfApproval(t *testing.T) {
	f := newFixture(t)
	req := auth("4242424242424242", "3312", 2500)
	resp := f.sw.Authorize(context.Background(), req)
	if resp.ResponseCode != "00" {
		t.Fatalf("%+v", resp)
	}
	adv := acquirerReversal(req)
	ack, err := f.sw.Reverse(adv)
	if err != nil {
		t.Fatal(err)
	}
	if ack.MTI != "0430" || ack.STAN != adv.STAN || ack.ResponseCode != "00" || !ack.Matched || ack.NetworkTxnID != resp.NetworkTxnID {
		t.Errorf("0430 %+v", ack)
	}
	waitFor(t, "hold released", func() bool {
		a, _ := f.fsb.bank.Snapshot("4242424242424242")
		return a.Available == 500_000
	})

	// The network's 0420 to the issuer has its own STAN; DE90 is the original 0100.
	advs := f.fsb.received()
	if len(advs) != 1 {
		t.Fatalf("%d 0420s to the issuer", len(advs))
	}
	if a := advs[0]; a.STAN == adv.STAN || a.STAN == req.STAN || a.ResponseCode != "68" || a.NetworkTxnID != resp.NetworkTxnID ||
		a.OriginalData.STAN != req.STAN || a.OriginalData.TransmissionTime != req.TransmissionTime {
		t.Errorf("forwarded 0420 %+v", a)
	}
	waitFor(t, "reversal acknowledged on the trace", func() bool {
		steps := reversalSteps(f.sw, resp.NetworkTxnID)
		return len(steps) == 2 && steps[1].OK
	})

	// A repeated 0420 is acknowledged but not forwarded again.
	if ack, err := f.sw.Reverse(acquirerReversal(req)); err != nil || ack.ResponseCode != "00" {
		t.Errorf("repeat: %+v %v", ack, err)
	}
	time.Sleep(10 * time.Millisecond)
	if n := len(f.fsb.received()); n != 1 {
		t.Errorf("repeat forwarded: %d 0420s", n)
	}
}

func TestAcquirerReversalOfDecline(t *testing.T) {
	f := newFixture(t)
	req := auth("5555555555554444", "3208", 99_999)
	if resp := f.sw.Authorize(context.Background(), req); resp.ResponseCode != "51" {
		t.Fatalf("%+v", resp)
	}
	ack, err := f.sw.Reverse(acquirerReversal(req))
	if err != nil || ack.MTI != "0430" || !ack.Matched {
		t.Fatalf("%+v %v", ack, err)
	}
	time.Sleep(10 * time.Millisecond)
	if n := len(f.ucb.received()); n != 0 {
		t.Errorf("declined authorization reversed at the issuer: %d 0420s", n)
	}
}

func TestAcquirerReversalBeforeAuthorization(t *testing.T) {
	f := newFixture(t)
	req := auth("4242424242424242", "3312", 2500)
	ack, err := f.sw.Reverse(acquirerReversal(req))
	if err != nil || ack.MTI != "0430" || ack.Matched {
		t.Fatalf("%+v %v", ack, err)
	}
	resp := f.sw.Authorize(context.Background(), req)
	if resp.ResponseCode != "05" || f.fsb.calls.Load() != 0 {
		t.Errorf("late 0100: %s, issuer calls %d", resp.ResponseCode, f.fsb.calls.Load())
	}
	if steps := reversalSteps(f.sw, resp.NetworkTxnID); len(steps) != 1 {
		t.Errorf("reversal steps %+v", steps)
	}
	// A retry of the 0100 gets the same decline.
	if again := f.sw.Authorize(context.Background(), req); again != resp {
		t.Errorf("retry: %+v", again)
	}
}

func TestAcquirerReversalWhileIssuerResponds(t *testing.T) {
	f := newFixture(t)
	f.fsb.delay = 20 * time.Millisecond
	req := auth("4242424242424242", "3312", 2500)
	done := make(chan iso8583.AuthResponse)
	go func() { done <- f.sw.Authorize(context.Background(), req) }()
	waitFor(t, "0100 sent to issuer", func() bool { return f.fsb.calls.Load() == 1 })
	ack, err := f.sw.Reverse(acquirerReversal(req))
	if err != nil || !ack.Matched {
		t.Fatalf("%+v %v", ack, err)
	}
	if resp := <-done; resp.ResponseCode != "00" {
		t.Fatalf("%+v", resp)
	}
	waitFor(t, "hold released", func() bool {
		a, _ := f.fsb.bank.Snapshot("4242424242424242")
		return a.Available == 500_000
	})
}

func TestInvalidAcquirerReversal(t *testing.T) {
	f := newFixture(t)
	req := auth("4242424242424242", "3312", 100)
	for name, mutate := range map[string]func(*iso8583.ReversalAdvice){
		"wrong MTI":          func(a *iso8583.ReversalAdvice) { a.MTI = "0400" },
		"no DE90":            func(a *iso8583.ReversalAdvice) { a.OriginalData = nil },
		"bad DE90 STAN":      func(a *iso8583.ReversalAdvice) { a.OriginalData.STAN = "12" },
		"bad DE11":           func(a *iso8583.ReversalAdvice) { a.STAN = "" },
		"other acquirer":     func(a *iso8583.ReversalAdvice) { a.AcquirerID = "200002" },
		"unknown reason":     func(a *iso8583.ReversalAdvice) { a.ResponseCode = "Z9" },
		"original not 0100":  func(a *iso8583.ReversalAdvice) { a.OriginalData.MTI = "0200" },
		"bad DE90 acquirer":  func(a *iso8583.ReversalAdvice) { a.AcquirerID, a.OriginalData.AcquirerID = "", "ACQ" },
		"bad DE90 timestamp": func(a *iso8583.ReversalAdvice) { a.OriginalData.TransmissionTime = "now" },
	} {
		adv := acquirerReversal(req)
		mutate(&adv)
		if _, err := f.sw.Reverse(adv); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
