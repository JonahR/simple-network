package main

import (
	"errors"
	"fmt"

	"github.com/JonahR/simple-network/internal/clearing"
	"github.com/JonahR/simple-network/internal/iso8583"
)

// authorizedTotal is what the network approved for an authorization: its own
// amount plus every approved, unreversed incremental authorization that
// names it. The caller holds a.mu.
func (a *acquirer) authorizedTotal(rec *Record) int64 {
	total := rec.Response.Amount
	id := rec.Response.NetworkTxnID
	for _, r := range a.records {
		if r.Request.AuthType == iso8583.AuthIncremental && r.Request.OriginalTxnID == id &&
			iso8583.IsApproved(r.Response.ResponseCode) && (r.Reversal == "" || r.Reversal == ReversalRejected) {
			total += r.Response.Amount
		}
	}
	return total
}

// findTerminalRecord finds the authorization a terminal names by its own
// STAN and transmission time. The caller holds a.mu.
func (a *acquirer) findTerminalRecord(merchantID, terminalID string, o *iso8583.OriginalData) *Record {
	for i := len(a.records) - 1; i >= 0; i-- {
		r := a.records[i]
		if r.MerchantID == merchantID && r.TerminalID == terminalID && r.TerminalSTAN == o.STAN && r.Request.TransmissionTime == o.TransmissionTime {
			return r
		}
	}
	return nil
}

// Capture applies a terminal's 0220 completion: the final amount of an
// authorized sale. It may be below what was authorized (the rest of the hold
// is released at clearing) or above it by the tolerance for the merchant's
// MCC (a tip). More than that needs an incremental authorization first.
// presented reports whether clearing already sent the sale to the network.
func (a *acquirer) Capture(c iso8583.Completion, presented func(id string) bool) (iso8583.CompletionResponse, error) {
	switch {
	case c.MTI != iso8583.MTICompletion:
		return iso8583.CompletionResponse{}, fmt.Errorf("MTI %q is not 0220", c.MTI)
	case c.OriginalData == nil || c.OriginalData.STAN == "" || c.OriginalData.TransmissionTime == "":
		return iso8583.CompletionResponse{}, errors.New("DE90: original STAN and transmission time are required")
	case c.Amount <= 0:
		return iso8583.CompletionResponse{}, errors.New("DE4: the final amount must be positive")
	}
	resp := iso8583.CompletionResponse{MTI: iso8583.MTICompletionResp, STAN: c.STAN, ResponseCode: iso8583.RCInvalidTransaction}
	a.mu.Lock()
	defer a.mu.Unlock()
	rec := a.findTerminalRecord(c.MerchantID, c.TerminalID, c.OriginalData)
	if rec == nil {
		resp.Message = "No authorization from this terminal with that STAN and time"
		return resp, nil
	}
	resp.NetworkTxnID = rec.Response.NetworkTxnID
	resp.Authorized = a.authorizedTotal(rec)
	max := clearing.MaxCapture(resp.Authorized, rec.Request.MCC)
	switch {
	case !iso8583.IsApproved(rec.Response.ResponseCode) || iso8583.IsBalanceInquiry(rec.Request.ProcessingCode):
		resp.Message = "Only an approved sale can be captured"
	case rec.Reversal != "" && rec.Reversal != ReversalRejected:
		resp.Message = "The sale was reversed"
	case rec.Request.AuthType == iso8583.AuthIncremental:
		resp.Message = "Capture the original authorization; increments are cleared with it"
	case c.Currency != rec.Request.Currency:
		resp.Message = "Currency differs from the authorization"
	case rec.Batch != "":
		resp.Message = "Batch " + rec.Batch + " is closed; the sale can't change now"
	case presented(rec.Response.NetworkTxnID):
		resp.Message = "Already sent to the network in a clearing file; it can't change now"
	case c.Amount > max:
		resp.Message = fmt.Sprintf("%d is over the authorized %d plus the %d%% tolerance for MCC %s (at most %d): ask for an incremental authorization first",
			c.Amount, resp.Authorized, clearing.Tolerance(rec.Request.MCC)/100, rec.Request.MCC, max)
	default:
		rec.Captured = c.Amount
		resp.ResponseCode, resp.Captured = iso8583.RCApproved, c.Amount
		resp.Message = "Captured: clearing will present the final amount"
		rec.Steps = append(rec.Steps, Step{
			From: PartyPOS, To: PartyAcquirer, MTI: iso8583.MTICompletion, Title: "Completion advice",
			Detail: fmt.Sprintf("Terminal %s sets the final amount to %d (authorized %d, at most %d with the MCC %s tolerance). Clearing presents %d; the issuer releases the rest of the hold.",
				c.TerminalID, c.Amount, resp.Authorized, max, rec.Request.MCC, c.Amount),
		})
	}
	return resp, nil
}
