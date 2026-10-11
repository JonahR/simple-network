package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/JonahR/simple-network/internal/iso8583"
)

// Batch close. A terminal's batch is every sale and refund it got approved
// since it last closed. Closing seals it: tips and captures can no longer
// change it, and the acquirer sends it to clearing.

// maxBatches bounds the closed batches the acquirer keeps for its page.
const maxBatches = 500

// Batch is a closed (or refused) batch close, as the acquirer saw it.
type Batch struct {
	Closed         time.Time                      `json:"closed"`
	MerchantID     string                         `json:"merchant_id"`
	TerminalID     string                         `json:"terminal_id"`
	Request        iso8583.Reconciliation         `json:"request"`
	Response       iso8583.ReconciliationResponse `json:"response"`
	Count          int                            `json:"count"`
	TerminalTotals []iso8583.BatchTotals          `json:"terminal_totals"`
}

// inBatch reports whether a record belongs in its terminal's open batch:
// approved, still standing, and with a final amount. An open pre-auth waits
// for its capture, and an increment is counted with its pre-auth.
func inBatch(r *Record) bool {
	switch {
	case r.Batch != "", r.Status == "DECLINED", !iso8583.IsApproved(r.Response.ResponseCode):
		return false
	case iso8583.IsBalanceInquiry(r.Request.ProcessingCode):
		return false
	case r.Reversal != "" && r.Reversal != ReversalRejected:
		return false
	case r.Request.AuthType == iso8583.AuthIncremental:
		return false
	}
	return r.Request.AuthType != iso8583.AuthEstimated || r.Captured > 0
}

// openBatch lists a terminal's open batch, oldest first, and counts its
// open pre-auths. The caller holds a.mu.
func (a *acquirer) openBatch(merchantID, terminalID string) (recs []*Record, items []iso8583.BatchItem, held int) {
	for _, r := range a.records {
		if r.MerchantID != merchantID || r.TerminalID != terminalID {
			continue
		}
		if !inBatch(r) {
			if r.Batch == "" && r.Request.AuthType == iso8583.AuthEstimated && iso8583.IsApproved(r.Response.ResponseCode) &&
				(r.Reversal == "" || r.Reversal == ReversalRejected) {
				held++
			}
			continue
		}
		amount := r.Captured
		if amount == 0 {
			amount = a.authorizedTotal(r)
		}
		recs = append(recs, r)
		items = append(items, iso8583.BatchItem{STAN: r.TerminalSTAN, Refund: iso8583.IsRefund(r.Request.ProcessingCode),
			Amount: amount, Currency: r.Request.Currency})
	}
	return recs, items, held
}

// plural writes a count and a noun, adding an s unless the count is 1.
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// validBatchNumber reports whether n is a three-digit batch number, 001 to 999.
func validBatchNumber(n string) bool {
	v, err := strconv.Atoi(n)
	return err == nil && len(n) == 3 && v >= 1
}

// CloseBatch reconciles a terminal's 0500 with the acquirer's own records.
// If the totals agree, or the terminal accepts the acquirer's totals (960000)
// after a 95, the batch is closed and ids lists what goes to clearing. The
// same close sent again (the 0510 was lost) gets the same answer.
func (a *acquirer) CloseBatch(rq iso8583.Reconciliation) (resp iso8583.ReconciliationResponse, ids []string, err error) {
	switch {
	case rq.MTI != iso8583.MTIReconciliation:
		return resp, nil, fmt.Errorf("MTI %q is not 0500", rq.MTI)
	case rq.ProcessingCode != iso8583.ProcBatchClose && rq.ProcessingCode != iso8583.ProcBatchCloseForced:
		return resp, nil, fmt.Errorf("DE3 %q: use 920000 to close or 960000 to close with the acquirer's totals", rq.ProcessingCode)
	case !validBatchNumber(rq.BatchNumber):
		return resp, nil, errors.New("DE60: the batch number must be three digits, 001 to 999")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := len(a.batches) - 1; i >= 0; i-- {
		b := a.batches[i]
		if b.MerchantID == rq.MerchantID && b.TerminalID == rq.TerminalID {
			if b.Request.BatchNumber == rq.BatchNumber && b.Response.ResponseCode == iso8583.RCApproved {
				resp = b.Response
				resp.STAN = rq.STAN
				return resp, nil, nil
			}
			break
		}
	}

	recs, items, held := a.openBatch(rq.MerchantID, rq.TerminalID)
	host := iso8583.SumBatch(items)
	resp = iso8583.ReconciliationResponse{MTI: iso8583.MTIReconciliationResp, STAN: rq.STAN, BatchNumber: rq.BatchNumber,
		HostTotals: host, Held: held}
	b := Batch{Closed: a.now(), MerchantID: rq.MerchantID, TerminalID: rq.TerminalID, Request: rq, Count: len(items),
		TerminalTotals: rq.Totals}
	if rq.Totals == nil {
		b.TerminalTotals = []iso8583.BatchTotals{}
	}
	agree := iso8583.SameTotals(host, rq.Totals)
	if !agree && rq.ProcessingCode != iso8583.ProcBatchCloseForced {
		resp.ResponseCode = iso8583.RCReconcileError
		resp.Message = "The terminal's totals differ from the acquirer's. Compare the items, then close again with the acquirer's totals."
		resp.Items = items
	} else {
		resp.ResponseCode = iso8583.RCApproved
		resp.Message = fmt.Sprintf("Batch %s closed: %s. They go to clearing now; tips can no longer change.", rq.BatchNumber, plural(len(items), "transaction"))
		if !agree {
			resp.Message = fmt.Sprintf("Batch %s closed with the acquirer's totals: %s. Its records are what clears.", rq.BatchNumber, plural(len(items), "transaction"))
		}
		if held > 0 {
			resp.Message += fmt.Sprintf(" %s left out until captured.", plural(held, "open pre-auth"))
		}
		for _, r := range recs {
			r.Batch = rq.BatchNumber
			ids = append(ids, r.Response.NetworkTxnID)
			r.Steps = append(r.Steps, Step{
				From: PartyPOS, To: PartyAcquirer, MTI: iso8583.MTIReconciliation, Title: "Batch close",
				Detail: fmt.Sprintf("Terminal %s closes batch %s. The sale is sealed: no more tips or captures; it goes to clearing.", rq.TerminalID, rq.BatchNumber),
			})
		}
	}
	b.Response = resp
	a.batches = append(a.batches, b)
	if over := len(a.batches) - maxBatches; over > 0 {
		a.batches = slices.Delete(a.batches, 0, over)
	}
	return resp, ids, nil
}

// Batches returns the closes the acquirer has seen, newest first.
func (a *acquirer) Batches() []Batch {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := slices.Clone(a.batches)
	slices.Reverse(out)
	return out
}

// CloseBatch closes a terminal's batch and sends what it closed to clearing
// straight away, rather than waiting for the network's cutoff. It holds c.mu
// while closing, so a capture can't slip in. If the network can't be reached
// the batch stays closed and its file is resent with the next submission.
func (c *clearer) CloseBatch(ctx context.Context, rq iso8583.Reconciliation) (iso8583.ReconciliationResponse, error) {
	c.mu.Lock()
	resp, ids, err := c.acq.CloseBatch(rq)
	c.mu.Unlock()
	if err != nil || len(ids) == 0 {
		return resp, err
	}
	only := map[string]bool{}
	for _, id := range ids {
		only[id] = true
	}
	sent, err := c.submit(ctx, only)
	resp.ClearingFileID = sent.Header.FileID
	if err != nil {
		resp.ClearingError = err.Error() + "; the file is resent with the next submission"
	}
	return resp, nil
}
