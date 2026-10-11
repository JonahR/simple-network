package main

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"slices"
	"time"

	"github.com/JonahR/simple-network/internal/iso8583"
)

// Batch close. Each lane's terminal keeps a batch of the card sales and
// refunds it got approved. At the end of the day the cashier closes it: the
// terminal sends its totals to the acquirer in an 0500, the acquirer checks
// them against its own records, and on a match the batch is sealed and sent
// to clearing. Open pre-auths stay out until they are captured.

// maxBatchReports bounds the batch closes the terminal remembers.
const maxBatchReports = 200

// BatchReport is one attempt to close a batch.
type BatchReport struct {
	Closed      time.Time                       `json:"closed"`
	TerminalID  string                          `json:"terminal_id"`
	Request     iso8583.Reconciliation          `json:"request"`
	Response    *iso8583.ReconciliationResponse `json:"response,omitempty"` // Nil if the acquirer never answered
	Error       string                          `json:"error,omitempty"`
	Differences []BatchDifference               `json:"differences,omitempty"` // On a 95
}

// BatchDifference is a transaction the terminal and acquirer disagree on.
type BatchDifference struct {
	STAN           string `json:"stan"`
	Currency       string `json:"currency"`
	Refund         bool   `json:"refund,omitempty"`
	OnTerminal     bool   `json:"on_terminal"`
	TerminalAmount int64  `json:"terminal_amount"`
	OnHost         bool   `json:"on_host"`
	HostAmount     int64  `json:"host_amount"`
}

// batchItem reports whether tx is in its terminal's open batch, and its
// amount. held is set for an open pre-auth, which waits for its capture.
func batchItem(tx *Transaction) (item iso8583.BatchItem, ok, held bool) {
	switch {
	case tx.Batch != "", tx.Tender != "card", tx.Response == nil, tx.IncrementOf != "":
		return item, false, false
	case tx.Status != "APPROVED" && tx.Status != "PARTIAL" && tx.Status != "REFUND":
		return item, false, false
	case tx.Reversal != nil && tx.Reversal.Status != "FAILED":
		// Voided or being reversed. A reversal that failed leaves the sale
		// standing at the acquirer, so it stays in the batch.
		return item, false, false
	case tx.Request.AuthType == iso8583.AuthEstimated && tx.Captured == 0:
		return item, false, true
	}
	amount := tx.Response.Amount
	if tx.Captured > 0 {
		amount = tx.Captured
	}
	return iso8583.BatchItem{STAN: tx.Request.STAN, Refund: tx.Status == "REFUND", Amount: amount, Currency: tx.Request.Currency}, true, false
}

// openBatch lists a terminal's open batch. The caller holds s.mu.
func (s *server) openBatch(terminalID string) (txs []*Transaction, items []iso8583.BatchItem, held int) {
	for _, tx := range s.history {
		if tx.Request.TerminalID != terminalID {
			continue
		}
		it, ok, h := batchItem(tx)
		if h {
			held++
		}
		if ok {
			txs = append(txs, tx)
			items = append(items, it)
		}
	}
	return txs, items, held
}

// batchNumber is a terminal's open batch number, 001 to 999. The caller
// holds s.mu.
func (s *server) batchNumber(terminalID string) string {
	n := s.batchNo[terminalID]
	if n == 0 {
		n = 1
	}
	return fmt.Sprintf("%03d", n)
}

// differences compares the terminal's items with the acquirer's, by STAN.
func differences(mine, host []iso8583.BatchItem) []BatchDifference {
	diffs := map[string]*BatchDifference{}
	var order []string
	get := func(it iso8583.BatchItem) *BatchDifference {
		d := diffs[it.STAN]
		if d == nil {
			d = &BatchDifference{STAN: it.STAN, Currency: it.Currency, Refund: it.Refund}
			diffs[it.STAN] = d
			order = append(order, it.STAN)
		}
		return d
	}
	for _, it := range mine {
		d := get(it)
		d.OnTerminal, d.TerminalAmount = true, it.Amount
	}
	for _, it := range host {
		d := get(it)
		d.OnHost, d.HostAmount = true, it.Amount
	}
	var out []BatchDifference
	for _, stan := range order {
		if d := diffs[stan]; !d.OnTerminal || !d.OnHost || d.TerminalAmount != d.HostAmount {
			out = append(out, *d)
		}
	}
	slices.SortFunc(out, func(a, b BatchDifference) int { return cmp.Compare(a.STAN, b.STAN) })
	return out
}

// batchView is a terminal's open batch and its recent closes.
type batchView struct {
	TerminalID  string                `json:"terminal_id"`
	BatchNumber string                `json:"batch_number"`
	Count       int                   `json:"count"`
	Totals      []iso8583.BatchTotals `json:"totals"`
	Held        int                   `json:"held"`
	Reports     []BatchReport         `json:"reports"` // Newest first
}

// view returns a terminal's batch. The caller holds s.mu.
func (s *server) batchView(terminalID string) batchView {
	_, items, held := s.openBatch(terminalID)
	v := batchView{TerminalID: terminalID, BatchNumber: s.batchNumber(terminalID), Count: len(items),
		Totals: iso8583.SumBatch(items), Held: held, Reports: []BatchReport{}}
	for i := len(s.batches) - 1; i >= 0 && len(v.Reports) < 10; i-- {
		if s.batches[i].TerminalID == terminalID {
			v.Reports = append(v.Reports, s.batches[i])
		}
	}
	return v
}

func (s *server) handleBatch(w http.ResponseWriter, r *http.Request) {
	l, ok := s.lane(r.URL.Query().Get("terminal"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such terminal"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	writeJSON(w, http.StatusOK, s.batchView(l.TerminalID))
}

// handleBatchClose closes a lane's batch. With accept_host, after a 95, it
// closes with the acquirer's totals (processing code 960000).
func (s *server) handleBatchClose(w http.ResponseWriter, r *http.Request) {
	var in struct {
		TerminalID string `json:"terminal_id"`
		AcceptHost bool   `json:"accept_host"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	l, ok := s.lane(in.TerminalID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such terminal"})
		return
	}
	report := s.closeBatch(r.Context(), l, in.AcceptHost)
	s.mu.Lock()
	v := s.batchView(l.TerminalID)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"report": report, "batch": v})
}

// closeBatch sends the 0500 and applies the 0510.
func (s *server) closeBatch(ctx context.Context, l lane, acceptHost bool) BatchReport {
	s.mu.Lock()
	txs, items, _ := s.openBatch(l.TerminalID)
	rq := iso8583.Reconciliation{MTI: iso8583.MTIReconciliation, ProcessingCode: iso8583.ProcBatchClose, STAN: s.nextSTAN(),
		TerminalID: l.TerminalID, MerchantID: l.MerchantID, BatchNumber: s.batchNumber(l.TerminalID), Totals: iso8583.SumBatch(items)}
	if acceptHost {
		rq.ProcessingCode = iso8583.ProcBatchCloseForced
	}
	s.mu.Unlock()

	report := BatchReport{Closed: time.Now(), TerminalID: l.TerminalID, Request: rq}
	resp, err := s.acquirer.closeBatch(ctx, rq)

	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case err != nil:
		report.Error = "No answer from the acquirer: " + err.Error() + ". The batch is still open; close it again."
	case resp.ResponseCode == iso8583.RCApproved:
		report.Response = &resp
		for _, tx := range txs {
			if tx.Batch == "" {
				tx.Batch = rq.BatchNumber
			}
		}
		if s.batchNo == nil {
			s.batchNo = map[string]int{}
		}
		n := s.batchNo[l.TerminalID]
		s.batchNo[l.TerminalID] = max(n, 1)%999 + 1
	default:
		report.Response = &resp
		report.Differences = differences(items, resp.Items)
	}
	s.batches = append(s.batches, report)
	if over := len(s.batches) - maxBatchReports; over > 0 {
		s.batches = slices.Delete(s.batches, 0, over)
	}
	log.Printf("batch close terminal=%s batch=%s proc=%s items=%d rc=%s err=%v", l.TerminalID, rq.BatchNumber, rq.ProcessingCode, len(items), resp.ResponseCode, err)
	return report
}
