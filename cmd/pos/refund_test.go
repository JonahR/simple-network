package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JonahR/simple-network/internal/iso8583"
)

// postErrors posts a sale that should be refused and returns its field errors.
func postErrors(t *testing.T, s *server, in SaleInput) map[string]string {
	t.Helper()
	b, _ := json.Marshal(in)
	rec := httptest.NewRecorder()
	s.handleSale(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(b))))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d, want 422: %s", rec.Code, rec.Body)
	}
	var out struct {
		Errors map[string]string `json:"errors"`
	}
	json.NewDecoder(rec.Body).Decode(&out)
	return out.Errors
}

func keyed(pan, amount string) SaleInput {
	in := sale(pan, iso8583.EntryManual)
	in.CVV, in.Amount = "123", amount
	return in
}

func TestRefundSendsProcessingCode20(t *testing.T) {
	s := newWiredServer(t, &stubAcquirer{})
	in := keyed(creditPAN, "7.50")
	in.Refund = true
	_, tx := post(t, s.handleSale, in)
	if tx.Status != "REFUND" || !strings.HasPrefix(tx.Request.ProcessingCode, "20") || tx.Request.Amount != 750 {
		t.Errorf("refund tx = status %s, DE3 %s, DE4 %d", tx.Status, tx.Request.ProcessingCode, tx.Request.Amount)
	}
	if !strings.Contains(tx.Message, "7.50") {
		t.Errorf("message %q should name the amount refunded", tx.Message)
	}
}

func TestLinkedRefundsAreCappedAtTheSale(t *testing.T) {
	s := newWiredServer(t, &stubAcquirer{})
	_, sale := post(t, s.handleSale, keyed(creditPAN, "20.00"))
	stan := sale.Request.STAN

	part := keyed(creditPAN, "12.00")
	part.Refund, part.RefundOf = true, stan
	if _, tx := post(t, s.handleSale, part); tx.Status != "REFUND" || tx.RefundOf != stan {
		t.Fatalf("first refund: %+v", tx)
	}
	s.mu.Lock()
	refunded := s.findTx(stan).Refunded
	s.mu.Unlock()
	if refunded != 1200 {
		t.Errorf("sale shows %d refunded, want 1200", refunded)
	}

	tooMuch := keyed(creditPAN, "8.01")
	tooMuch.Refund, tooMuch.RefundOf = true, stan
	if errs := postErrors(t, s, tooMuch); !strings.Contains(errs["amount"], "8.00") {
		t.Errorf("errors %v; want the 8.00 still refundable named", errs)
	}

	rest := keyed(creditPAN, "8.00")
	rest.Refund, rest.RefundOf = true, stan
	if _, tx := post(t, s.handleSale, rest); tx.Status != "REFUND" {
		t.Errorf("refunding the rest: %+v", tx)
	}
}

func TestLinkedRefundGoesToTheSameCard(t *testing.T) {
	s := newWiredServer(t, &stubAcquirer{})
	_, sale := post(t, s.handleSale, keyed(creditPAN, "20.00"))
	other := keyed("5555555555554444", "5.00")
	other.Expiry = "08/32"
	other.Refund, other.RefundOf = true, sale.Request.STAN
	if errs := postErrors(t, s, other); !strings.Contains(errs["card_number"], "424242") {
		t.Errorf("errors %v; want the original card named", errs)
	}
}

func TestRefundRules(t *testing.T) {
	s := newWiredServer(t, &stubAcquirer{})
	_, sale := post(t, s.handleSale, keyed(creditPAN, "20.00"))

	linkedButCharged := keyed(creditPAN, "5.00")
	linkedButCharged.RefundOf = sale.Request.STAN // Charge pressed instead of Refund
	if errs := postErrors(t, s, linkedButCharged); !strings.Contains(errs["form"], "Refund") {
		t.Errorf("charge with a refund link: %v", errs)
	}

	withCashback := keyed(creditPAN, "5.00")
	withCashback.Refund, withCashback.Cashback = true, "10.00"
	if errs := postErrors(t, s, withCashback); errs["form"] == "" {
		t.Error("a refund with cash back must be refused")
	}

	unknown := keyed(creditPAN, "5.00")
	unknown.Refund, unknown.RefundOf = true, "999999"
	if errs := postErrors(t, s, unknown); !strings.Contains(errs["form"], "999999") {
		t.Errorf("refund of an unknown sale: %v", errs)
	}
}

func TestRefundsSurviveARestart(t *testing.T) {
	s := newWiredServer(t, &stubAcquirer{})
	_, sale := post(t, s.handleSale, keyed(creditPAN, "20.00"))
	r := keyed(creditPAN, "5.00")
	r.Refund, r.RefundOf = true, sale.Request.STAN
	post(t, s.handleSale, r)

	// Save and restore, as a redeploy does: card numbers come back masked.
	b, _ := json.Marshal(s.saveState())
	var st posState
	json.Unmarshal(b, &st)
	s2 := newWiredServer(t, &stubAcquirer{})
	s2.restoreState(st)

	again := keyed(creditPAN, "15.01")
	again.Refund, again.RefundOf = true, sale.Request.STAN
	if errs := postErrors(t, s2, again); !strings.Contains(errs["amount"], "15.00") {
		t.Errorf("after a restart: %v; want 15.00 still refundable", errs)
	}
	ok := keyed(creditPAN, "15.00")
	ok.Refund, ok.RefundOf = true, sale.Request.STAN
	if _, tx := post(t, s2.handleSale, ok); tx.Status != "REFUND" {
		t.Errorf("refund after a restart: %+v", tx)
	}
}
