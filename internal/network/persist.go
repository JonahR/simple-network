package network

import (
	"slices"

	"github.com/JonahR/simple-network/internal/iso8583"
)

// RecorderState is the saved transaction records, oldest first. Card data in
// them is already masked.
type RecorderState struct {
	Records []Record `json:"records"`
}

// SaveState copies the recorder's records.
func (r *Recorder) SaveState() RecorderState {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := RecorderState{Records: make([]Record, 0, len(r.order))}
	for _, id := range r.order {
		st.Records = append(st.Records, r.byID[id].clone())
	}
	return st
}

// RestoreState replaces the recorder's records with saved ones.
func (r *Recorder) RestoreState(st RecorderState) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.order, r.byID = nil, map[string]*Record{}
	recs := st.Records
	if len(recs) > r.max {
		recs = recs[len(recs)-r.max:]
	}
	for i := range recs {
		rec := recs[i]
		if rec.Steps == nil {
			rec.Steps = []Step{}
		}
		r.order = append(r.order, rec.ID)
		r.byID[rec.ID] = &rec
	}
}

// record returns the live record with id, for linking restored attempts.
func (r *Recorder) record(id string) *Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.byID[id]
}

// VaultState is each token's active flag; the tokens themselves come from
// the vault's provisioning data.
type VaultState struct {
	Active map[string]bool `json:"active"`
}

// SaveState copies which tokens are active.
func (v *Vault) SaveState() VaultState {
	v.mu.RLock()
	defer v.mu.RUnlock()
	st := VaultState{Active: map[string]bool{}}
	for id, t := range v.tokens {
		st.Active[id] = t.Active
	}
	return st
}

// RestoreState reapplies saved active flags to tokens the vault still has.
func (v *Vault) RestoreState(st VaultState) {
	for id, active := range st.Active {
		v.SetActive(id, active)
	}
}

// SavedAttempt is a finished authorization the switch remembers, so a
// repeated 0100 gets its original answer and an acquirer's 0420 can still
// reverse it after a restart. Fwd is already masked; the issuer matches a
// reversal by network transaction ID, not card number.
type SavedAttempt struct {
	Key      string               `json:"key"` // Acquirer ID | STAN | DE7
	Started  bool                 `json:"started"`
	Reversed bool                 `json:"reversed"`
	Resp     iso8583.AuthResponse `json:"resp"`
	RecordID string               `json:"record_id,omitempty"`
	Fwd      *iso8583.AuthRequest `json:"fwd,omitempty"`
	IssuerID string               `json:"issuer_id,omitempty"`
}

// SwitchState is the switch's idempotency memory.
type SwitchState struct {
	Attempts []SavedAttempt `json:"attempts"` // Oldest first
}

// SaveState copies the finished attempts, and reversals that arrived before
// their authorization. Attempts still waiting on an issuer are left out.
func (s *Switch) SaveState() SwitchState {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := SwitchState{Attempts: []SavedAttempt{}}
	for _, key := range s.seenFIFO {
		a := s.seen[key]
		if a == nil {
			continue
		}
		sa := SavedAttempt{Key: key, Started: a.started, Reversed: a.reversed}
		if a.started {
			select {
			case <-a.done:
			default:
				continue // Still in flight
			}
			sa.Resp = a.resp
			if a.rec != nil {
				sa.RecordID = a.rec.ID
			}
			if a.fwd != nil {
				f := *a.fwd
				sa.Fwd = &f
			}
			if a.iss != nil {
				sa.IssuerID = a.iss.ID
			}
		}
		st.Attempts = append(st.Attempts, sa)
	}
	return st
}

// RestoreState rebuilds the switch's idempotency memory. Restore the
// recorder first: attempts link to their records.
func (s *Switch) RestoreState(st SwitchState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen, s.seenFIFO = map[string]*attempt{}, nil
	for _, sa := range st.Attempts {
		a := &attempt{done: make(chan struct{}), started: sa.Started, reversed: sa.Reversed}
		if sa.Started {
			rec := s.Recorder.record(sa.RecordID)
			if rec == nil {
				continue // The record aged out; without it the attempt can't be traced
			}
			a.resp, a.rec = sa.Resp, rec
			a.tr = &tracer{s: s, rec: rec, start: rec.Received}
			if sa.Fwd != nil && s.Issuers[sa.IssuerID] != nil {
				f := *sa.Fwd
				a.fwd, a.iss = &f, s.Issuers[sa.IssuerID]
			}
			close(a.done)
		}
		s.seen[sa.Key] = a
		s.seenFIFO = append(s.seenFIFO, sa.Key)
	}
	if len(s.seenFIFO) > maxSeen {
		for _, k := range s.seenFIFO[:len(s.seenFIFO)-maxSeen] {
			delete(s.seen, k)
		}
		s.seenFIFO = slices.Clone(s.seenFIFO[len(s.seenFIFO)-maxSeen:])
	}
}
