package fraud

import (
	"strings"
	"testing"
	"time"

	"github.com/JonahR/simple-network/internal/iso8583"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }
func newClock() *clock                   { return &clock{t: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)} }
func req(pan, terminal string) iso8583.AuthRequest {
	return iso8583.AuthRequest{
		PAN: pan, AcquirerID: "100001", MerchantID: "000000000099001", TerminalID: terminal,
		MerchantNameLoc: "Risk Lab                 San Francisco US", Amount: 100, EntryMode: iso8583.EntryEcommerce, MCC: "5999",
	}
}

// decline runs one authorization through the engine as the switch does and
// returns whether the fraud department declined it.
func decline(e *Engine, r iso8583.AuthRequest, issuerCode string) bool {
	v := e.Screen(r, "txn")
	code := issuerCode
	if v.Decline {
		code = iso8583.RCSuspectedFraud
	}
	e.Observe(r, code, 0, v.Decline)
	return v.Decline
}

func TestTerminalDeclinesBlockTheTerminal(t *testing.T) {
	c := newClock()
	e := NewEngine([]Rule{DefaultRules()[0]}, c.now) // More than 5 declines in 60s → block 2 minutes
	for i := range 6 {
		if decline(e, req("4242424242424242", "RISK0001"), iso8583.RCInvalidCard) {
			t.Fatalf("attempt %d declined; only past declines count", i+1)
		}
	}
	if !decline(e, req("4000056655665556", "RISK0001"), iso8583.RCApproved) {
		t.Fatal("seventh attempt after six declines was not stopped")
	}
	// The block applies to any card at that terminal, but not other terminals.
	if !decline(e, req("5555555555554444", "RISK0001"), iso8583.RCApproved) {
		t.Error("blocked terminal let a different card through")
	}
	if decline(e, req("5555555555554444", "TERM0001"), iso8583.RCApproved) {
		t.Error("block spilled over to another terminal")
	}
	s := e.Snapshot()
	if len(s.Blocks) != 1 || len(s.Cases) != 1 || s.Cases[0].Declined != 2 {
		t.Fatalf("blocks %d, cases %+v", len(s.Blocks), s.Cases)
	}
	c.advance(121 * time.Second)
	if decline(e, req("5555555555554444", "RISK0001"), iso8583.RCApproved) {
		t.Error("block outlived its 2 minutes")
	}
}

func TestDeclinesSlideOutOfTheWindow(t *testing.T) {
	c := newClock()
	e := NewEngine([]Rule{DefaultRules()[1]}, c.now) // Card: more than 3 declines in 10 minutes → decline
	r := req("4242424242424242", "RISK0001")
	for range 3 {
		decline(e, r, iso8583.RCCVVMismatch)
	}
	c.advance(11 * time.Minute)
	if decline(e, r, iso8583.RCCVVMismatch) {
		t.Error("declines from 11 minutes ago still counted")
	}
}

func TestOutagesAndOwnDeclinesDontCount(t *testing.T) {
	c := newClock()
	e := NewEngine([]Rule{DefaultRules()[1]}, c.now)
	r := req("5555555555554444", "RISK0001")
	for range 10 {
		decline(e, r, iso8583.RCIssuerUnavailable)
	}
	if decline(e, r, iso8583.RCApproved) {
		t.Error("issuer outages counted as card declines")
	}
}

func TestTestModeChangesNothing(t *testing.T) {
	c := newClock()
	rule := DefaultRules()[0]
	rule.Mode = ModeTest
	e := NewEngine([]Rule{rule}, c.now)
	for range 10 {
		if decline(e, req("4242424242424242", "RISK0001"), iso8583.RCInvalidCard) {
			t.Fatal("a test-mode rule declined")
		}
	}
	s := e.Snapshot()
	if len(s.Blocks) != 0 || s.Rules[0].Stats.Hits != 4 || len(s.Cases) != 1 || s.Cases[0].Mode != ModeTest {
		t.Fatalf("blocks %v, hits %d, cases %+v", s.Blocks, s.Rules[0].Stats.Hits, s.Cases)
	}
	if !strings.Contains(e.Screen(req("4242424242424242", "RISK0001"), "x").Detail, "test mode") {
		t.Error("trace detail doesn't say the rule is in test mode")
	}
}

func TestAmountRuleFiltersByEntryMode(t *testing.T) {
	c := newClock()
	rule := DefaultRules()[3] // More than $1,000, keyed or online → review
	rule.Mode, rule.Action = ModeOn, ActionDecline
	e := NewEngine([]Rule{rule}, c.now)
	big := req("4242424242424242", "RISK0001")
	big.Amount = 150_000
	if !e.Screen(big, "a").Decline {
		t.Error("large online purchase passed")
	}
	big.EntryMode = iso8583.EntryChip
	if e.Screen(big, "b").Decline {
		t.Error("rule for keyed and online purchases fired on a chip purchase")
	}
}

func TestSpendRuleCountsThisPurchase(t *testing.T) {
	c := newClock()
	e := NewEngine([]Rule{{Name: "Spend", Mode: ModeOn, Kind: KindVelocity, Subject: SubjectCard, Measure: MeasureSpend, Threshold: 10_000, WindowSec: 3600, Action: ActionDecline}}, c.now)
	r := req("4242424242424242", "RISK0001")
	r.Amount = 6_000
	if e.Screen(r, "a").Decline {
		t.Fatal("first $60 declined")
	}
	e.Observe(r, iso8583.RCApproved, 6_000, false)
	r.Amount = 3_000 // $90 in the hour: still under $100
	if e.Screen(r, "b").Decline {
		t.Fatal("$90 total declined")
	}
	r.Amount = 5_000 // $110 with this one
	if !e.Screen(r, "c").Decline {
		t.Error("spend over $100 within the hour not declined")
	}
}

func TestClearingACaseLiftsItsBlock(t *testing.T) {
	c := newClock()
	e := NewEngine([]Rule{DefaultRules()[0]}, c.now)
	for range 7 {
		decline(e, req("4242424242424242", "RISK0001"), iso8583.RCInvalidCard)
	}
	s := e.Snapshot()
	if len(s.Blocks) != 1 {
		t.Fatal("no block")
	}
	if err := e.ResolveCase(s.Cases[0].ID, CaseCleared, "known tester"); err != nil {
		t.Fatal(err)
	}
	if len(e.Snapshot().Blocks) != 0 {
		t.Error("clearing the case left the block in place")
	}
}

func TestForgetResetsASubject(t *testing.T) {
	c := newClock()
	e := NewEngine([]Rule{DefaultRules()[0]}, c.now)
	r := req("4242424242424242", "RISK0001")
	for range 7 {
		decline(e, r, iso8583.RCInvalidCard)
	}
	key, _ := KeyOf(SubjectTerminal, r)
	if n := e.Forget([]SubjectKey{{SubjectTerminal, key}}, "drill"); n != 1 {
		t.Errorf("closed %d cases, want 1", n)
	}
	if decline(e, r, iso8583.RCInvalidCard) {
		t.Error("terminal still blocked after Forget")
	}
}

func TestBacktestReplaysHistory(t *testing.T) {
	base := newClock().t
	var txns []Txn
	for i := range 8 {
		txns = append(txns, Txn{Time: base.Add(time.Duration(i) * time.Second), ID: "t", Req: req("424242******0000", "RISK0001"), Code: iso8583.RCInvalidCard})
	}
	bt, err := RunBacktest(DefaultRules()[0], txns)
	if err != nil {
		t.Fatal(err)
	}
	// Six declines, then the seventh is blocked (a hit) and the eighth hits the block.
	if bt.Hits != 1 || bt.Declined != 2 || bt.WhileBlocked != 1 || len(bt.Top) != 1 || !strings.Contains(bt.Top[0].Label, "RISK0001") {
		t.Fatalf("%+v", bt)
	}
}

func TestSentences(t *testing.T) {
	for _, tc := range []struct {
		r    Rule
		want string
	}{
		{DefaultRules()[0], "When one terminal has more than 5 declines within 1 minute → decline it and block the terminal for 2 minutes"},
		{DefaultRules()[3], "When a purchase is more than $1,000.00, by manual key entry or e-commerce → let it through and open a case"},
	} {
		if got := tc.r.Sentence(); got != tc.want {
			t.Errorf("got  %q\nwant %q", got, tc.want)
		}
	}
}

func TestEveryTemplateIsValid(t *testing.T) {
	for _, r := range append(DefaultRules(), Templates()...) {
		if err := r.Validate(); err != nil {
			t.Errorf("%s: %v", r.Name, err)
		}
	}
}
