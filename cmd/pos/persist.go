package main

import (
	"github.com/JonahR/simple-network/internal/card"
	"github.com/JonahR/simple-network/internal/iso8583"
)

// posState is the terminal's saved state: its sales and its STAN counter, so
// trace numbers stay unique across restarts. The 0100 kept with each sale
// (for voids) is saved masked; a reversal names the sale by STAN and time.
type posState struct {
	STAN    uint32         `json:"stan"`
	History []savedSale    `json:"history"` // Oldest first
	BatchNo map[string]int `json:"batch_numbers"`
	Batches []BatchReport  `json:"batches"` // Oldest first
}

type savedSale struct {
	Transaction
	Sent iso8583.AuthRequest `json:"sent"`
}

func (s *server) saveState() posState {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := posState{STAN: s.stan.Load(), History: make([]savedSale, len(s.history)), BatchNo: map[string]int{},
		Batches: append([]BatchReport{}, s.batches...)}
	for k, v := range s.batchNo {
		st.BatchNo[k] = v
	}
	for i, tx := range s.history {
		st.History[i] = savedSale{Transaction: *tx, Sent: tx.sent.Redacted(card.Mask)}
	}
	return st
}

func (s *server) restoreState(st posState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st.STAN != 0 {
		s.stan.Store(st.STAN)
	}
	s.batchNo, s.batches = st.BatchNo, st.Batches
	s.history = make([]*Transaction, len(st.History))
	for i := range st.History {
		tx := st.History[i].Transaction
		tx.sent = st.History[i].Sent
		s.history[i] = &tx
	}
}
