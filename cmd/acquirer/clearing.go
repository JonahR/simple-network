package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/JonahR/simple-network/internal/card"
	"github.com/JonahR/simple-network/internal/clearing"
	"github.com/JonahR/simple-network/internal/iso8583"
)

// Presentment statuses, per authorization.
const (
	presented = "presented" // Accepted by the network, waiting for settlement
	rejected  = "rejected"  // The network would not clear it
	funded    = "funded"    // Settled, and the merchant has been paid
)

// SentFile is a clearing file the acquirer sent and the network's answer.
type SentFile struct {
	Header  clearing.FileHeader  `json:"header"`
	Trailer clearing.FileTrailer `json:"trailer"`
	Ack     *clearing.Ack        `json:"ack,omitempty"` // Nil until the network answers
	Error   string               `json:"error,omitempty"`
}

// Funding is what the acquirer paid one merchant for one settled cycle.
type Funding struct {
	CycleID      string `json:"cycle_id"`
	ValueDate    string `json:"value_date"`
	MerchantID   string `json:"merchant_id"`
	MerchantName string `json:"merchant_name"`
	Currency     string `json:"currency"`
	Count        int    `json:"count"`
	Sales        int64  `json:"sales"`
	DiscountFee  int64  `json:"discount_fee"` // Merchant discount rate on sales
	Paid         int64  `json:"paid"`         // Sales minus the discount fee
	Interchange  int64  `json:"interchange"`  // Paid to the issuer, out of the discount fee
	NetworkFees  int64  `json:"network_fees"` // Paid to the network, out of the discount fee
	Margin       int64  `json:"margin"`       // What the acquirer keeps (can be negative)
}

// clearer sends the acquirer's clearing files and funds merchants when the
// network settles.
type clearer struct {
	acq        *acquirer
	networkURL string
	http       *http.Client
	now        func() time.Time

	mu       sync.Mutex
	sequence int                 // Last file sequence the network accepted
	pending  *clearing.File      // A file sent but not acknowledged; resent as is
	status   map[string]string   // Network transaction ID -> presentment status
	reasons  map[string]string   // Network transaction ID -> rejection reason
	files    []SentFile          // Newest last
	fundings []Funding           // Newest last
	advices  map[string]struct{} // Cycle IDs already funded
}

func newClearer(acq *acquirer, networkURL string) *clearer {
	return &clearer{acq: acq, networkURL: networkURL, http: &http.Client{Timeout: networkTimeout}, now: time.Now,
		status: map[string]string{}, reasons: map[string]string{}, advices: map[string]struct{}{}}
}

// presentments builds clearing records for approved authorizations not yet
// presented. The caller holds c.mu.
func (c *clearer) presentments() []clearing.Presentment {
	var out []clearing.Presentment
	records := c.acq.Records()
	slices.Reverse(records) // Oldest first
	for _, r := range records {
		id := r.Response.NetworkTxnID
		if r.Status == "DECLINED" || id == "" || c.status[id] != "" || !iso8583.IsApproved(r.Response.ResponseCode) {
			continue
		}
		var cashback int64
		for _, a := range r.Request.AdditionalAmounts {
			if a.Type == iso8583.AmountCashback {
				cashback = a.Amount
			}
		}
		out = append(out, clearing.Presentment{
			NetworkTxnID:   id,
			RRN:            r.Request.RRN,
			AuthCode:       r.Response.AuthCode,
			PAN:            r.Request.PAN, // Already masked in the record
			Amount:         r.Response.Amount,
			Cashback:       min(cashback, r.Response.Amount),
			Currency:       r.Request.Currency,
			MerchantID:     r.MerchantID,
			MerchantName:   r.MerchantName,
			MCC:            r.Request.MCC,
			EntryMode:      r.Request.EntryMode,
			ProcessingCode: r.Request.ProcessingCode,
			TxnDate:        r.Received.UTC().Format("2006-01-02"),
		})
	}
	return out
}

// arn builds a 23-digit acquirer reference number: 7, the acquirer ID, the
// last digit of the year and day of year, an 11-digit sequence, and a Luhn
// check digit.
func arn(acquirerID string, now time.Time, seq int) string {
	body := fmt.Sprintf("7%06.6s%s%03d%011d", acquirerID, now.Format("2006")[3:], now.YearDay(), seq)
	for d := '0'; d <= '9'; d++ {
		if card.Luhn(body + string(d)) {
			return body + string(d)
		}
	}
	return body + "0"
}

// Submit sends a clearing file of everything not yet presented. A file the
// network never acknowledged is resent unchanged, with the same ID, so the
// network's duplicate check makes the retry safe.
func (c *clearer) Submit(ctx context.Context) (SentFile, error) {
	c.mu.Lock()
	file := c.pending
	if file == nil {
		records := c.presentments()
		now := c.now().UTC()
		base := len(c.status)
		for i := range records {
			records[i].ARN = arn(c.acq.id, now, base+i+1)
		}
		f := clearing.NewFile(clearing.FileHeader{
			FileID:     fmt.Sprintf("%s-%s-%03d", c.acq.id, now.Format("20060102T150405"), c.sequence+1),
			AcquirerID: c.acq.id,
			Sequence:   c.sequence + 1,
			Created:    now,
		}, records)
		file = &f
		c.pending = file
	}
	c.mu.Unlock()

	sent := SentFile{Header: file.Header, Trailer: file.Trailer}
	ack, err := c.send(ctx, *file)

	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		sent.Error = err.Error()
		c.files = append(c.files, sent)
		return sent, err
	}
	sent.Ack = &ack
	c.files = append(c.files, sent)
	c.pending = nil
	if ack.Status == clearing.FileRejected {
		return sent, nil // Nothing in it was presented; the records go in the next file
	}
	c.sequence = file.Header.Sequence
	rej := map[string]string{}
	for _, r := range ack.Rejected {
		rej[r.NetworkTxnID] = r.Reason
	}
	for _, p := range file.Records {
		if reason, ok := rej[p.NetworkTxnID]; ok {
			c.status[p.NetworkTxnID], c.reasons[p.NetworkTxnID] = rejected, reason
		} else {
			c.status[p.NetworkTxnID] = presented
		}
	}
	return sent, nil
}

func (c *clearer) send(ctx context.Context, f clearing.File) (clearing.Ack, error) {
	var ack clearing.Ack
	body, _ := json.Marshal(f)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.networkURL+"/clearing/files", bytes.NewReader(body))
	if err != nil {
		return ack, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return ack, fmt.Errorf("network unreachable: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return ack, fmt.Errorf("network returned HTTP %d", res.StatusCode)
	}
	if err := json.NewDecoder(res.Body).Decode(&ack); err != nil {
		return ack, fmt.Errorf("unreadable acknowledgement: %v", err)
	}
	return ack, nil
}

// Fund handles the network's settlement advice: it pays each merchant its
// sales minus the merchant discount fee. The discount fee covers interchange
// and network fees; what is left is the acquirer's margin. An advice for a
// cycle already funded changes nothing.
func (c *clearer) Fund(a clearing.Advice) []Funding {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.advices[a.CycleID+"|"+a.Currency]; ok {
		return nil
	}
	c.advices[a.CycleID+"|"+a.Currency] = struct{}{}
	byMerchant := map[string]*Funding{}
	var order []string
	for _, it := range a.Items {
		f := byMerchant[it.MerchantID]
		if f == nil {
			m, _ := c.acq.merchant(it.MerchantID)
			f = &Funding{CycleID: a.CycleID, ValueDate: a.ValueDate, MerchantID: it.MerchantID, MerchantName: m.Name, Currency: a.Currency}
			byMerchant[it.MerchantID] = f
			order = append(order, it.MerchantID)
		}
		m, _ := c.acq.merchant(it.MerchantID)
		fee := clearing.BPS(it.Amount, m.DiscountBPS)
		f.Count++
		f.Sales += it.Amount
		f.DiscountFee += fee
		f.Paid += it.Amount - fee
		f.Interchange += it.Interchange
		f.NetworkFees += it.NetworkFee
		f.Margin += fee - it.Interchange - it.NetworkFee
		c.status[it.NetworkTxnID] = funded
	}
	var out []Funding
	for _, id := range order {
		out = append(out, *byMerchant[id])
	}
	c.fundings = append(c.fundings, out...)
	return out
}

// ClearingView is the acquirer's clearing and funding state for its page.
type ClearingView struct {
	Unpresented int               `json:"unpresented"` // Approved authorizations not yet in a file
	Presented   int               `json:"presented"`   // Accepted, waiting for settlement
	Files       []SentFile        `json:"files"`       // Newest first
	Fundings    []Funding         `json:"fundings"`    // Newest first
	Status      map[string]string `json:"status"`      // Network transaction ID -> presented, rejected, or funded
	Reasons     map[string]string `json:"reasons"`
}

// View returns the clearing state. A nil clearer (an acquirer set up
// without clearing) reports nothing.
func (c *clearer) View() ClearingView {
	v := ClearingView{Files: []SentFile{}, Fundings: []Funding{}, Status: map[string]string{}, Reasons: map[string]string{}}
	if c == nil {
		return v
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	v.Unpresented = len(c.presentments())
	for i := len(c.files) - 1; i >= 0; i-- {
		v.Files = append(v.Files, c.files[i])
	}
	for i := len(c.fundings) - 1; i >= 0; i-- {
		v.Fundings = append(v.Fundings, c.fundings[i])
	}
	for id, s := range c.status {
		v.Status[id] = s
		if s == presented {
			v.Presented++
		}
	}
	for id, r := range c.reasons {
		v.Reasons[id] = r
	}
	return v
}
