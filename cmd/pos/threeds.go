package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/JonahR/simple-network/internal/card"
	"github.com/JonahR/simple-network/internal/iso8583"
)

// challengeTimeout is how long a shopper has to enter a 3-D Secure code.
const challengeTimeout = 5 * time.Minute

// pending3DS is a sale held while the shopper answers a 3-D Secure challenge.
type pending3DS struct {
	in      SaleInput
	req     iso8583.AuthRequest
	created time.Time
}

// checkCardNotPresent validates the billing address (AVS) and the 3-D Secure
// request. Both only make sense when no card is in front of the merchant:
// keyed and online sales. Wallets authenticate the shopper on the phone.
func checkCardNotPresent(in SaleInput, inquiry bool, errs fieldErrors) *iso8583.AVSData {
	cnp := (in.EntryMode == iso8583.EntryManual || in.EntryMode == iso8583.EntryEcommerce) && in.Wallet == ""
	if in.ThreeDS {
		switch {
		case in.EntryMode != iso8583.EntryEcommerce || in.Wallet != "":
			errs["three_ds"] = "3-D Secure is for online card payments; wallets carry their own cryptogram"
		case inquiry:
			errs["three_ds"] = "a balance inquiry doesn't need 3-D Secure"
		}
	}
	a := iso8583.AVSData{Zip: strings.TrimSpace(in.BillingZip), Street: strings.TrimSpace(in.BillingStreet)}
	if a == (iso8583.AVSData{}) {
		return nil
	}
	if !cnp {
		errs["billing_zip"] = "the billing address is only checked when the card isn't present (keyed or online)"
		return nil
	}
	if a.Zip != "" && !validZip(a.Zip) {
		errs["billing_zip"] = "ZIP code must be 5 digits, or ZIP+4 like 94105-1234"
	}
	if len(a.Street) > 40 {
		errs["billing_street"] = "street address is limited to 40 characters"
	}
	return &a
}

func validZip(z string) bool {
	five, four, plus4 := strings.Cut(z, "-")
	return isDigits(five, 5, 5) && (!plus4 || isDigits(four, 4, 4))
}

// defaultECI is the e-commerce indicator for an online card sale before any
// 3-D Secure authentication: 07, not authenticated.
func defaultECI(in SaleInput) string {
	if in.EntryMode == iso8583.EntryEcommerce && in.Wallet == "" {
		return "07"
	}
	return ""
}

// startThreeDS asks the issuer, through the acquirer's 3DS server and the
// network's directory server, to authenticate the shopper. A frictionless
// result goes straight to authorization; a challenge waits for the code.
func (s *server) startThreeDS(w http.ResponseWriter, r *http.Request, in SaleInput, req iso8583.AuthRequest) {
	l, _ := s.lane(req.TerminalID) // The picked merchant, shown to the shopper in the challenge
	res, err := s.acquirer.authenticate3DS(r.Context(), iso8583.ThreeDSRequest{
		PAN: req.PAN, Expiry: req.Expiry, Amount: req.Amount, Currency: req.Currency,
		MerchantID: req.MerchantID, MerchantName: l.MerchantName,
	})
	if err != nil {
		res = iso8583.ThreeDSResult{Status: "U", Reason: err.Error()}
	}
	log.Printf("3ds authenticate pan=%s amount=%d status=%s", card.Mask(req.PAN), req.Amount, res.Status)
	writeJSON(w, http.StatusOK, s.afterThreeDS(r.Context(), in, req, res))
}

// handleChallenge takes the shopper's one-time code for a held sale.
func (s *server) handleChallenge(w http.ResponseWriter, r *http.Request) {
	var c iso8583.ThreeDSChallenge
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&c); err != nil || c.ID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"errors": map[string]string{"form": "invalid request body"}})
		return
	}
	s.mu.Lock()
	p := s.challenges[c.ID]
	delete(s.challenges, c.ID)
	s.mu.Unlock()
	if p == nil || time.Since(p.created) > challengeTimeout {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"errors": map[string]string{"otp": "this checkout expired: start the sale again"}})
		return
	}
	res, err := s.acquirer.challenge3DS(r.Context(), c)
	if err != nil {
		res = iso8583.ThreeDSResult{ID: c.ID, Status: "U", Reason: err.Error()}
	}
	log.Printf("3ds challenge id=%s status=%s", c.ID, res.Status)
	writeJSON(w, http.StatusOK, s.afterThreeDS(r.Context(), p.in, p.req, res))
}

// afterThreeDS acts on an ACS answer the way a merchant's checkout does.
func (s *server) afterThreeDS(ctx context.Context, in SaleInput, req iso8583.AuthRequest, res iso8583.ThreeDSResult) Transaction {
	result := res
	switch res.Status {
	case "Y":
		// Authenticated: the CAVV proves it, and liability for fraud shifts
		// to the issuer.
		req.ECI, req.CAVV = res.ECI, res.CAVV
		return s.completeSale(ctx, in, req, &result)
	case "C":
		s.mu.Lock()
		if s.challenges == nil {
			s.challenges = map[string]*pending3DS{}
		}
		s.challenges[res.ID] = &pending3DS{in: in, req: req, created: time.Now()}
		s.mu.Unlock()
		return Transaction{Status: "CHALLENGE", Message: res.Challenge, Request: req.Redacted(card.Mask), Created: time.Now(), ThreeDS: &result}
	case "N":
		// Failed authentication: a merchant doesn't send the sale, since the
		// shopper couldn't prove they hold the card.
		tx := &Transaction{Status: "NOT_AUTHENTICATED", Message: "3-D Secure failed: " + res.Reason + ". The sale was not sent for authorization.",
			Request: req.Redacted(card.Mask), Created: time.Now(), Tender: "card", ThreeDS: &result}
		s.mu.Lock()
		s.history = append(s.history, tx)
		s.mu.Unlock()
		return s.snapshot(tx)
	default:
		// Couldn't authenticate (no ACS, or it didn't answer): the merchant
		// may still send the sale unauthenticated, keeping fraud liability.
		req.ECI = "07"
		return s.completeSale(ctx, in, req, &result)
	}
}
