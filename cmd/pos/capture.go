package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/JonahR/simple-network/internal/card"
	"github.com/JonahR/simple-network/internal/iso8583"
)

// Pre-authorization and capture. A pre-auth (an estimated authorization)
// holds an estimate, such as a bar tab or a hotel stay. The merchant can ask
// for more with incremental authorizations, then captures the final amount
// with an 0220 completion advice to the acquirer. A tip on an ordinary sale
// is the same capture: the final amount is the sale plus the tip.

// authorizedTotal is what the issuer approved for tx, increments included.
// The caller holds s.mu.
func (s *server) authorizedTotal(tx *Transaction) int64 {
	if tx.Response == nil {
		return 0
	}
	total := tx.Response.Amount
	for _, h := range s.history {
		if h.IncrementOf == tx.Request.STAN && h.Response != nil && iso8583.IsApproved(h.Response.ResponseCode) && h.Reversal == nil {
			total += h.Response.Amount
		}
	}
	return total
}

// capturable reports why tx can't take an increment or a capture, or "".
// The caller holds s.mu.
func capturable(tx *Transaction) string {
	switch {
	case tx == nil:
		return "No sale with that STAN on this terminal."
	case tx.Tender != "card" || tx.Response == nil || (tx.Status != "APPROVED" && tx.Status != "PARTIAL"):
		return "Only an approved card sale can be adjusted."
	case tx.Reversal != nil:
		return "The sale was voided or reversed."
	case tx.IncrementOf != "":
		return "Adjust the original pre-auth, not an increment."
	case iso8583.IsBalanceInquiry(tx.Request.ProcessingCode):
		return "A balance inquiry has nothing to capture."
	case tx.Captured > 0:
		return "Already captured: the final amount is set."
	}
	return ""
}

// handleIncrement sends an incremental authorization: an 0100 that adds to
// an open pre-auth's hold, naming it by its network transaction ID.
func (s *server) handleIncrement(w http.ResponseWriter, r *http.Request) {
	var in struct {
		STAN   string `json:"stan"`
		Amount string `json:"amount"`
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
	orig := s.findTx(in.STAN)
	problem := capturable(orig)
	if problem == "" && orig.Request.AuthType != iso8583.AuthEstimated {
		problem = "Only a pre-auth can be increased; a final sale's amount is fixed except for a tip."
	}
	var req iso8583.AuthRequest
	if problem == "" {
		req = orig.sent
	}
	s.mu.Unlock()
	if problem != "" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": problem})
		return
	}

	// Merchant-initiated: no card read, so no cryptogram, PIN, CVV or 3DS.
	now := time.Now()
	loc := time.UTC
	if l, ok := s.lane(req.TerminalID); ok && l.loc != nil {
		loc = l.loc // DE12/DE13 are the merchant's local time
	}
	stan, utc, local := s.nextSTAN(), now.UTC(), now.In(loc)
	req.STAN = stan
	req.TransmissionTime = utc.Format("0102150405")
	req.LocalTime, req.LocalDate = local.Format("150405"), local.Format("0102")
	req.RRN = fmt.Sprintf("%s%03d%02d%s", utc.Format("2006")[3:], utc.YearDay(), utc.Hour(), stan)
	req.Amount, req.AdditionalAmounts = amount, nil
	req.AuthType, req.OriginalTxnID = iso8583.AuthIncremental, orig.Response.NetworkTxnID
	req.AVS, req.ECI, req.CAVV = nil, "", ""

	tx := &Transaction{Request: req.Redacted(card.Mask), Created: now, sent: forReversal(req), Tender: "card", IncrementOf: in.STAN}
	resp, err := s.acquirer.authorize(r.Context(), req)
	if err != nil {
		tx.Status, tx.Message = "NO_RESPONSE", "No response from the acquirer: "+err.Error()
	} else {
		redacted := resp.Redacted(card.Mask)
		tx.Response = &redacted
		tx.Status, tx.Message = outcome(req, resp)
		if iso8583.IsApproved(resp.ResponseCode) {
			tx.Message = fmt.Sprintf("Added %s to the pre-auth of STAN %s. Auth code %s.", cents(resp.Amount), in.STAN, resp.AuthCode)
		}
	}
	s.mu.Lock()
	s.history = append(s.history, tx)
	s.mu.Unlock()
	if tx.Status == "NO_RESPONSE" {
		s.startReversal(r.Context(), tx, iso8583.RCLateResponse)
	}
	log.Printf("increment stan=%s of=%s amount=%d status=%s", stan, in.STAN, amount, tx.Status)
	writeJSON(w, http.StatusOK, s.snapshot(tx))
}

// handleCapture sends the final amount of a sale to the acquirer in an 0220:
// a pre-auth's final bill, or a sale plus tip.
func (s *server) handleCapture(w http.ResponseWriter, r *http.Request) {
	var in struct {
		STAN   string `json:"stan"`
		Amount string `json:"amount"` // The final amount, or
		Tip    string `json:"tip"`    // a tip added to what was authorized
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	s.mu.Lock()
	tx := s.findTx(in.STAN)
	problem := capturable(tx)
	var final, authorized int64
	var comp iso8583.Completion
	if problem == "" {
		authorized = s.authorizedTotal(tx)
		var err error
		final, err = finalAmount(in.Amount, in.Tip, authorized)
		if err != nil {
			problem = err.Error()
		}
		comp = iso8583.Completion{
			MTI: iso8583.MTICompletion, STAN: s.nextSTAN(), Amount: final, Currency: tx.Request.Currency,
			TerminalID: tx.Request.TerminalID, MerchantID: tx.Request.MerchantID, // The lane that made the sale
			OriginalData: &iso8583.OriginalData{MTI: iso8583.MTIAuthRequest, STAN: tx.Request.STAN, TransmissionTime: tx.Request.TransmissionTime},
		}
	}
	s.mu.Unlock()
	if problem != "" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": problem})
		return
	}

	resp, err := s.acquirer.capture(r.Context(), comp)
	switch {
	case err != nil:
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "The acquirer didn't take the capture: " + err.Error()})
		return
	case resp.ResponseCode != iso8583.RCApproved:
		writeJSON(w, http.StatusConflict, map[string]string{"error": "The acquirer refused the capture: " + resp.Message})
		return
	}
	s.mu.Lock()
	tx.Captured = final
	tx.Completion = &resp
	s.mu.Unlock()
	log.Printf("capture stan=%s authorized=%d final=%d", in.STAN, authorized, final)
	writeJSON(w, http.StatusOK, s.snapshot(tx))
}

// finalAmount is the amount to capture: the final amount given, or what was
// authorized plus the tip.
func finalAmount(amount, tip string, authorized int64) (int64, error) {
	amount, tip = strings.TrimSpace(amount), strings.TrimSpace(tip)
	switch {
	case amount != "" && tip != "":
		return 0, errors.New("send a final amount or a tip, not both")
	case tip != "":
		t, err := parseAmount(tip)
		if err != nil {
			return 0, fmt.Errorf("tip: %v", err)
		}
		return authorized + t, nil
	case amount != "":
		return parseAmount(amount)
	}
	return 0, errors.New("enter the final amount or a tip")
}

func (c *acquirerClient) capture(ctx context.Context, comp iso8583.Completion) (iso8583.CompletionResponse, error) {
	var resp iso8583.CompletionResponse
	return resp, c.post(ctx, "/capture", comp, &resp)
}
