package dispute

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JonahR/simple-network/internal/clearing"
	"github.com/JonahR/simple-network/internal/iso8583"
)

type source map[string]Facts

func (s source) Facts(id string) (Facts, bool) { f, ok := s[id]; return f, ok }

type fakeMoney struct {
	date  string
	moves []clearing.DisputeMoney
}

func (m *fakeMoney) AddDispute(d clearing.DisputeMoney) (clearing.Item, error) {
	m.moves = append(m.moves, d)
	return clearing.Item{CycleID: m.date + ".1"}, nil
}
func (m *fakeMoney) BusinessDate() string { return m.date }

type notifier struct {
	mu      sync.Mutex
	notices []Notice
	fail    bool
}

func (n *notifier) NotifyDispute(_ context.Context, x Notice) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.fail {
		return context.DeadlineExceeded
	}
	n.notices = append(n.notices, x)
	return nil
}
func (n *notifier) last() Notice { n.mu.Lock(); defer n.mu.Unlock(); return n.notices[len(n.notices)-1] }

func sale(id string, amount int64, entry string, ch clearing.Channel) clearing.Item {
	return clearing.Item{Presentment: clearing.Presentment{NetworkTxnID: id, ARN: "ARN" + id, Amount: amount, Currency: "840",
		MerchantID: "M1", MerchantName: "Shop", EntryMode: entry, PAN: "424242******4242"},
		AcquirerID: "100001", IssuerID: "FSB", Channel: ch}
}

func setup() (*Engine, source, *fakeMoney, *notifier) {
	src := source{
		"keyed":    {Sale: sale("keyed", 10_000, iso8583.EntryManual, clearing.CardNotPresent), ClearedOn: "2026-10-10"},
		"3ds":      {Sale: sale("3ds", 5_000, iso8583.EntryEcommerce, clearing.CardNotPresent), ClearedOn: "2026-10-10", ECI: "05"},
		"chip":     {Sale: sale("chip", 5_000, iso8583.EntryChip, clearing.CardPresent), ClearedOn: "2026-10-10"},
		"swiped":   {Sale: sale("swiped", 5_000, iso8583.EntryMagstripe, clearing.CardPresent), ClearedOn: "2026-10-10"},
		"refunded": {Sale: sale("refunded", 5_000, iso8583.EntryChip, clearing.CardPresent), ClearedOn: "2026-10-10", Refunded: 5_000},
		"old":      {Sale: sale("old", 5_000, iso8583.EntryChip, clearing.CardPresent), ClearedOn: "2026-05-01"},
		"standin":  {Sale: sale("standin", 5_000, iso8583.EntryManual, clearing.CardNotPresent), ClearedOn: "2026-10-10", StandIn: true},
	}
	m := &fakeMoney{date: "2026-10-12"}
	n := &notifier{}
	e := &Engine{Source: src, Money: m, Notifiers: map[string]Notifier{"FSB": n}, Now: time.Now}
	return e, src, m, n
}

func TestEligibility(t *testing.T) {
	e, _, _, _ := setup()
	tests := []struct {
		txn, reason string
		amount      int64
		want        string // Substring of the refusal, or "" to allow
	}{
		{"keyed", "13.1", 10_000, ""},
		{"keyed", "10.4", 10_000, ""},
		{"keyed", "13.1", 10_001, "at most"},
		{"3ds", "10.4", 5_000, "3-D Secure"},
		{"chip", "10.4", 5_000, "card was present"},
		{"chip", "10.1", 5_000, "chip"},
		{"swiped", "10.1", 5_000, ""},
		{"standin", "10.4", 5_000, "stand-in"},
		{"refunded", "13.1", 5_000, "credit already issued"},
		{"old", "13.1", 5_000, "120-day"},
		{"chip", "13.2", 5_000, "recurring"},
		{"chip", "12.6", 5_000, "no other cleared sale"},
		{"missing", "13.1", 5_000, "no cleared sale"},
		{"chip", "99.9", 5_000, "unknown reason"},
	}
	for _, tt := range tests {
		got := e.Eligible(tt.txn, tt.reason, tt.amount)
		if (tt.want == "" && got != "") || (tt.want != "" && !strings.Contains(got, tt.want)) {
			t.Errorf("%s %s: %q, want %q", tt.txn, tt.reason, got, tt.want)
		}
	}
}

func TestMerchantWinsArbitration(t *testing.T) {
	ctx := context.Background()
	e, _, m, n := setup()
	d, err := e.File(ctx, "keyed", "13.1", 4_000, "")
	if err != nil {
		t.Fatal(err)
	}
	if d.State != StateChargeback || d.Deadline != "2026-11-11" || m.moves[0].Kind != clearing.DisputeChargeback || m.moves[0].Amount != 4_000 {
		t.Fatalf("filed %+v, moves %+v", d, m.moves)
	}
	if nn := n.last(); !nn.Credit || nn.Amount != 4_000 {
		t.Errorf("cardholder not credited: %+v", nn)
	}
	// Partial disputes add up to the sale.
	if why := e.Eligible("keyed", "13.1", 6_001); !strings.Contains(why, "already disputed") {
		t.Errorf("over-dispute allowed: %q", why)
	}
	if _, err := e.PreArbitrate(ctx, d.ID, "", ""); err == nil || !strings.Contains(err.Error(), "acquirer's turn") {
		t.Errorf("issuer acted out of turn: %v", err)
	}
	if _, err := e.Represent(ctx, d.ID, "chip_read", ""); err == nil {
		t.Error("chip_read accepted as an answer to 13.1")
	}
	if d, err = e.Represent(ctx, d.ID, "proof_of_delivery", "Tracking 1Z999"); err != nil || d.State != StateRepresented {
		t.Fatalf("represent: %v %+v", err, d)
	}
	// A rebuttal that doesn't answer proof of delivery.
	if d, err = e.PreArbitrate(ctx, d.ID, "expert_opinion", ""); err != nil || d.State != StatePreArbitration {
		t.Fatalf("pre-arb: %v", err)
	}
	if d, err = e.Arbitrate(ctx, d.ID, ""); err != nil {
		t.Fatal(err)
	}
	if d.State != StateClosed || d.Outcome != OutcomeCardholderRebill {
		t.Fatalf("outcome %+v", d)
	}
	last := m.moves[len(m.moves)-1]
	if last.Kind != clearing.DisputeFee || last.FeePayer != clearing.RoleIssuer || last.Amount != ArbitrationFee {
		t.Errorf("fee %+v", last)
	}
	if n.last().Credit {
		t.Error("cardholder kept the credit after losing")
	}
}

func TestIssuerWinsArbitration(t *testing.T) {
	ctx := context.Background()
	e, _, m, _ := setup()
	d, _ := e.File(ctx, "keyed", "13.1", 10_000, "")
	e.Represent(ctx, d.ID, "proof_of_delivery", "")
	e.PreArbitrate(ctx, d.ID, "wrong_address", "")
	d, _ = e.Arbitrate(ctx, d.ID, "")
	if d.Outcome != OutcomeMerchantLiable {
		t.Fatalf("outcome %s", d.Outcome)
	}
	kinds := []string{}
	for _, mv := range m.moves {
		kinds = append(kinds, mv.Kind+"/"+mv.FeePayer)
	}
	// chargeback, representment, chargeback again, acquirer pays the fee
	if got := strings.Join(kinds, ","); got != "chargeback/,representment/,chargeback/,arbitration_fee/acquirer" {
		t.Errorf("moves %s", got)
	}
}

func TestDeadlinesAreEnforced(t *testing.T) {
	ctx := context.Background()
	e, _, _, n := setup()
	a, _ := e.File(ctx, "keyed", "13.1", 1_000, "")
	b, _ := e.File(ctx, "swiped", "10.1", 1_000, "")
	e.Represent(ctx, b.ID, "chip_read", "")
	e.Advance(ctx, 31)
	v := e.View()
	got := map[string]string{}
	for _, d := range v.Disputes {
		got[d.ID] = d.Outcome
	}
	if got[a.ID] != OutcomeMerchantLiable || got[b.ID] != OutcomeCardholderRebill {
		t.Errorf("after deadlines: %v", got)
	}
	if v.Today != "2026-11-12" || v.Offset != 31 {
		t.Errorf("clock %s +%d", v.Today, v.Offset)
	}
	if _, err := e.Accept(ctx, a.ID, ActorAcquirer, ""); err == nil {
		t.Error("acted on a closed dispute")
	}
	_ = n
}

func TestRefundIssuedEvidence(t *testing.T) {
	ctx := context.Background()
	e, src, _, _ := setup()
	d, _ := e.File(ctx, "keyed", "13.3", 2_000, "")
	if _, err := e.Represent(ctx, d.ID, "refund_issued", ""); err == nil {
		t.Fatal("refund_issued accepted with no refund on record")
	}
	f := src["keyed"]
	f.Refunded = 2_000
	src["keyed"] = f
	if d, err := e.Represent(ctx, d.ID, "refund_issued", ""); err != nil || d.State != StateRepresented {
		t.Fatalf("%v %+v", err, d)
	}
}

func TestNoticesRetryAndStateRoundTrip(t *testing.T) {
	ctx := context.Background()
	e, src, m, n := setup()
	n.fail = true
	d, _ := e.File(ctx, "keyed", "13.1", 1_000, "")
	n.fail = false
	e.Sync(ctx)
	if got := n.last(); got.DisputeID != d.ID || !got.Credit {
		t.Fatalf("retried notice %+v", got)
	}
	count := len(n.notices)
	e.Sync(ctx)
	if len(n.notices) != count {
		t.Error("acknowledged notice sent again")
	}

	data, _ := json.Marshal(e.SaveState())
	var st State
	json.Unmarshal(data, &st)
	e2 := &Engine{Source: src, Money: m, Notifiers: map[string]Notifier{"FSB": n}, Now: time.Now}
	e2.RestoreState(st)
	if v := e2.View(); len(v.Disputes) != 1 || v.Disputes[0].State != StateChargeback {
		t.Fatalf("restored %+v", v.Disputes)
	}
	e2.Sync(ctx)
	if len(n.notices) != count {
		t.Error("restored engine re-sent an acknowledged notice")
	}
	if d2, err := e2.File(ctx, "keyed", "13.3", 1_000, ""); err != nil || d2.ID != "D2" {
		t.Errorf("sequence after restore: %v %s", err, d2.ID)
	}
}
