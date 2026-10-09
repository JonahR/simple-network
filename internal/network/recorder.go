package network

import (
	"slices"
	"sync"
	"time"

	"github.com/JonahR/simple-network/internal/iso8583"
)

// Step is one thing the switch did while processing a transaction.
type Step struct {
	Name       string `json:"name"` // validate, route, detokenize, translate_pin, breaker, send, issuer, respond, reversal
	Detail     string `json:"detail"`
	OK         bool   `json:"ok"`
	StartUS    int64  `json:"start_us"` // Offset from when the request arrived
	DurationUS int64  `json:"duration_us"`
}

// Record is the network's trace of one authorization. Card data in it is
// masked; the clear PAN exists only in memory while the request is processed.
type Record struct {
	ID            string                `json:"network_txn_id"`
	Received      time.Time             `json:"received"`
	Status        string                `json:"status"` // pending, approved, declined
	AcquirerID    string                `json:"acquirer_id"`
	IssuerID      string                `json:"issuer_id,omitempty"`
	ResponseCode  string                `json:"response_code,omitempty"`
	ResponseText  string                `json:"response_text,omitempty"`
	LatencyUS     int64                 `json:"latency_us"`
	IssuerUS      int64                 `json:"issuer_us"` // Time spent waiting on the issuer
	Duplicates    int                   `json:"duplicates"`
	Reversed      bool                  `json:"reversed,omitempty"`       // The issuer acknowledged a reversal (0420/0430)
	Request       iso8583.AuthRequest   `json:"request"`                  // As received from the acquirer
	IssuerRequest *iso8583.AuthRequest  `json:"issuer_request,omitempty"` // As forwarded to the issuer
	Response      *iso8583.AuthResponse `json:"response,omitempty"`       // As returned to the acquirer
	Steps         []Step                `json:"steps"`
}

func (r *Record) clone() Record {
	c := *r
	c.Steps = slices.Clone(r.Steps)
	return c
}

// Recorder keeps the most recent records in memory and publishes every
// change to subscribers. Records are only appended to; a step is never
// edited once written.
type Recorder struct {
	max int

	mu    sync.Mutex
	order []string
	byID  map[string]*Record
	subs  map[chan Event]struct{}
}

// Event is published to subscribers when a record changes or issuer health
// changes.
type Event struct {
	Type   string  `json:"type"` // "txn" or "health"
	Record *Record `json:"record,omitempty"`
}

// NewRecorder keeps up to max records.
func NewRecorder(max int) *Recorder {
	return &Recorder{max: max, byID: map[string]*Record{}, subs: map[chan Event]struct{}{}}
}

func (r *Recorder) add(rec *Record) {
	r.mu.Lock()
	r.order = append(r.order, rec.ID)
	r.byID[rec.ID] = rec
	if len(r.order) > r.max {
		delete(r.byID, r.order[0])
		r.order = r.order[1:]
	}
	r.mu.Unlock()
	r.publish(rec)
}

// update applies fn to a record under the lock and publishes the result.
func (r *Recorder) update(rec *Record, fn func(*Record)) {
	r.mu.Lock()
	fn(rec)
	r.mu.Unlock()
	r.publish(rec)
}

func (r *Recorder) publish(rec *Record) {
	r.mu.Lock()
	c := rec.clone()
	subs := make([]chan Event, 0, len(r.subs))
	for ch := range r.subs {
		subs = append(subs, ch)
	}
	r.mu.Unlock()
	r.broadcast(Event{Type: "txn", Record: &c}, subs)
}

// PublishHealth tells subscribers that issuer health may have changed.
func (r *Recorder) PublishHealth() {
	r.mu.Lock()
	subs := make([]chan Event, 0, len(r.subs))
	for ch := range r.subs {
		subs = append(subs, ch)
	}
	r.mu.Unlock()
	r.broadcast(Event{Type: "health"}, subs)
}

func (r *Recorder) broadcast(ev Event, subs []chan Event) {
	for _, ch := range subs {
		select {
		case ch <- ev:
		default: // A slow subscriber misses events rather than stalling the switch.
		}
	}
}

// Subscribe returns a channel of events and a function to stop receiving them.
func (r *Recorder) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 64)
	r.mu.Lock()
	r.subs[ch] = struct{}{}
	r.mu.Unlock()
	return ch, func() {
		r.mu.Lock()
		delete(r.subs, ch)
		r.mu.Unlock()
	}
}

// Get returns a copy of one record.
func (r *Recorder) Get(id string) (Record, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.byID[id]
	if !ok {
		return Record{}, false
	}
	return rec.clone(), true
}

// Recent returns copies of up to n records, newest first.
func (r *Recorder) Recent(n int) []Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Record, 0, min(n, len(r.order)))
	for i := len(r.order) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, r.byID[r.order[i]].clone())
	}
	return out
}

// Stats summarizes the records the recorder holds.
type Stats struct {
	Total        int                    `json:"total"`
	Pending      int                    `json:"pending"`
	Approved     int                    `json:"approved"`
	Declined     int                    `json:"declined"`
	ApprovalRate float64                `json:"approval_rate"` // 0-1, over completed transactions
	P50US        int64                  `json:"p50_us"`
	P95US        int64                  `json:"p95_us"`
	ByCode       map[string]int         `json:"by_code"`
	ByIssuer     map[string]IssuerStats `json:"by_issuer"`
}

// IssuerStats summarizes one issuer's traffic.
type IssuerStats struct {
	Total       int   `json:"total"`
	Approved    int   `json:"approved"`
	AvgIssuerUS int64 `json:"avg_issuer_us"`
}

// Stats computes summary statistics.
func (r *Recorder) Stats() Stats {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := Stats{ByCode: map[string]int{}, ByIssuer: map[string]IssuerStats{}}
	var latencies []int64
	issuerTime := map[string]int64{}
	for _, id := range r.order {
		rec := r.byID[id]
		s.Total++
		if rec.Status == "pending" {
			s.Pending++
			continue
		}
		latencies = append(latencies, rec.LatencyUS)
		s.ByCode[rec.ResponseCode]++
		approved := rec.Status == "approved"
		if approved {
			s.Approved++
		} else {
			s.Declined++
		}
		if rec.IssuerID != "" {
			is := s.ByIssuer[rec.IssuerID]
			is.Total++
			if approved {
				is.Approved++
			}
			issuerTime[rec.IssuerID] += rec.IssuerUS
			s.ByIssuer[rec.IssuerID] = is
		}
	}
	for id, is := range s.ByIssuer {
		is.AvgIssuerUS = issuerTime[id] / int64(is.Total)
		s.ByIssuer[id] = is
	}
	if done := s.Approved + s.Declined; done > 0 {
		s.ApprovalRate = float64(s.Approved) / float64(done)
		slices.Sort(latencies)
		s.P50US = latencies[(len(latencies)-1)*50/100]
		s.P95US = latencies[(len(latencies)-1)*95/100]
	}
	return s
}
