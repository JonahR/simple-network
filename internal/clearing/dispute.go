package clearing

import (
	"errors"
	"fmt"
)

// Dispute kinds: money a dispute moves, cleared and settled like any other
// item in the next cycle (KT 08: "disputes are just more ledger entries").
const (
	DisputeChargeback    = "chargeback"      // Acquirer pays the issuer back the disputed amount
	DisputeRepresentment = "representment"   // The issuer pays it back to the acquirer
	DisputeFee           = "arbitration_fee" // The losing side pays the network
)

// Program IDs that label dispute items on the settlement page.
const (
	ProgramChargeback    = "CHARGEBACK"
	ProgramRepresentment = "REPRESENTMENT"
	ProgramArbitration   = "ARBITRATION_FEE"
)

// DisputeMoney is one money movement a dispute asks the network to settle.
type DisputeMoney struct {
	Kind      string // DisputeChargeback, DisputeRepresentment, or DisputeFee
	DisputeID string
	Original  Item   // The cleared sale under dispute
	Amount    int64  // Disputed amount, or the fee
	FeePayer  string // For a fee: RoleIssuer or RoleAcquirer
}

// AddDispute adds a dispute's money movement to the open cycle and the
// ledger. It is settled at the next cutoff with everything else.
func (e *Engine) AddDispute(m DisputeMoney) (Item, error) {
	if m.Amount <= 0 {
		return Item{}, errors.New("dispute amount must be positive")
	}
	o := m.Original
	it := Item{
		Presentment: Presentment{
			NetworkTxnID: o.NetworkTxnID, ARN: o.ARN, RRN: o.RRN, AuthCode: o.AuthCode, PAN: o.PAN,
			Currency: o.Currency, MerchantID: o.MerchantID, MerchantName: o.MerchantName, MCC: o.MCC,
			EntryMode: o.EntryMode, ProcessingCode: o.ProcessingCode, TxnDate: o.TxnDate,
		},
		AcquirerID: o.AcquirerID, IssuerID: o.IssuerID, Product: o.Product, Channel: o.Channel,
		Dispute: m.Kind, DisputeID: m.DisputeID, Posting: "not sent (dispute)",
	}
	it.FeeVersion = e.Fees.Version
	switch m.Kind {
	case DisputeChargeback:
		it.Amount, it.ProgramID = m.Amount, ProgramChargeback
		it.IssuerOwes, it.AcquirerGets = -m.Amount, -m.Amount
	case DisputeRepresentment:
		it.Amount, it.ProgramID = m.Amount, ProgramRepresentment
		it.IssuerOwes, it.AcquirerGets = m.Amount, m.Amount
	case DisputeFee:
		it.ProgramID = ProgramArbitration
		switch m.FeePayer {
		case RoleIssuer:
			it.IssuerFee, it.IssuerOwes = m.Amount, m.Amount
		case RoleAcquirer:
			it.AcquirerFee, it.AcquirerGets = m.Amount, -m.Amount
		default:
			return Item{}, fmt.Errorf("fee payer must be %s or %s", RoleIssuer, RoleAcquirer)
		}
	default:
		return Item{}, fmt.Errorf("unknown dispute kind %q", m.Kind)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.init()
	it.CycleID = e.open.ID
	e.open.Items = append(e.open.Items, it)
	e.postClearing(e.open, it)
	return it, nil
}

// ClearedSale finds a cleared purchase (not a refund or dispute item) by
// network transaction ID, with the business date of the cycle that
// cleared it.
func (e *Engine) ClearedSale(networkTxnID string) (Item, string, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.init()
	for _, c := range append(append([]*Cycle{}, e.history...), e.open) {
		for _, it := range c.Items {
			if it.NetworkTxnID == networkTxnID && !it.Credit && it.Dispute == "" {
				return it, c.BusinessDate, true
			}
		}
	}
	return Item{}, "", false
}

// ClearedSales lists every cleared purchase, newest cycle first.
func (e *Engine) ClearedSales() []Item {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.init()
	var out []Item
	cycles := append(append([]*Cycle{}, e.history...), e.open)
	for i := len(cycles) - 1; i >= 0; i-- {
		for _, it := range cycles[i].Items {
			if !it.Credit && it.Dispute == "" {
				out = append(out, it)
			}
		}
	}
	return out
}

// BusinessDate is the open cycle's business date: the network's "today".
func (e *Engine) BusinessDate() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.init()
	return e.open.BusinessDate
}
