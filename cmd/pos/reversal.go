package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/JonahR/simple-network/internal/card"
	"github.com/JonahR/simple-network/internal/iso8583"
)

// maxReversalAttempts bounds how long a terminal keeps trying to deliver an
// 0420 before it asks the cashier to call the acquirer.
const maxReversalAttempts = 10

// Reversal is the terminal's 0420 reversal advice to undo a sale: a void the
// cashier asked for (reason 17, customer cancellation), or one the terminal
// sends on its own because no 0110 arrived (reason 68, response too late).
type Reversal struct {
	Reason   string                    `json:"reason"` // DE39 of the 0420: 17 or 68
	Status   string                    `json:"status"` // PENDING, ACCEPTED, or FAILED
	Message  string                    `json:"message"`
	Attempts int                       `json:"attempts"`
	Advice   iso8583.ReversalAdvice    `json:"advice"`        // As sent, PAN masked
	Ack      *iso8583.ReversalResponse `json:"ack,omitempty"` // The acquirer's 0430
}

// forReversal keeps what a reversal needs from the 0100 and drops the rest.
func forReversal(req iso8583.AuthRequest) iso8583.AuthRequest {
	req.PINData, req.CVV2, req.Track2, req.ARQC = "", "", "", ""
	return req
}

// handleVoid voids an approved sale, identified by its terminal STAN.
func (s *server) handleVoid(w http.ResponseWriter, r *http.Request) {
	var in struct {
		STAN string `json:"stan"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	s.mu.Lock()
	var tx *Transaction
	for i := len(s.history) - 1; i >= 0; i-- {
		if s.history[i].Request.STAN == in.STAN {
			tx = s.history[i]
			break
		}
	}
	var problem string
	switch {
	case tx == nil:
		problem = "No sale with that STAN on this terminal."
	case tx.Reversal != nil:
		problem = "This sale is already being reversed."
	case tx.Status != "APPROVED" && tx.Status != "PARTIAL":
		problem = "Only approved sales can be voided."
	}
	s.mu.Unlock()
	if problem != "" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": problem})
		return
	}
	s.startReversal(r.Context(), tx, iso8583.RCCustomerCancel)
	writeJSON(w, http.StatusOK, s.snapshot(tx))
}

// startReversal sends the 0420 for tx now and, if the acquirer doesn't
// acknowledge it, keeps resending the same advice in the background: a
// reversal must not be forgotten, or the cardholder's funds stay held.
func (s *server) startReversal(ctx context.Context, tx *Transaction, reason string) {
	adv := s.adviceFor(tx, reason, time.Now())
	s.mu.Lock()
	tx.Reversal = &Reversal{Reason: reason, Status: "PENDING", Advice: adv.Redacted(card.Mask), Message: "Sending reversal…"}
	s.mu.Unlock()

	if s.tryReversal(ctx, tx, adv) {
		return
	}
	go func() {
		for {
			time.Sleep(s.reversalRetry)
			if s.tryReversal(context.Background(), tx, adv) {
				return
			}
		}
	}()
}

// tryReversal sends the 0420 once and records the outcome. It returns true
// when there is nothing left to do: acknowledged, refused, or out of attempts.
func (s *server) tryReversal(ctx context.Context, tx *Transaction, adv iso8583.ReversalAdvice) bool {
	ctx, cancel := context.WithTimeout(ctx, posTimeout)
	ack, err := s.acquirer.reverse(ctx, adv)
	cancel()

	s.mu.Lock()
	defer s.mu.Unlock()
	rev := tx.Reversal
	rev.Attempts++
	what := "Void"
	if rev.Reason != iso8583.RCCustomerCancel {
		what = "Timeout reversal"
	}
	var refused *httpError
	switch {
	case err == nil && ack.ResponseCode == iso8583.RCApproved:
		rev.Ack = &ack
		rev.Status = "ACCEPTED"
		if ack.Matched {
			rev.Message = what + " acknowledged: the acquirer will reverse the sale at the network, releasing any hold on the card."
		} else {
			rev.Message = what + " acknowledged: the sale never reached the network, so nothing was held."
		}
		if rev.Reason == iso8583.RCCustomerCancel {
			tx.Status, tx.Message = "VOIDED", "Voided. The hold on the card is being released."
		}
		log.Printf("reversal stan=%s orig_stan=%s reason=%s matched=%v attempts=%d", adv.STAN, adv.OriginalData.STAN, rev.Reason, ack.Matched, rev.Attempts)
		return true
	case errors.As(err, &refused) && refused.code < 500:
		// The acquirer refused the advice itself; resending it can't help.
		rev.Status = "FAILED"
		rev.Message = fmt.Sprintf("%s refused by the acquirer: %v. Call the acquirer to release any hold.", what, err)
		return true
	}
	if rev.Attempts >= maxReversalAttempts {
		rev.Status = "FAILED"
		rev.Message = fmt.Sprintf("%s not acknowledged after %d attempts. Call the acquirer to release any hold.", what, rev.Attempts)
		return true
	}
	reason := fmt.Sprintf("0430 had response code %q", ack.ResponseCode)
	if err != nil {
		reason = err.Error()
	}
	rev.Message = fmt.Sprintf("%s not acknowledged yet (%s). Resending the same 0420 every %s (attempt %d of %d).",
		what, reason, s.reversalRetry, rev.Attempts, maxReversalAttempts)
	return false
}

// adviceFor builds the 0420 that undoes tx. DE90 names the original by the
// terminal's own STAN and transmission time; the acquirer translates them.
func (s *server) adviceFor(tx *Transaction, reason string, now time.Time) iso8583.ReversalAdvice {
	s.mu.Lock()
	defer s.mu.Unlock()
	o := tx.sent
	amount := o.Amount
	var txnID string
	if tx.Response != nil {
		txnID = tx.Response.NetworkTxnID
		if tx.Response.Amount > 0 {
			amount = tx.Response.Amount // A partial approval holds less than was asked
		}
	}
	return iso8583.ReversalAdvice{
		MTI:              iso8583.MTIReversalAdvice,
		NetworkTxnID:     txnID,
		PAN:              o.PAN,
		Amount:           amount,
		TransmissionTime: now.UTC().Format("0102150405"),
		STAN:             s.nextSTAN(),
		RRN:              o.RRN,
		ResponseCode:     reason,
		TerminalID:       o.TerminalID,
		MerchantID:       o.MerchantID,
		OriginalData:     &iso8583.OriginalData{MTI: o.MTI, STAN: o.STAN, TransmissionTime: o.TransmissionTime},
	}
}
