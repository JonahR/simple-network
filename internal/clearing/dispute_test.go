package clearing

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/JonahR/simple-network/internal/card"
)

// Disputes add items to the next cycle; every cycle still sums to zero and
// settles to nothing owed.
func TestDisputesAreZeroSum(t *testing.T) {
	issuers, acquirers := []string{"FSB", "UCB"}, []string{"A1", "A2"}
	for seed := uint64(0); seed < 200; seed++ {
		r := rand.New(rand.NewPCG(seed, 11))
		a := auths{}
		files := map[string][]Presentment{}
		for i := range 1 + r.IntN(20) {
			id := fmt.Sprintf("t%d", i)
			acq := acquirers[r.IntN(2)]
			amt := int64(100 + r.IntN(50_000))
			a[id] = AuthInfo{AcquirerID: acq, IssuerID: issuers[r.IntN(2)], Approved: true, Amount: amt, Currency: "840", Product: card.Credit}
			files[acq] = append(files[acq], pres(id, amt))
		}
		e, _, _ := newEngine(DefaultFees(), a, issuers, acquirers)
		for _, acq := range acquirers {
			e.Receive(NewFile(FileHeader{FileID: acq + "-1", AcquirerID: acq, Sequence: 1}, files[acq]))
		}
		e.Close(context.Background())

		// The next day, dispute some of the cleared sales.
		for _, it := range e.ClearedSales() {
			switch r.IntN(4) {
			case 0:
				e.AddDispute(DisputeMoney{Kind: DisputeChargeback, DisputeID: "D-" + it.NetworkTxnID, Original: it, Amount: 1 + int64(r.IntN(int(it.Amount)))})
			case 1:
				e.AddDispute(DisputeMoney{Kind: DisputeRepresentment, DisputeID: "D-" + it.NetworkTxnID, Original: it, Amount: it.Amount})
			case 2:
				e.AddDispute(DisputeMoney{Kind: DisputeFee, DisputeID: "D-" + it.NetworkTxnID, Original: it, Amount: 25_000, FeePayer: []string{RoleIssuer, RoleAcquirer}[r.IntN(2)]})
			}
		}
		c := e.Close(context.Background())
		if c.Status != CycleSettled {
			t.Fatalf("seed %d: %s", seed, c.Status)
		}
		for _, chk := range c.Checks {
			if chk.Sum != 0 {
				t.Fatalf("seed %d: not zero-sum %+v", seed, chk)
			}
		}
		for _, b := range e.View(0).Balances {
			if strings.HasPrefix(b.Account, "due_") && b.Net != 0 {
				t.Fatalf("seed %d: %s left at %d", seed, b.Account, b.Net)
			}
		}
	}
}

func TestDisputeMoneyDirections(t *testing.T) {
	a := auths{"t1": {AcquirerID: "A1", IssuerID: "I1", Approved: true, Amount: 10_000, Currency: "840", Product: card.Credit}}
	e, iss, acq := newEngine(DefaultFees(), a, []string{"I1"}, []string{"A1"})
	e.Receive(NewFile(FileHeader{FileID: "f1", AcquirerID: "A1", Sequence: 1}, []Presentment{pres("t1", 10_000)}))
	e.Close(context.Background())
	sale, date, ok := e.ClearedSale("t1")
	if !ok || date != "2026-10-09" {
		t.Fatalf("cleared sale %v %s", ok, date)
	}

	net := func(c Cycle) map[string]int64 {
		m := map[string]int64{}
		for _, tr := range c.Transfers {
			if tr.From == NetworkID {
				m[tr.To] += tr.Amount
			} else {
				m[tr.From] -= tr.Amount
			}
		}
		return m
	}
	e.AddDispute(DisputeMoney{Kind: DisputeChargeback, DisputeID: "D1", Original: sale, Amount: 4_000})
	c := e.Close(context.Background())
	if n := net(c); n["I1"] != 4_000 || n["A1"] != -4_000 {
		t.Errorf("chargeback: %v", n)
	}
	if len(iss["I1"].files) != 1 {
		t.Errorf("issuer was sent dispute items: %d files", len(iss["I1"].files))
	}
	if adv := acq["A1"].advices[len(acq["A1"].advices)-1]; adv.Net != -4_000 || adv.Items[0].Dispute != DisputeChargeback {
		t.Errorf("acquirer advice %+v", adv)
	}

	e.AddDispute(DisputeMoney{Kind: DisputeRepresentment, DisputeID: "D1", Original: sale, Amount: 4_000})
	e.AddDispute(DisputeMoney{Kind: DisputeFee, DisputeID: "D1", Original: sale, Amount: 25_000, FeePayer: RoleIssuer})
	c = e.Close(context.Background())
	if n := net(c); n["A1"] != 4_000 || n["I1"] != -29_000 {
		t.Errorf("representment + issuer fee: %v", n)
	}
	if c.Checks[0].Revenue != 25_000 {
		t.Errorf("fee not network revenue: %+v", c.Checks)
	}
	if _, err := e.AddDispute(DisputeMoney{Kind: "bogus", Original: sale, Amount: 1}); err == nil {
		t.Error("unknown kind accepted")
	}
}
