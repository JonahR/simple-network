package clearing

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/JonahR/simple-network/internal/card"
)

type auths map[string]AuthInfo

func (a auths) Lookup(id string) (AuthInfo, bool) { v, ok := a[id]; return v, ok }

type fakeIssuer struct {
	files []IssuerFile
	err   error
}

func (f *fakeIssuer) Post(_ context.Context, file IssuerFile) (IssuerAck, error) {
	if f.err != nil {
		return IssuerAck{}, f.err
	}
	f.files = append(f.files, file)
	return IssuerAck{FileID: file.FileID, Posted: len(file.Records)}, nil
}

type fakeAcquirer struct {
	advices []Advice
	send    func() error
}

func (f *fakeAcquirer) RequestFile(context.Context) error {
	if f.send != nil {
		return f.send()
	}
	return nil
}
func (f *fakeAcquirer) Advise(_ context.Context, a Advice) error {
	f.advices = append(f.advices, a)
	return nil
}

var testNow = time.Date(2026, 10, 9, 22, 0, 0, 0, time.UTC)

func newEngine(fees FeeTable, a auths, issuers []string, acquirers []string) (*Engine, map[string]*fakeIssuer, map[string]*fakeAcquirer) {
	e := &Engine{Fees: fees, Auths: a, Issuers: map[string]IssuerClient{}, Acquirers: map[string]AcquirerClient{},
		Names: map[string]string{}, Now: func() time.Time { return testNow }, OpeningBalance: 1_000_000}
	fi, fa := map[string]*fakeIssuer{}, map[string]*fakeAcquirer{}
	for _, id := range issuers {
		fi[id] = &fakeIssuer{}
		e.Issuers[id] = fi[id]
	}
	for _, id := range acquirers {
		fa[id] = &fakeAcquirer{}
		e.Acquirers[id] = fa[id]
	}
	return e, fi, fa
}

func pres(id string, amount int64) Presentment {
	return Presentment{NetworkTxnID: id, ARN: "ARN-" + id, Amount: amount, Currency: "840", MCC: "5814", EntryMode: "051", ProcessingCode: "000000"}
}

// TestKTWorkedExample reproduces KT 05's three-bank day: interchange 1.80%,
// network fees 0.15% on the acquirer and 0.05% on the issuer. Bank B is both
// an issuer and an acquirer, so its two positions net into one payment.
func TestKTWorkedExample(t *testing.T) {
	fees := FeeTable{Version: "kt05", Programs: []Program{{ID: "KT", RateBPS: 180}}, AcquirerAssessmentBPS: 15, IssuerAssessmentBPS: 5}
	a := auths{
		"t1": {AcquirerID: "A1", IssuerID: "I1", Approved: true, Amount: 100_000, Currency: "840", Product: card.Credit},
		"t2": {AcquirerID: "A1", IssuerID: "B", Approved: true, Amount: 50_000, Currency: "840", Product: card.Credit},
		"t3": {AcquirerID: "B", IssuerID: "I1", Approved: true, Amount: 30_000, Currency: "840", Product: card.Credit},
		"t4": {AcquirerID: "B", IssuerID: "B", Approved: true, Amount: 220_000, Currency: "840", Product: card.Credit},
	}
	e, _, _ := newEngine(fees, a, []string{"I1", "B"}, []string{"A1", "B"})
	for _, f := range []File{
		NewFile(FileHeader{FileID: "A1-1", AcquirerID: "A1", Sequence: 1}, []Presentment{pres("t1", 100_000), pres("t2", 50_000)}),
		NewFile(FileHeader{FileID: "B-1", AcquirerID: "B", Sequence: 1}, []Presentment{pres("t3", 30_000), pres("t4", 220_000)}),
	} {
		if ack := e.Receive(f); ack.Status != FileAccepted || len(ack.Rejected) != 0 {
			t.Fatalf("file %s: %+v", f.Header.FileID, ack)
		}
	}
	c := e.Close(context.Background())
	if c.Status != CycleSettled {
		t.Fatalf("status %s, stages %+v", c.Status, c.Stages)
	}

	got := map[string]int64{}
	for _, tr := range c.Transfers {
		if tr.From == NetworkID {
			got[tr.To] += tr.Amount
		} else {
			got[tr.From] -= tr.Amount
		}
	}
	want := map[string]int64{"I1": -127_725, "B": -20_150, "A1": 147_075}
	for m, w := range want {
		if got[m] != w {
			t.Errorf("%s net = %d, want %d", m, got[m], w)
		}
	}
	if len(c.Transfers) != 3 {
		t.Errorf("want 3 payments after netting, got %d: %+v", len(c.Transfers), c.Transfers)
	}
	if len(c.Checks) != 1 || !c.Checks[0].OK || c.Checks[0].Revenue != 800 {
		t.Errorf("check %+v, want network revenue 800 and a zero sum", c.Checks)
	}

	// After settlement every due-from and due-to account is back to zero,
	// and the network's cash equals its fee revenue.
	v := e.View(100)
	for _, b := range v.Balances {
		switch {
		case strings.HasPrefix(b.Account, "due_"):
			if b.Net != 0 {
				t.Errorf("%s not cleared after settlement: %d", b.Account, b.Net)
			}
		case b.Account == AccountCash && b.Net != 800, b.Account == AccountRevenue && b.Net != -800:
			t.Errorf("%s = %d", b.Account, b.Net)
		}
	}
}

// TestSettlementIsZeroSum is D6's property test: for random days of
// transactions, member positions plus network revenue always sum to zero,
// every ledger entry balances, and settlement leaves nothing owed.
func TestSettlementIsZeroSum(t *testing.T) {
	products := []card.Product{card.Credit, card.Debit, card.Prepaid, card.Fleet, card.Healthcare}
	entries := []string{"011", "051", "071", "901", "812", "100"}
	issuers, acquirers := []string{"FSB", "UCB", "X"}, []string{"A1", "A2"}
	for seed := uint64(0); seed < 300; seed++ {
		r := rand.New(rand.NewPCG(seed, 7))
		a := auths{}
		files := map[string][]Presentment{}
		for i := range 1 + r.IntN(40) {
			id := fmt.Sprintf("t%d", i)
			acq := acquirers[r.IntN(len(acquirers))]
			amt := int64(1 + r.IntN(500_000))
			cur := []string{"840", "978"}[r.IntN(2)]
			a[id] = AuthInfo{AcquirerID: acq, IssuerID: issuers[r.IntN(len(issuers))], Approved: true, Amount: amt, Currency: cur, Product: products[r.IntN(len(products))]}
			p := pres(id, amt)
			p.Currency, p.EntryMode = cur, entries[r.IntN(len(entries))]
			if r.IntN(4) == 0 {
				p.Cashback = int64(r.IntN(int(amt)))
			}
			files[acq] = append(files[acq], p)
		}
		e, _, _ := newEngine(DefaultFees(), a, issuers, acquirers)
		for _, acq := range acquirers {
			e.Receive(NewFile(FileHeader{FileID: acq + "-1", AcquirerID: acq, Sequence: 1}, files[acq]))
		}
		c := e.Close(context.Background())
		if c.Status != CycleSettled {
			t.Fatalf("seed %d: status %s", seed, c.Status)
		}
		for _, chk := range c.Checks {
			if chk.Sum != 0 {
				t.Fatalf("seed %d: %s not zero-sum: %+v", seed, chk.Currency, chk)
			}
		}
		for _, b := range e.View(0).Balances {
			if strings.HasPrefix(b.Account, "due_") && b.Net != 0 {
				t.Fatalf("seed %d: %s left at %d after settlement", seed, b.Account, b.Net)
			}
		}
	}
}

func TestFileControls(t *testing.T) {
	a := auths{"t1": {AcquirerID: "A1", IssuerID: "I1", Approved: true, Amount: 1000, Currency: "840", Product: card.Credit}}
	e, _, _ := newEngine(DefaultFees(), a, []string{"I1"}, []string{"A1"})

	bad := NewFile(FileHeader{FileID: "f0", AcquirerID: "A1", Sequence: 1}, []Presentment{pres("t1", 1000)})
	bad.Trailer.HashTotal = 999
	if ack := e.Receive(bad); ack.Status != FileRejected || !strings.Contains(ack.Reason, "hash total") {
		t.Errorf("bad hash total: %+v", ack)
	}
	gap := NewFile(FileHeader{FileID: "f2", AcquirerID: "A1", Sequence: 2}, nil)
	if ack := e.Receive(gap); ack.Status != FileRejected || !strings.Contains(ack.Reason, "expected 1") {
		t.Errorf("sequence gap: %+v", ack)
	}
	good := NewFile(FileHeader{FileID: "f1", AcquirerID: "A1", Sequence: 1}, []Presentment{pres("t1", 1000)})
	if ack := e.Receive(good); ack.Status != FileAccepted || ack.Accepted != 1 {
		t.Fatalf("good file: %+v", ack)
	}
	// Sending the same file again changes nothing.
	if ack := e.Receive(good); ack.Status != FileDuplicate || ack.Accepted != 1 {
		t.Errorf("duplicate: %+v", ack)
	}
	if v := e.View(10); len(v.Open.Items) != 1 || v.EntryCount != 1 {
		t.Errorf("duplicate file cleared twice: %d items, %d entries", len(v.Open.Items), v.EntryCount)
	}
}

func TestMatchingRejects(t *testing.T) {
	a := auths{
		"ok":       {AcquirerID: "A1", IssuerID: "I1", Approved: true, Amount: 1000, Currency: "840", Product: card.Credit},
		"declined": {AcquirerID: "A1", IssuerID: "I1", Approved: false, Amount: 0, Currency: "840"},
		"reversed": {AcquirerID: "A1", IssuerID: "I1", Approved: true, Amount: 1000, Currency: "840", Reversed: true},
		"other":    {AcquirerID: "A2", IssuerID: "I1", Approved: true, Amount: 1000, Currency: "840"},
		"tip":      {AcquirerID: "A1", IssuerID: "I1", Approved: true, Amount: 1000, Currency: "840", Product: card.Credit},
		"over":     {AcquirerID: "A1", IssuerID: "I1", Approved: true, Amount: 1000, Currency: "840"},
	}
	e, _, _ := newEngine(DefaultFees(), a, []string{"I1"}, []string{"A1", "A2"})
	tip := pres("tip", 1200)
	tip.MCC = "5812" // Restaurants allow 20% over for the tip.
	ack := e.Receive(NewFile(FileHeader{FileID: "f1", AcquirerID: "A1", Sequence: 1}, []Presentment{
		pres("ok", 1000), pres("ok", 1000), pres("missing", 1000), pres("declined", 1000),
		pres("reversed", 1000), pres("other", 1000), tip, pres("over", 1001),
	}))
	if ack.Accepted != 2 {
		t.Errorf("accepted %d, want 2 (ok, tip)", ack.Accepted)
	}
	reasons := map[string]string{}
	for _, r := range ack.Rejected {
		reasons[r.NetworkTxnID] += r.Reason
	}
	for id, want := range map[string]string{
		"ok": "already presented", "missing": "no matching", "declined": "declined",
		"reversed": "reversed", "other": "another acquirer", "over": "tolerance",
	} {
		if !strings.Contains(reasons[id], want) {
			t.Errorf("%s: reason %q, want %q", id, reasons[id], want)
		}
	}
}

func TestPricing(t *testing.T) {
	fees := DefaultFees()
	tests := []struct {
		product      card.Product
		ch           Channel
		amount, cash int64
		program      string
		interchange  int64
	}{
		{card.Credit, CardPresent, 10_000, 0, "CP_CREDIT_STD", 160}, // 1.50% + $0.10
		{card.Credit, CardNotPresent, 10_000, 0, "CNP_CREDIT_STD", 190},
		{card.Debit, CardPresent, 10_000, 2_000, "DEBIT_CP", 79}, // 0.80% of $80 + $0.15: cash back earns none
		{card.Prepaid, CardNotPresent, 10_000, 0, "PREPAID", 130},
		{card.Fleet, CardPresent, 10_000, 0, "COMMERCIAL_FLEET", 260},
		{card.Product("unknown"), CardPresent, 10_000, 0, "DEFAULT", 210},
	}
	for _, tt := range tests {
		p := fees.Price(tt.product, tt.ch, tt.amount, tt.cash)
		if p.ProgramID != tt.program || p.Interchange != tt.interchange || p.FeeVersion != fees.Version {
			t.Errorf("%s %s: %+v, want %s %d", tt.product, tt.ch, p, tt.program, tt.interchange)
		}
	}
	// Half-up rounding per transaction.
	if BPS(1, 5_000) != 1 || BPS(1, 4_999) != 0 || BPS(333, 150) != 5 {
		t.Error("BPS rounding")
	}
}

func TestIssuerDeliveryFailureIsRetried(t *testing.T) {
	a := auths{
		"t1": {AcquirerID: "A1", IssuerID: "I1", Approved: true, Amount: 1000, Currency: "840", Product: card.Credit},
		"t2": {AcquirerID: "A1", IssuerID: "I1", Approved: true, Amount: 500, Currency: "840", Product: card.Credit},
	}
	e, iss, acq := newEngine(DefaultFees(), a, []string{"I1"}, []string{"A1"})
	iss["I1"].err = errors.New("issuer down")
	e.Receive(NewFile(FileHeader{FileID: "f1", AcquirerID: "A1", Sequence: 1}, []Presentment{pres("t1", 1000)}))
	c := e.Close(context.Background())
	// The issuer still owes the money; settlement goes ahead.
	if c.Status != CycleSettled || !strings.HasPrefix(c.IssuerFiles[0].Status, "failed") || c.Items[0].Posting != "delivery failed" {
		t.Fatalf("cycle 1: %s, %+v, posting %s", c.Status, c.IssuerFiles, c.Items[0].Posting)
	}
	if len(acq["A1"].advices) != 1 || acq["A1"].advices[0].Net != c.Items[0].AcquirerGets {
		t.Errorf("advice: %+v", acq["A1"].advices)
	}

	iss["I1"].err = nil
	e.Receive(NewFile(FileHeader{FileID: "f2", AcquirerID: "A1", Sequence: 2}, []Presentment{pres("t2", 500)}))
	c2 := e.Close(context.Background())
	if len(iss["I1"].files) != 2 || len(c2.IssuerFiles) != 2 {
		t.Fatalf("retry: delivered %d files, cycle 2 lists %d", len(iss["I1"].files), len(c2.IssuerFiles))
	}
	v := e.View(0)
	if v.History[1].Items[0].Posting != "posted" || v.Undelivered != 0 {
		t.Errorf("cycle 1 item after retry: %s, undelivered %d", v.History[1].Items[0].Posting, v.Undelivered)
	}
}

func TestRunRequestsFilesThenCloses(t *testing.T) {
	a := auths{"t1": {AcquirerID: "A1", IssuerID: "I1", Approved: true, Amount: 1000, Currency: "840", Product: card.Credit}}
	e, _, acq := newEngine(DefaultFees(), a, []string{"I1"}, []string{"A1"})
	acq["A1"].send = func() error {
		e.Receive(NewFile(FileHeader{FileID: "f1", AcquirerID: "A1", Sequence: 1}, []Presentment{pres("t1", 1000)}))
		return nil
	}
	c, err := e.Run(context.Background())
	if err != nil || c.Status != CycleSettled || len(c.Items) != 1 {
		t.Fatalf("run: %v, %+v", err, c)
	}
	v := e.View(0)
	if v.Open.Number != 2 || v.Open.BusinessDate != "2026-10-10" || c.ValueDate != "2026-10-10" {
		t.Errorf("next cycle %s, value date %s", v.Open.ID, c.ValueDate)
	}
}
