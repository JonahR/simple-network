package network

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/JonahR/simple-network/internal/card"
	"github.com/JonahR/simple-network/internal/iso8583"
	"github.com/JonahR/simple-network/internal/pin"
)

func (c *bankClient) Advise(_ context.Context, adv iso8583.AuthAdvice) (iso8583.AuthAdviceResponse, error) {
	if c.down {
		return iso8583.AuthAdviceResponse{}, ErrIssuerUnavailable
	}
	return c.bank.Advise(adv), nil
}

func withStandIn(f *fixture, enabled bool) {
	f.sw.StandIn = NewStandIn([]string{"FSB", "UCB"}, time.Now)
	f.sw.StandIn.SetEnabled("FSB", enabled)
}

func stepDetail(t *testing.T, f *fixture, id, name string) Step {
	t.Helper()
	rec, _ := f.sw.Recorder.Get(id)
	for _, s := range rec.Steps {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no %s step in %+v", name, rec.Steps)
	return Step{}
}

func available(f *fixture) int64 {
	a, _ := f.fsb.bank.Snapshot("4242424242424242")
	return a.Available
}

func TestStandInIsOffByDefault(t *testing.T) {
	f := newFixture(t)
	withStandIn(f, false)
	f.fsb.down = true
	resp := f.sw.Authorize(context.Background(), auth("4242424242424242", "3312", 1000))
	if resp.ResponseCode != "91" {
		t.Fatalf("code %s", resp.ResponseCode)
	}
	if s := stepDetail(t, f, resp.NetworkTxnID, "stand_in"); s.OK || !strings.Contains(s.Detail, "off") {
		t.Errorf("step %+v", s)
	}
}

func TestStandInApprovesAndAdvisesWhenIssuerReturns(t *testing.T) {
	f := newFixture(t)
	withStandIn(f, true)
	f.fsb.down = true
	before := available(f)
	resp := f.sw.Authorize(context.Background(), auth("4242424242424242", "3312", 2500))
	if resp.ResponseCode != "00" || !strings.HasPrefix(resp.AuthCode, "S") || resp.Amount != 2500 {
		t.Fatalf("stand-in response %+v", resp)
	}
	if v := f.sw.StandIn.View("FSB"); v.Pending != 1 {
		t.Fatalf("pending advices %d", v.Pending)
	}
	// The issuer comes back; the advice places the hold.
	f.fsb.down = false
	f.sw.deliverAdvices(context.Background(), f.sw.Issuers["FSB"])
	if v := f.sw.StandIn.View("FSB"); v.Pending != 0 || v.Delivered != 1 {
		t.Fatalf("after delivery: %+v", v)
	}
	if got := available(f); got != before-2500 {
		t.Errorf("available %d, want %d", got, before-2500)
	}
	st := f.fsb.bank.State()
	if d := st.Decisions[0]; d.Checks[0].Name != "Stand-in" || d.AuthCode != resp.AuthCode || d.Hold != "active" {
		t.Errorf("issuer decision %+v", d)
	}
	if s := stepDetail(t, f, resp.NetworkTxnID, "advice"); !s.OK {
		t.Errorf("advice step %+v", s)
	}
}

func TestStandInLimits(t *testing.T) {
	f := newFixture(t)
	withStandIn(f, true)
	f.fsb.down = true

	over := f.sw.Authorize(context.Background(), auth("4242424242424242", "3312", 10_001))
	if over.ResponseCode != "91" || !strings.Contains(stepDetail(t, f, over.NetworkTxnID, "stand_in").Detail, "over the") {
		t.Errorf("over max: %s", over.ResponseCode)
	}
	p := auth("4000056655665556", "3305", 1000)
	p.EntryMode, p.ARQC = iso8583.EntryChip, "A1B2C3D4E5F60718"
	p.PINData, _ = pin.Encrypt("1234", p.PAN, f.acqKey)
	if resp := f.sw.Authorize(context.Background(), p); resp.ResponseCode != "91" {
		t.Errorf("PIN in stand-in: %s", resp.ResponseCode)
	}
	// $250 per card per day: two $100 sales pass, a third $100 doesn't.
	for i, want := range []string{"00", "00", "91"} {
		if resp := f.sw.Authorize(context.Background(), auth("4242424242424242", "3312", 10_000)); resp.ResponseCode != want {
			t.Errorf("sale %d: %s, want %s", i+1, resp.ResponseCode, want)
		}
	}
}

func TestStandInAfterTimeoutHoldsOnce(t *testing.T) {
	f := newFixture(t)
	withStandIn(f, true)
	f.fsb.delay = 150 * time.Millisecond // Answers after the 50ms timeout
	before := available(f)
	resp := f.sw.Authorize(context.Background(), auth("4242424242424242", "3312", 3000))
	if resp.ResponseCode != "00" {
		t.Fatalf("code %s", resp.ResponseCode)
	}
	time.Sleep(250 * time.Millisecond) // The issuer's own late approval lands
	f.sw.deliverAdvices(context.Background(), f.sw.Issuers["FSB"])
	if got := available(f); got != before-3000 {
		t.Errorf("available %d, want exactly one hold (%d)", got, before-3000)
	}
	if n := len(f.fsb.received()); n != 0 {
		t.Errorf("stand-in approval was also reversed (%d 0420s)", n)
	}
}

func TestVoidBeforeAdviceLeavesNoHold(t *testing.T) {
	f := newFixture(t)
	withStandIn(f, true)
	f.fsb.down = true
	before := available(f)
	resp := f.sw.Authorize(context.Background(), auth("4242424242424242", "3312", 2000))
	f.fsb.down = false
	f.fsb.bank.Reverse(iso8583.ReversalAdvice{NetworkTxnID: resp.NetworkTxnID}) // The void reaches the issuer first
	f.sw.deliverAdvices(context.Background(), f.sw.Issuers["FSB"])
	if got := available(f); got != before {
		t.Errorf("available %d, want %d: a voided stand-in sale must not hold", got, before)
	}
	if v := f.sw.StandIn.View("FSB"); !strings.Contains(v.Recent[0].Result, "reversed") {
		t.Errorf("advice result %q", v.Recent[0].Result)
	}
}

func TestStandInStateRoundTrip(t *testing.T) {
	f := newFixture(t)
	withStandIn(f, true)
	f.fsb.down = true
	f.sw.Authorize(context.Background(), auth("4242424242424242", "3312", 2000))
	data, _ := json.Marshal(f.sw.StandIn.SaveState())
	if strings.Contains(string(data), "4242424242424242") {
		t.Fatal("saved stand-in state contains a card number")
	}
	var st StandInState
	json.Unmarshal(data, &st)
	si := NewStandIn([]string{"FSB", "UCB"}, time.Now)
	si.RestoreState(st)
	if v := si.View("FSB"); !v.Params.Enabled || v.Pending != 1 {
		t.Errorf("restored %+v", v)
	}
}

// A refund is never approved in stand-in: the issuer's 0120 handling would
// hold it like a purchase, taking money from the card instead of returning it.
func TestStandInLeavesRefundsToTheIssuer(t *testing.T) {
	f := newFixture(t)
	withStandIn(f, true)
	f.fsb.down = true
	before := available(f)
	r := auth("4242424242424242", "3312", 1_000)
	r.ProcessingCode = "200000"
	resp := f.sw.Authorize(context.Background(), r)
	if resp.ResponseCode != "91" || !strings.Contains(stepDetail(t, f, resp.NetworkTxnID, "stand_in").Detail, "refund") {
		t.Errorf("refund in stand-in: %s", resp.ResponseCode)
	}
	f.fsb.down = false
	if available(f) != before {
		t.Errorf("available %d, want %d: nothing may be held for a refund", available(f), before)
	}
}

// The 0120 carries a keyed card reference: a plain hash of the card number
// could be reversed by trying every number in the BIN range.
func TestStandInAdviceUsesKeyedCardRef(t *testing.T) {
	f := newFixture(t)
	withStandIn(f, true)
	f.fsb.down = true
	f.sw.Authorize(context.Background(), auth("4242424242424242", "3312", 2000))
	data, _ := json.Marshal(f.sw.StandIn.SaveState())
	sum := sha256.Sum256([]byte("4242424242424242"))
	for _, s := range []string{hex.EncodeToString(sum[:]), "3312", "pan_sha256"} {
		if strings.Contains(string(data), s) {
			t.Errorf("saved stand-in state contains %s", s)
		}
	}
	if !strings.Contains(string(data), card.Ref("4242424242424242")) {
		t.Error("saved advice lacks the card reference")
	}
	// The issuer still finds the account by reference. (The fixture's issuer
	// stays down: flipping it would race the background delivery.)
	before := available(f)
	resp := f.fsb.bank.Advise(iso8583.AuthAdvice{NetworkTxnID: "ref-test", CardRef: card.Ref("4242424242424242"), Amount: 500})
	if !resp.Held || available(f) != before-500 {
		t.Errorf("advice by reference: %+v, available %d → %d", resp, before, available(f))
	}
}
