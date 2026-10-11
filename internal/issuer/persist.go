package issuer

import (
	"slices"

	"github.com/JonahR/simple-network/internal/clearing"
)

// BankState is a bank's saved state: accounts, holds and its logs. 3-D
// Secure challenges and their CAVVs live for minutes and are not kept.
type BankState struct {
	Accounts       []Account                     `json:"accounts"`             // In the order they were opened
	Holds          map[string]SavedHold          `json:"holds"`                // By network transaction ID
	Increments     map[string]SavedIncrement     `json:"increments,omitempty"` // Incremental authorizations folded into a hold
	Reversed       []string                      `json:"reversed"`             // Reversals that arrived before their authorization
	Decisions      []Decision                    `json:"decisions"`
	Reversals      []Reversal                    `json:"reversals"`
	PostedFiles    map[string]clearing.IssuerAck `json:"posted_files"`
	DisputeCredits map[string]int64              `json:"dispute_credits,omitempty"`
	Postings       []Posting                     `json:"postings,omitempty"` // The cardholder's posted transactions
}

// SavedHold is an authorization hold.
type SavedHold struct {
	PAN    string `json:"pan"`
	Amount int64  `json:"amount"`
}

// SavedIncrement is an incremental authorization, folded into the hold of
// the authorization it adds to.
type SavedIncrement struct {
	Of     string `json:"of"`
	Amount int64  `json:"amount"`
}

// SaveState copies the bank's state.
func (b *Bank) SaveState() BankState {
	b.mu.Lock()
	defer b.mu.Unlock()
	st := BankState{Holds: map[string]SavedHold{}, Reversed: []string{}, PostedFiles: map[string]clearing.IssuerAck{},
		Decisions: slices.Clone(b.decisions), Reversals: slices.Clone(b.reversals), Postings: slices.Clone(b.postings)}
	for _, pan := range b.order {
		st.Accounts = append(st.Accounts, *b.accounts[pan])
	}
	for id, h := range b.holds {
		st.Holds[id] = SavedHold{PAN: h.pan, Amount: h.amount}
	}
	if len(b.increments) > 0 {
		st.Increments = map[string]SavedIncrement{}
		for id, inc := range b.increments {
			st.Increments[id] = SavedIncrement{Of: inc.of, Amount: inc.amount}
		}
	}
	for id := range b.reversed {
		st.Reversed = append(st.Reversed, id)
	}
	slices.Sort(st.Reversed)
	for id, ack := range b.postedFiles {
		st.PostedFiles[id] = ack
	}
	st.DisputeCredits = map[string]int64{}
	for id, v := range b.disputeCredits {
		st.DisputeCredits[id] = v
	}
	return st
}

// RestoreState replaces the bank's state with a saved one. Accounts the seed
// has but the saved state lacks (added since it was saved) are kept.
func (b *Bank) RestoreState(st BankState) {
	b.mu.Lock()
	defer b.mu.Unlock()
	saved := map[string]bool{}
	var order []string
	for _, a := range st.Accounts {
		a := a
		b.accounts[a.PAN] = &a
		saved[a.PAN] = true
		order = append(order, a.PAN)
	}
	for _, pan := range b.order {
		if !saved[pan] {
			order = append(order, pan)
		}
	}
	b.order = order
	b.holds = map[string]hold{}
	for id, h := range st.Holds {
		b.holds[id] = hold{pan: h.PAN, amount: h.Amount}
	}
	b.increments = map[string]increment{}
	for id, inc := range st.Increments {
		b.increments[id] = increment{of: inc.Of, amount: inc.Amount}
	}
	b.reversed = map[string]struct{}{}
	for _, id := range st.Reversed {
		b.reversed[id] = struct{}{}
	}
	b.decisions, b.reversals, b.postings = st.Decisions, st.Reversals, st.Postings
	b.postedFiles = map[string]clearing.IssuerAck{}
	for id, ack := range st.PostedFiles {
		b.postedFiles[id] = ack
	}
	b.disputeCredits = map[string]int64{}
	for id, v := range st.DisputeCredits {
		b.disputeCredits[id] = v
	}
}
