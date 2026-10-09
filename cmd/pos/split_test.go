package main

import (
	"net/http"
	"testing"

	"github.com/JonahR/simple-network/internal/iso8583"
)

func TestSplitTenderCardThenCash(t *testing.T) {
	acq := &stubAcquirer{limits: map[string]int64{prepaidPAN: 600}}
	s := newWiredServer(t, acq)

	// $10.00 on a gift card with $6.00 left: partial approval, $4.00 due.
	gift := sale(prepaidPAN, iso8583.EntryManual)
	gift.CVV = "123"
	_, first := post(t, s.handleSale, gift)
	if first.Status != "PARTIAL" || first.BalanceDue != 400 {
		t.Fatalf("first tender: %s, balance due %d", first.Status, first.BalanceDue)
	}

	// More than the balance is refused.
	tooMuch := sale(creditPAN, iso8583.EntryManual)
	tooMuch.CVV, tooMuch.Amount, tooMuch.SplitOf = "123", "5.00", first.Request.STAN
	if code, _ := post(t, s.handleSale, tooMuch); code != http.StatusUnprocessableEntity {
		t.Errorf("tender over the balance: HTTP %d", code)
	}

	// $2.50 on a credit card toward the balance.
	second := sale(creditPAN, iso8583.EntryManual)
	second.CVV, second.Amount, second.SplitOf = "123", "2.50", first.Request.STAN
	_, tx2 := post(t, s.handleSale, second)
	if tx2.Status != "APPROVED" || tx2.SplitOf != first.Request.STAN || tx2.Tender != "card" {
		t.Fatalf("second tender: %+v", tx2)
	}
	if got := s.snapshot(s.findTx(first.Request.STAN)); got.BalanceDue != 150 {
		t.Errorf("balance after card tender = %d, want 150", got.BalanceDue)
	}

	// The last $1.50 in cash, which never touches the network.
	code, cash := post(t, s.handleCash, map[string]string{"split_of": first.Request.STAN, "amount": "1.50"})
	if code != 200 || cash.Status != "CASH" || cash.Tender != "cash" || cash.Request.Amount != 150 {
		t.Fatalf("cash tender: %d %+v", code, cash)
	}
	got := s.snapshot(s.findTx(first.Request.STAN))
	if got.BalanceDue != 0 || got.Message != "Paid in full with split tender." {
		t.Errorf("after cash: balance %d, message %q", got.BalanceDue, got.Message)
	}

	// Nothing is left to collect.
	if code, _ := post(t, s.handleCash, map[string]string{"split_of": first.Request.STAN, "amount": "0.01"}); code != http.StatusUnprocessableEntity {
		t.Errorf("cash after paid in full: HTTP %d", code)
	}
}
