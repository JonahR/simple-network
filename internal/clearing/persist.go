package clearing

import "slices"

// EngineState is the clearing engine's saved state: every cycle, the
// ledger, settlement-bank balances, and what it needs to keep ingest
// idempotent (acknowledgements, sequences, cleared transactions).
type EngineState struct {
	Open        *Cycle            `json:"open"`
	History     []Cycle           `json:"history"` // Oldest first
	Acks        map[string]Ack    `json:"acks"`
	LastSeq     map[string]int    `json:"last_seq"`
	Cleared     map[string]string `json:"cleared"`
	Entries     []Entry           `json:"entries"` // Oldest first
	Bank        []BankBalance     `json:"bank"`
	Undelivered []IssuerFile      `json:"undelivered"`
}

// SaveState copies the engine's state.
func (e *Engine) SaveState() EngineState {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.init()
	open := e.open.clone()
	st := EngineState{Open: &open, Acks: map[string]Ack{}, LastSeq: map[string]int{}, Cleared: map[string]string{},
		Entries: slices.Clone(e.ledger.entries), Undelivered: slices.Clone(e.undelivered)}
	for _, c := range e.history {
		st.History = append(st.History, c.clone())
	}
	for k, v := range e.acks {
		st.Acks[k] = v
	}
	for k, v := range e.lastSeq {
		st.LastSeq[k] = v
	}
	for k, v := range e.cleared {
		st.Cleared[k] = v
	}
	for k, v := range e.bank {
		st.Bank = append(st.Bank, BankBalance{MemberID: k[0], Currency: k[1], Balance: v})
	}
	return st
}

// RestoreState replaces the engine's state with a saved one.
func (e *Engine) RestoreState(st EngineState) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.init()
	if st.Open != nil {
		e.open = st.Open
	}
	e.history = nil
	for i := range st.History {
		c := st.History[i]
		e.history = append(e.history, &c)
	}
	e.acks, e.lastSeq, e.cleared = map[string]Ack{}, map[string]int{}, map[string]string{}
	for k, v := range st.Acks {
		e.acks[k] = v
	}
	for k, v := range st.LastSeq {
		e.lastSeq[k] = v
	}
	for k, v := range st.Cleared {
		e.cleared[k] = v
	}
	e.ledger = Ledger{entries: st.Entries}
	e.bank = map[[2]string]int64{}
	for _, b := range st.Bank {
		e.bank[[2]string{b.MemberID, b.Currency}] = b.Balance
	}
	e.undelivered = st.Undelivered
}
