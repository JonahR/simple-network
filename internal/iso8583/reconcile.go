package iso8583

import "sort"

// Batch close (terminal settlement). At the end of the day the terminal
// sends its acquirer an 0500 reconciliation request with the totals of its
// open batch: how many sales and refunds it approved and for how much. The
// acquirer adds up its own records for the terminal. If they agree, it
// answers 00 and the batch is closed: tips can no longer change, and the
// sales go to clearing. If they don't, it answers 95 with its own totals and
// items so the terminal can find the difference.
const (
	MTIReconciliation     = "0500"
	MTIReconciliationResp = "0510"

	ProcBatchClose       = "920000" // Close the batch if the totals agree
	ProcBatchCloseForced = "960000" // Close with the acquirer's totals after a 95

	RCReconcileError = "95"
)

// BatchTotals are the counts and amounts of one currency in a batch, in the
// reconciliation fields of ISO 8583: debits are sales, credits refunds.
type BatchTotals struct {
	Currency      string `json:"de50_currency"`
	SalesCount    int    `json:"de76_debits_count"`
	SalesAmount   int64  `json:"de88_debits_amount"`
	RefundsCount  int    `json:"de74_credits_count"`
	RefundsAmount int64  `json:"de86_credits_amount"`
	Net           int64  `json:"de97_net_amount"` // Sales minus refunds
}

// BatchItem is one transaction in a batch, named by the terminal's STAN.
type BatchItem struct {
	STAN     string `json:"stan"`
	Refund   bool   `json:"refund,omitempty"`
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
}

// SumBatch totals batch items by currency, sorted by currency code.
func SumBatch(items []BatchItem) []BatchTotals {
	by := map[string]*BatchTotals{}
	for _, it := range items {
		t := by[it.Currency]
		if t == nil {
			t = &BatchTotals{Currency: it.Currency}
			by[it.Currency] = t
		}
		if it.Refund {
			t.RefundsCount++
			t.RefundsAmount += it.Amount
			t.Net -= it.Amount
		} else {
			t.SalesCount++
			t.SalesAmount += it.Amount
			t.Net += it.Amount
		}
	}
	out := make([]BatchTotals, 0, len(by))
	for _, t := range by {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Currency < out[j].Currency })
	return out
}

// SameTotals reports whether two sets of batch totals agree.
func SameTotals(a, b []BatchTotals) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Reconciliation is the terminal's 0500 batch close.
type Reconciliation struct {
	MTI            string        `json:"mti"`
	ProcessingCode string        `json:"de3_processing_code"` // 920000 or 960000
	STAN           string        `json:"de11_stan"`
	TerminalID     string        `json:"de41_terminal_id"`
	MerchantID     string        `json:"de42_merchant_id"`
	BatchNumber    string        `json:"de60_batch_number"` // 001 to 999, then back to 001
	Totals         []BatchTotals `json:"totals"`            // The terminal's, by currency
}

// ReconciliationResponse is the acquirer's 0510.
type ReconciliationResponse struct {
	MTI          string        `json:"mti"`
	STAN         string        `json:"de11_stan"`
	ResponseCode string        `json:"de39_response_code"` // 00 closed, 95 totals differ
	Message      string        `json:"message"`
	BatchNumber  string        `json:"de60_batch_number"`
	HostTotals   []BatchTotals `json:"host_totals"`     // The acquirer's, by currency
	Items        []BatchItem   `json:"items,omitempty"` // The acquirer's items, on a 95
	Held         int           `json:"held"`            // Open pre-auths left out until captured

	// Clearing: the file the closed batch went out in, or why it didn't.
	ClearingFileID string `json:"clearing_file_id,omitempty"`
	ClearingError  string `json:"clearing_error,omitempty"`
}
