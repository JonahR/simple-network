package main

import (
	"fmt"

	"github.com/JonahR/simple-network/internal/card"
	"github.com/JonahR/simple-network/internal/iso8583"
)

// refundable is what a sale can still give back: what it charged (the
// captured amount for a pre-auth or tipped sale, else the approved amount),
// less refunds already approved. The caller holds s.mu.
func refundable(sale *Transaction) int64 {
	if sale.Response == nil {
		return 0
	}
	charged := sale.Response.Amount
	if sale.Captured > 0 {
		charged = sale.Captured
	}
	return charged - sale.Refunded
}

// networkTxnOf is the network transaction ID of the sale with a STAN.
func (s *server) networkTxnOf(stan string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sale := s.findTx(stan); sale != nil && sale.Response != nil {
		return sale.Response.NetworkTxnID
	}
	return ""
}

// checkRefund checks a refund linked to an earlier sale (its STAN): the sale
// must be an approved card sale that wasn't voided, the refund must go to the
// same card, and the total refunded can't exceed what the sale charged. It
// returns the form field to flag and a message, or "" when the refund is fine.
//
// The card is compared by its masked number, which the terminal keeps for
// every sale (it never keeps full card numbers), so the check also works
// after a restart.
func (s *server) checkRefund(saleSTAN string, req iso8583.AuthRequest) (field, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sale := s.findTx(saleSTAN)
	switch {
	case sale == nil:
		return "form", "no sale with STAN " + saleSTAN + " on this terminal"
	case sale.Tender != "card" || iso8583.IsRefund(sale.Request.ProcessingCode) || iso8583.IsBalanceInquiry(sale.Request.ProcessingCode):
		return "form", "only a card sale can be refunded"
	case sale.Status != "APPROVED" && sale.Status != "PARTIAL":
		return "form", fmt.Sprintf("sale STAN %s is %s, so there is nothing to refund", saleSTAN, sale.Status)
	case sale.Request.AuthType == iso8583.AuthEstimated && sale.Captured == 0:
		return "form", "this pre-auth was never captured, so nothing was charged: void it instead"
	case sale.Request.TerminalID != req.TerminalID:
		return "form", "refund the sale at the merchant that made it"
	case card.Mask(req.PAN) != sale.Request.PAN:
		return "card_number", fmt.Sprintf("refunds go back to the card that paid: %s", sale.Request.PAN)
	case req.Currency != sale.Request.Currency:
		return "currency", "a refund is in the sale's currency"
	}
	if left := refundable(sale); req.Amount > left {
		return "amount", fmt.Sprintf("at most %s can still be refunded on this sale", cents(left))
	}
	return "", ""
}
