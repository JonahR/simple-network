package issuer

import "fmt"

// DisputeNotice is the network telling the bank where one of its
// cardholder's disputes stands.
type DisputeNotice struct {
	DisputeID    string `json:"dispute_id"`
	NetworkTxnID string `json:"network_txn_id"`
	Reason       string `json:"reason"`
	State        string `json:"state"`
	Amount       int64  `json:"amount"`
	Credit       bool   `json:"credit"` // The cardholder keeps the disputed amount
	Summary      string `json:"summary"`
}

// ApplyDispute gives the cardholder a provisional credit while a dispute is
// open or won, and takes it back (rebills) when the merchant wins. It is
// idempotent: the notice says what should be true, and the bank moves the
// account only by the difference.
func (b *Bank) ApplyDispute(n DisputeNotice) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.disputeCredits == nil {
		b.disputeCredits = map[string]int64{}
	}
	acct := b.accountFor(n.NetworkTxnID)
	if acct == nil {
		return fmt.Errorf("no account for transaction %s", n.NetworkTxnID)
	}
	// A dispute can't credit more than the bank approved for the sale.
	var approved int64
	for _, d := range b.decisions {
		if d.NetworkTxnID == n.NetworkTxnID {
			approved = max(approved, d.Approved)
		}
	}
	if n.Amount < 0 || n.Amount > approved {
		return fmt.Errorf("dispute amount %d is outside the sale's approved %d", n.Amount, approved)
	}
	want := int64(0)
	if n.Credit {
		want = n.Amount
	}
	acct.Available += want - b.disputeCredits[n.DisputeID]
	b.disputeCredits[n.DisputeID] = want
	for i := range b.decisions {
		if b.decisions[i].NetworkTxnID == n.NetworkTxnID {
			b.decisions[i].Dispute = n.Summary
			b.decisions[i].DisputeCredit = want
		}
	}
	return nil
}
