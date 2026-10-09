package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/JonahR/simple-network/internal/iso8583"
)

// Split tender: when the issuer approves only part of a sale (DE39 10, for
// example a prepaid card without enough on it), the rest is still owed. The
// cashier collects it with another card or in cash; each tender names the
// first one, and the first one's balance due falls until it is paid in full.

// findTx returns the newest transaction with a terminal STAN. The caller holds s.mu.
func (s *server) findTx(stan string) *Transaction {
	for i := len(s.history) - 1; i >= 0; i-- {
		if s.history[i].Request.STAN == stan {
			return s.history[i]
		}
	}
	return nil
}

// checkSplit validates a tender toward the balance on sale stan, and returns
// a message for the cashier if it can't be taken.
func (s *server) checkSplit(stan string, amount int64, currency string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	first := s.findTx(stan)
	switch {
	case first == nil || first.BalanceDue <= 0:
		return "that sale has no balance due"
	case first.Reversal != nil:
		return "that sale was voided or reversed"
	case currency != first.Request.Currency:
		return "pay the balance in the sale's currency"
	case amount > first.BalanceDue:
		return fmt.Sprintf("the balance due is only %s", cents(first.BalanceDue))
	}
	return ""
}

// applyTender records what tx collected: a partial approval opens a balance
// due, and a tender toward an earlier sale reduces that sale's balance. The
// caller holds s.mu.
func (s *server) applyTender(tx *Transaction, requested int64) {
	collected := int64(0)
	switch {
	case tx.Tender == "cash":
		collected = requested
	case tx.Response != nil && (tx.Status == "APPROVED" || tx.Status == "PARTIAL"):
		collected = tx.Response.Amount
	}
	if tx.SplitOf == "" {
		if tx.Status == "PARTIAL" {
			tx.BalanceDue = requested - collected
		}
		return
	}
	first := s.findTx(tx.SplitOf)
	if first == nil {
		return
	}
	first.BalanceDue = max(first.BalanceDue-collected, 0)
	if first.BalanceDue == 0 {
		first.Message = "Paid in full with split tender."
		tx.Message += " The sale is now paid in full."
	} else {
		tx.Message += fmt.Sprintf(" Still due on the sale: %s.", cents(first.BalanceDue))
	}
}

// handleCash takes a cash tender toward the balance due on a partially
// approved sale. Cash never touches the card network.
func (s *server) handleCash(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SplitOf string `json:"split_of"`
		Amount  string `json:"amount"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	amount, err := parseAmount(in.Amount)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	s.mu.Lock()
	first := s.findTx(in.SplitOf)
	s.mu.Unlock()
	if first == nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "that sale has no balance due"})
		return
	}
	if msg := s.checkSplit(in.SplitOf, amount, first.Request.Currency); msg != "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": msg})
		return
	}

	now := time.Now()
	local := now.In(s.location)
	tx := &Transaction{
		Status:  "CASH",
		Message: fmt.Sprintf("%s cash collected.", cents(amount)),
		Tender:  "cash",
		SplitOf: in.SplitOf,
		Created: now,
		Request: iso8583.AuthRequest{
			Amount: amount, Currency: first.Request.Currency, STAN: s.nextSTAN(),
			LocalTime: local.Format("150405"), LocalDate: local.Format("0102"),
			TerminalID: s.terminal.TerminalID, MerchantID: s.terminal.MerchantID,
		},
	}
	s.mu.Lock()
	s.history = append(s.history, tx)
	s.applyTender(tx, amount)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, s.snapshot(tx))
}

// cents formats minor units as dollars and cents for cashier messages.
func cents(minor int64) string { return fmt.Sprintf("$%d.%02d", minor/100, minor%100) }
