package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strings"

	"github.com/JonahR/simple-network/internal/dispute"
	"github.com/JonahR/simple-network/internal/jsonapi"
)

// The cardholder's online banking: what a customer of this bank sees when
// they sign in. It shows the same accounts as the back office, from the
// customer's side: limit, balance, pending and posted transactions, and a
// switch to lock the card. The page is at /banking/.

func (s *bankServer) handleCustomers(w http.ResponseWriter, _ *http.Request) {
	banks := make([]bankLink, len(s.banks))
	url := map[string]string{} // Bank ID -> its online banking
	for i, b := range s.banks {
		banks[i] = b
		banks[i].URL = strings.TrimSuffix(b.URL, "/") + "/banking/"
		url[b.ID] = banks[i].URL
	}
	// directory says which bank's site each customer signs in to. It stands
	// in for the customer simply knowing who their bank is.
	directory := map[string]string{}
	for _, p := range s.peers {
		for _, c := range p.bank.Customers() {
			if _, ok := directory[c.Holder]; !ok {
				directory[c.Holder] = url[p.bank.ID]
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"bank":      map[string]string{"id": s.bank.ID, "name": s.bank.Name},
		"banks":     banks,
		"customers": s.bank.Customers(),
		"directory": directory,
		"claims":    claims,
	})
}

func (s *bankServer) handleStatement(w http.ResponseWriter, r *http.Request) {
	st, ok := s.bank.Statement(r.PathValue("id"))
	if !ok {
		http.Error(w, "account not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// claim is what a cardholder can say went wrong with a charge, in their
// words. The bank turns it into a network reason code: the first of Reasons
// that the network says applies to the sale (a stolen card number is 10.4
// for a keyed or online sale, 10.1 for a swiped counterfeit).
type claim struct {
	ID        string   `json:"id"`
	Label     string   `json:"label"`
	Hint      string   `json:"hint"`
	AskAmount bool     `json:"ask_amount,omitempty"` // The cardholder says what they agreed to pay
	Reasons   []string `json:"reasons"`
}

var claims = []claim{
	{"not_me", "I don't recognize this charge", "Someone else used your card or its number.", false, []string{"10.4", "10.1"}},
	{"duplicate", "I was charged twice", "The same purchase appears more than once.", false, []string{"12.6"}},
	{"wrong_amount", "I was charged the wrong amount", "You agreed to pay less than this.", true, []string{"12.5"}},
	{"not_received", "I never got what I paid for", "The goods didn't arrive or the service didn't happen.", false, []string{"13.1"}},
	{"not_as_described", "It wasn't as described", "What arrived was broken or not what the merchant described.", false, []string{"13.3"}},
	{"cancelled", "I cancelled this subscription", "The merchant charged again after you cancelled.", false, []string{"13.2"}},
}

// handleDisputeCharge is "Dispute this charge" in online banking: the bank
// files a chargeback for the cardholder through the network. The network's
// notice back gives the cardholder a provisional credit right away.
func (s *bankServer) handleDisputeCharge(w http.ResponseWriter, r *http.Request) {
	if !jsonapi.Require(w, r) {
		return
	}
	var in struct {
		NetworkTxnID string `json:"network_txn_id"`
		Claim        string `json:"claim"`
		Agreed       int64  `json:"agreed"` // Minor units, for wrong_amount
		Story        string `json:"story"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&in); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	refuse := func(msg string) { writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": msg}) }
	i := slices.IndexFunc(claims, func(c claim) bool { return c.ID == in.Claim })
	if i < 0 {
		refuse("Tell us what went wrong with this charge.")
		return
	}
	c := claims[i]
	charge, ok := s.bank.DisputableCharge(r.PathValue("id"), in.NetworkTxnID)
	if !ok {
		refuse("This charge can't be disputed: it isn't a posted purchase on this card, or it's already disputed.")
		return
	}
	amount := charge.Amount
	if c.AskAmount {
		if in.Agreed <= 0 || in.Agreed >= charge.Amount {
			refuse("Enter the amount you agreed to pay. It must be less than the charge.")
			return
		}
		amount = charge.Amount - in.Agreed
	}
	story := strings.TrimSpace(in.Story)
	if len(story) > 280 {
		story = story[:280]
	}
	note := "Cardholder: " + c.Label + "."
	if story != "" {
		note += " " + story
	}

	refusal := ""
	for _, code := range c.Reasons {
		why, err := s.disputes.Eligible(r.Context(), charge.NetworkTxnID, code, amount)
		if err != nil {
			log.Printf("%s dispute eligibility: %v", s.bank.ID, err)
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "The bank can't reach the card network right now. Try again in a moment."})
			return
		}
		if why != "" {
			if refusal == "" {
				refusal = why // The first reason code is the one that usually fits
			}
			continue
		}
		d, err := s.disputes.File(r.Context(), charge.NetworkTxnID, code, amount, note)
		var refused *dispute.RefusedError
		switch {
		case errors.As(err, &refused):
			refuse("The card network refused the dispute: " + refused.Reason + ".")
			return
		case err != nil:
			log.Printf("%s dispute filing: %v", s.bank.ID, err)
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "The bank can't reach the card network right now. Try again in a moment."})
			return
		}
		log.Printf("%s cardholder disputed txn=%s claim=%s reason=%s dispute=%s", s.bank.ID, charge.NetworkTxnID, c.ID, code, d.ID)
		writeJSON(w, http.StatusOK, map[string]any{"dispute_id": d.ID, "reason": code, "amount": amount, "deadline": d.Deadline})
		return
	}
	refuse(fmt.Sprintf("This charge doesn't qualify for a dispute with the merchant: %s.", refusal))
}
