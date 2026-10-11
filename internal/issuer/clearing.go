package issuer

import "github.com/JonahR/simple-network/internal/clearing"

// Post applies the network's clearing file: each record turns its
// authorization hold into a posted charge for the final amount. The hold is
// released and the final amount is posted instead, so a final amount below
// the hold gives the difference back. A credit (a refund) gives the amount
// back to the card instead. Posting the same file twice (a retry after a
// lost reply) changes nothing.
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
		if r.Credit {
			// A refund: credit the account it was authorized on. It lowers
			// what is owed (Posted) and frees up that much to spend.
			if acct := b.accountFor(r.NetworkTxnID); acct != nil {
				acct.Posted -= r.Amount
				acct.Available += r.Amount
				b.setHold(r.NetworkTxnID, "credited")
				b.recordRefund(r)
				b.recordPosting(acct, r)
				ack.Posted++
			} else {
				ack.Unmatched = append(ack.Unmatched, r.NetworkTxnID)
			}
			continue
		}
		h, ok := b.holds[r.NetworkTxnID]
		if !ok {
			// Presented without a live hold: post it to the card the
			// authorization was for, and keep the right to charge it back.
			if acct := b.accountFor(r.NetworkTxnID); acct != nil {
				acct.Available -= r.Amount
				acct.Posted += r.Amount
				b.setHold(r.NetworkTxnID, "posted")
				b.recordPosting(acct, r)
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
		b.recordPosting(acct, r)
		ack.Posted++
	}
	b.postedFiles[f.FileID] = ack
	return ack
}

// recordRefund notes a posted credit on the refund's decision and on the
// sale it names, so a dispute on that sale can see the credit was already
// issued. The caller holds b.mu.
func (b *Bank) recordRefund(r clearing.IssuerRecord) {
	if r.OriginalTxnID == "" {
		return
	}
	for i := range b.decisions {
		d := &b.decisions[i]
		switch d.NetworkTxnID {
		case r.NetworkTxnID:
			d.OriginalTxnID = r.OriginalTxnID
		case r.OriginalTxnID:
			d.Refunded += r.Amount
		}
	}
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
