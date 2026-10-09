package issuer

import "github.com/JonahR/simple-network/internal/clearing"

// Post applies the network's clearing file: each record turns its
// authorization hold into a posted charge for the final amount. The hold is
// released and the final amount is posted instead, so a final amount below
// the hold gives the difference back. Posting the same file twice (a retry
// after a lost reply) changes nothing.
func (b *Bank) Post(f clearing.IssuerFile) clearing.IssuerAck {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.postedFiles == nil {
		b.postedFiles = map[string]clearing.IssuerAck{}
	}
	if ack, ok := b.postedFiles[f.FileID]; ok {
		return ack
	}
	ack := clearing.IssuerAck{FileID: f.FileID, Unmatched: []string{}}
	for _, r := range f.Records {
		h, ok := b.holds[r.NetworkTxnID]
		if !ok {
			// Presented without a live hold: post it to the card the
			// authorization was for, and keep the right to charge it back.
			if acct := b.accountFor(r.NetworkTxnID); acct != nil {
				acct.Available -= r.Amount
				acct.Posted += r.Amount
				b.setHold(r.NetworkTxnID, "posted")
				ack.Posted++
			}
			ack.Unmatched = append(ack.Unmatched, r.NetworkTxnID)
			continue
		}
		acct := b.accounts[h.pan]
		acct.Available += h.amount - r.Amount
		acct.Posted += r.Amount
		delete(b.holds, r.NetworkTxnID)
		b.setHold(r.NetworkTxnID, "posted")
		ack.Posted++
	}
	b.postedFiles[f.FileID] = ack
	return ack
}

// accountFor finds the account an authorization was decided on. The caller holds b.mu.
func (b *Bank) accountFor(networkTxnID string) *Account {
	for _, d := range b.decisions {
		if d.NetworkTxnID != networkTxnID || d.AccountID == "" {
			continue
		}
		for _, a := range b.accounts {
			if a.ID == d.AccountID {
				return a
			}
		}
	}
	return nil
}

// setHold updates a decision's hold status. The caller holds b.mu.
func (b *Bank) setHold(networkTxnID, status string) {
	for i := range b.decisions {
		if b.decisions[i].NetworkTxnID == networkTxnID {
			b.decisions[i].Hold = status
		}
	}
}
