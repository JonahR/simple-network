package main

import (
	"sort"
	"time"

	"github.com/JonahR/simple-network/internal/card"
	"github.com/JonahR/simple-network/internal/clearing"
	"github.com/JonahR/simple-network/internal/iso8583"
)

// acquirerState is the acquirer's saved state. Saved requests are masked: a
// later reversal names the original by STAN and transmission time, not card.
type acquirerState struct {
	STAN     uint32       `json:"stan"`
	Records  []Record     `json:"records"` // Oldest first
	Seen     []savedSeen  `json:"seen"`
	Legs     []savedLeg   `json:"legs"`
	Clearing clearerState `json:"clearing"`
}

type savedSeen struct {
	Key  string               `json:"key"`
	Resp iso8583.AuthResponse `json:"resp"`
	At   time.Time            `json:"at"`
}

type savedLeg struct {
	Key    string              `json:"key"`
	Out    iso8583.AuthRequest `json:"out"`    // Masked
	Record int                 `json:"record"` // Index into Records, or -1
	Reason string              `json:"reason,omitempty"`
}

type clearerState struct {
	Sequence int               `json:"sequence"`
	Pending  *clearing.File    `json:"pending,omitempty"`
	Status   map[string]string `json:"status"`
	Reasons  map[string]string `json:"reasons"`
	Files    []SentFile        `json:"files"`
	Fundings []Funding         `json:"fundings"`
	Advices  []string          `json:"advices"`
}

func (s *server) saveState() acquirerState {
	a := s.acq
	a.mu.Lock()
	st := acquirerState{STAN: a.stan.Load(), Records: make([]Record, len(a.records)), Seen: []savedSeen{}, Legs: []savedLeg{}}
	index := map[*Record]int{}
	for i, r := range a.records {
		c := *r
		c.Steps = append([]Step(nil), r.Steps...)
		st.Records[i] = c
		index[r] = i
	}
	for _, k := range a.seenOrder {
		if e, ok := a.seen[k.key]; ok {
			st.Seen = append(st.Seen, savedSeen{Key: k.key, Resp: e.resp, At: e.at})
		}
	}
	for key, l := range a.legs {
		i := -1
		if l.rec != nil {
			if j, ok := index[l.rec]; ok {
				i = j
			}
		}
		st.Legs = append(st.Legs, savedLeg{Key: key, Out: l.out.Redacted(card.Mask), Record: i, Reason: l.reason})
	}
	a.mu.Unlock()
	sort.Slice(st.Legs, func(i, j int) bool { return st.Legs[i].Key < st.Legs[j].Key })
	if s.clr != nil {
		st.Clearing = s.clr.saveState()
	}
	return st
}

func (s *server) restoreState(st acquirerState) {
	a := s.acq
	a.mu.Lock()
	defer a.mu.Unlock()
	if st.STAN != 0 {
		a.stan.Store(st.STAN)
	}
	a.records = make([]*Record, len(st.Records))
	for i := range st.Records {
		r := st.Records[i]
		a.records[i] = &r
	}
	a.seen, a.seenOrder = map[string]seenEntry{}, nil
	for _, e := range st.Seen {
		a.seen[e.Key] = seenEntry{resp: e.Resp, at: e.At}
		a.seenOrder = append(a.seenOrder, seenKey{key: e.Key, at: e.At})
	}
	a.legs = map[string]*leg{}
	for _, l := range st.Legs {
		lg := &leg{out: l.Out, reason: l.Reason}
		if l.Record >= 0 && l.Record < len(a.records) {
			lg.rec = a.records[l.Record]
		}
		a.legs[l.Key] = lg
	}
	if s.clr != nil {
		s.clr.restoreState(st.Clearing)
	}
}

func (c *clearer) saveState() clearerState {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := clearerState{Sequence: c.sequence, Pending: c.pending, Status: map[string]string{}, Reasons: map[string]string{},
		Files: append([]SentFile{}, c.files...), Fundings: append([]Funding{}, c.fundings...), Advices: []string{}}
	for k, v := range c.status {
		st.Status[k] = v
	}
	for k, v := range c.reasons {
		st.Reasons[k] = v
	}
	for k := range c.advices {
		st.Advices = append(st.Advices, k)
	}
	sort.Strings(st.Advices)
	return st
}

func (c *clearer) restoreState(st clearerState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sequence, c.pending = st.Sequence, st.Pending
	c.status, c.reasons, c.advices = map[string]string{}, map[string]string{}, map[string]struct{}{}
	for k, v := range st.Status {
		c.status[k] = v
	}
	for k, v := range st.Reasons {
		c.reasons[k] = v
	}
	for _, k := range st.Advices {
		c.advices[k] = struct{}{}
	}
	c.files, c.fundings = st.Files, st.Fundings
}
