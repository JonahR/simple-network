package network

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"slices"
	"sync"
	"time"

	"github.com/JonahR/simple-network/internal/card"
	"github.com/JonahR/simple-network/internal/iso8583"
)

// StandInParams are the limits an issuer agreed to for stand-in processing
// (STIP, D8): when it can't answer, the network may approve on its behalf
// within them. Off by default.
type StandInParams struct {
	Enabled         bool     `json:"enabled"`
	MaxAmount       int64    `json:"max_amount"`        // Per authorization, minor units
	DailyCardLimit  int64    `json:"daily_card_limit"`  // Stand-in approvals per card per day, minor units
	BlockedMCCs     []string `json:"blocked_mccs"`      // Never approved in stand-in
	CardPresentOnly bool     `json:"card_present_only"` // Decline keyed and online sales
}

// DefaultStandIn is the starting agreement for every issuer: $100 per sale,
// $250 per card per day, no gambling, and switched off.
func DefaultStandIn() StandInParams {
	return StandInParams{MaxAmount: 10_000, DailyCardLimit: 25_000, BlockedMCCs: []string{"7995"}}
}

// AdviceRecord is an 0120 the network owes, or delivered to, an issuer.
type AdviceRecord struct {
	IssuerID  string             `json:"issuer_id"`
	Advice    iso8583.AuthAdvice `json:"advice"`
	Queued    time.Time          `json:"queued"`
	Attempts  int                `json:"attempts"`
	Delivered *time.Time         `json:"delivered,omitempty"`
	Result    string             `json:"result,omitempty"` // The issuer's answer, or the last error
}

// AdviceClient is implemented by issuer clients that accept 0120 advices.
type AdviceClient interface {
	Advise(ctx context.Context, adv iso8583.AuthAdvice) (iso8583.AuthAdviceResponse, error)
}

// maxAdvices caps delivered advices kept for display; undelivered ones are
// always kept.
const maxAdvices = 500

// StandIn holds every issuer's stand-in agreement, what stand-in has
// approved per card today, and the advices owed to issuers.
type StandIn struct {
	now func() time.Time

	mu      sync.Mutex
	params  map[string]StandInParams
	spent   map[string]int64 // issuer|card hash|date -> approved in stand-in
	advices []*AdviceRecord  // Oldest first
}

// NewStandIn creates stand-in agreements for issuers, all at the defaults.
func NewStandIn(issuerIDs []string, now func() time.Time) *StandIn {
	si := &StandIn{now: now, params: map[string]StandInParams{}, spent: map[string]int64{}}
	for _, id := range issuerIDs {
		si.params[id] = DefaultStandIn()
	}
	return si
}

// Params returns an issuer's stand-in agreement.
func (si *StandIn) Params(issuerID string) StandInParams {
	si.mu.Lock()
	defer si.mu.Unlock()
	return si.params[issuerID]
}

// SetEnabled switches stand-in on or off for an issuer.
func (si *StandIn) SetEnabled(issuerID string, enabled bool) bool {
	si.mu.Lock()
	defer si.mu.Unlock()
	p, ok := si.params[issuerID]
	if ok {
		p.Enabled = enabled
		si.params[issuerID] = p
	}
	return ok
}

func panHash(pan string) string {
	sum := sha256.Sum256([]byte(pan))
	return hex.EncodeToString(sum[:])
}

// decide applies an issuer's stand-in agreement to a request the issuer
// couldn't answer. On approval it queues the 0120 the issuer is owed.
func (si *StandIn) decide(issuerID string, fwd iso8583.AuthRequest, why string) (iso8583.AuthAdvice, string, bool) {
	si.mu.Lock()
	defer si.mu.Unlock()
	p := si.params[issuerID]
	now := si.now()
	hash := panHash(fwd.PAN)
	key := issuerID + "|" + hash + "|" + now.UTC().Format("2006-01-02")
	switch {
	case !p.Enabled:
		return iso8583.AuthAdvice{}, "stand-in is off for this issuer", false
	case fwd.AuthType != "":
		return iso8583.AuthAdvice{}, "stand-in approves ordinary purchases only, not " + fwd.AuthType + " authorizations", false
	case iso8583.IsBalanceInquiry(fwd.ProcessingCode):
		return iso8583.AuthAdvice{}, "only the issuer knows the balance", false
	case fwd.PINData != "":
		return iso8583.AuthAdvice{}, "a PIN can't be verified in stand-in", false
	case fwd.Amount > p.MaxAmount:
		return iso8583.AuthAdvice{}, fmt.Sprintf("amount %s is over the %s stand-in limit", minor(fwd.Amount), minor(p.MaxAmount)), false
	case slices.Contains(p.BlockedMCCs, fwd.MCC):
		return iso8583.AuthAdvice{}, "merchant category " + fwd.MCC + " is blocked in stand-in", false
	case p.CardPresentOnly && !cardPresent(fwd.EntryMode):
		return iso8583.AuthAdvice{}, "stand-in approves card-present sales only", false
	case si.spent[key]+fwd.Amount > p.DailyCardLimit:
		return iso8583.AuthAdvice{}, fmt.Sprintf("card already approved %s in stand-in today (limit %s)", minor(si.spent[key]), minor(p.DailyCardLimit)), false
	}
	si.spent[key] += fwd.Amount
	adv := iso8583.AuthAdvice{
		MTI: iso8583.MTIAuthAdvice, NetworkTxnID: fwd.NetworkTxnID, PAN: card.Mask(fwd.PAN), PANHash: hash,
		Expiry: fwd.Expiry, Amount: fwd.Amount, Currency: fwd.Currency, STAN: fwd.STAN, RRN: fwd.RRN,
		AuthCode: standInCode(), ResponseCode: iso8583.RCApproved, MCC: fwd.MCC, EntryMode: fwd.EntryMode,
		MerchantNameLoc: fwd.MerchantNameLoc, Reason: why,
	}
	si.advices = append(si.advices, &AdviceRecord{IssuerID: issuerID, Advice: adv, Queued: now})
	si.trim()
	return adv, fmt.Sprintf("approved on the issuer's behalf (%s): within its %s per sale and %s per card per day limits; 0120 advice queued", why, minor(p.MaxAmount), minor(p.DailyCardLimit)), true
}

// trim drops the oldest delivered advices over the cap. The caller holds si.mu.
func (si *StandIn) trim() {
	for len(si.advices) > maxAdvices {
		i := slices.IndexFunc(si.advices, func(a *AdviceRecord) bool { return a.Delivered != nil })
		if i < 0 {
			return
		}
		si.advices = slices.Delete(si.advices, i, i+1)
	}
}

// pending returns copies of an issuer's undelivered advices, oldest first.
func (si *StandIn) pending(issuerID string) []AdviceRecord {
	si.mu.Lock()
	defer si.mu.Unlock()
	var out []AdviceRecord
	for _, a := range si.advices {
		if a.IssuerID == issuerID && a.Delivered == nil {
			out = append(out, *a)
		}
	}
	return out
}

// record notes a delivery attempt.
func (si *StandIn) record(txnID string, delivered bool, result string) {
	si.mu.Lock()
	defer si.mu.Unlock()
	for _, a := range si.advices {
		if a.Advice.NetworkTxnID == txnID && a.Delivered == nil {
			a.Attempts++
			a.Result = result
			if delivered {
				t := si.now()
				a.Delivered = &t
			}
		}
	}
}

// StandInView is one issuer's agreement and advice queue, for display.
type StandInView struct {
	Params    StandInParams  `json:"params"`
	Pending   int            `json:"pending"`
	Delivered int            `json:"delivered"`
	Recent    []AdviceRecord `json:"recent"` // Newest first, up to 10
}

// View returns an issuer's stand-in state.
func (si *StandIn) View(issuerID string) StandInView {
	si.mu.Lock()
	defer si.mu.Unlock()
	v := StandInView{Params: si.params[issuerID], Recent: []AdviceRecord{}}
	for i := len(si.advices) - 1; i >= 0; i-- {
		a := si.advices[i]
		if a.IssuerID != issuerID {
			continue
		}
		if a.Delivered == nil {
			v.Pending++
		} else {
			v.Delivered++
		}
		if len(v.Recent) < 10 {
			v.Recent = append(v.Recent, *a)
		}
	}
	return v
}

// standIn is called when an issuer can't answer. If the issuer's agreement
// allows it, it approves on the issuer's behalf and tries to deliver the
// advice right away.
func (s *Switch) standIn(iss *Issuer, req, fwd iso8583.AuthRequest, id string, tr *tracer, why string) (iso8583.AuthResponse, bool) {
	if s.StandIn == nil {
		return iso8583.AuthResponse{}, false
	}
	t0 := s.Now()
	adv, detail, ok := s.StandIn.decide(iss.ID, fwd, why)
	tr.step("stand_in", detail, ok, t0)
	if !ok {
		return iso8583.AuthResponse{}, false
	}
	resp := s.decline(req, id, iso8583.RCApproved)
	resp.Amount, resp.AuthCode = req.Amount, adv.AuthCode
	s.background(func(ctx context.Context) { s.deliverAdvices(ctx, iss) })
	return resp, true
}

// deliverAdvices sends an issuer the 0120s it is owed, oldest first, and
// notes each on the authorization's trace. It stops at the first failure;
// the delivery loop tries again later.
func (s *Switch) deliverAdvices(ctx context.Context, iss *Issuer) {
	client, ok := iss.Client.(AdviceClient)
	if !ok || s.StandIn == nil {
		return
	}
	for _, a := range s.StandIn.pending(iss.ID) {
		t0 := s.Now()
		cctx, cancel := context.WithTimeout(ctx, s.IssuerTimeout)
		resp, err := client.Advise(cctx, a.Advice)
		cancel()
		var tr *tracer
		if rec := s.Recorder.record(a.Advice.NetworkTxnID); rec != nil {
			tr = &tracer{s: s, rec: rec, start: rec.Received}
		}
		if err != nil {
			s.StandIn.record(a.Advice.NetworkTxnID, false, err.Error())
			if tr != nil && a.Attempts == 0 {
				tr.step("advice", fmt.Sprintf("0120 to %s not delivered yet (%v); retrying until it is", iss.Name, err), false, t0)
			}
			return
		}
		result := "hold placed"
		if !resp.Held {
			result = "no hold: " + resp.Note
		} else if resp.Note != "" {
			result = resp.Note
		}
		s.StandIn.record(a.Advice.NetworkTxnID, true, result)
		if tr != nil {
			tr.step("advice", fmt.Sprintf("0120 delivered to %s: %s", iss.Name, result), true, t0)
		}
	}
}

// RunAdviceDelivery retries owed advices every interval until Close, for
// issuers whose circuit breaker is not open.
func (s *Switch) RunAdviceDelivery(every time.Duration) {
	s.background(func(ctx context.Context) {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				for _, iss := range s.Issuers {
					if iss.Breaker.State() != BreakerOpen && len(s.StandIn.pending(iss.ID)) > 0 {
						s.deliverAdvices(ctx, iss)
					}
				}
			}
		}
	})
}

// StandInState is saved stand-in state: agreements, today's spend, and
// advices. Advices carry a card hash, never a card number.
type StandInState struct {
	Params  map[string]StandInParams `json:"params"`
	Spent   map[string]int64         `json:"spent"`
	Advices []AdviceRecord           `json:"advices"`
}

// SaveState copies the stand-in state.
func (si *StandIn) SaveState() StandInState {
	si.mu.Lock()
	defer si.mu.Unlock()
	st := StandInState{Params: map[string]StandInParams{}, Spent: map[string]int64{}, Advices: []AdviceRecord{}}
	for k, v := range si.params {
		st.Params[k] = v
	}
	for k, v := range si.spent {
		st.Spent[k] = v
	}
	for _, a := range si.advices {
		st.Advices = append(st.Advices, *a)
	}
	return st
}

// RestoreState replaces the stand-in state with a saved one. Agreements
// for issuers that no longer exist are dropped.
func (si *StandIn) RestoreState(st StandInState) {
	si.mu.Lock()
	defer si.mu.Unlock()
	for k, v := range st.Params {
		if _, ok := si.params[k]; ok {
			si.params[k] = v
		}
	}
	si.spent = map[string]int64{}
	for k, v := range st.Spent {
		si.spent[k] = v
	}
	si.advices = nil
	for i := range st.Advices {
		a := st.Advices[i]
		si.advices = append(si.advices, &a)
	}
}

func cardPresent(entryMode string) bool {
	switch entryMode {
	case iso8583.EntryChip, iso8583.EntryContactless, iso8583.EntryMagstripe, iso8583.EntryFallback:
		return true
	}
	return false
}

// standInCode returns a network approval code: S and five digits, so it
// can't be mistaken for an issuer's.
func standInCode() string {
	n, _ := rand.Int(rand.Reader, big.NewInt(100_000))
	return fmt.Sprintf("S%05d", n.Int64())
}

func minor(v int64) string { return fmt.Sprintf("$%d.%02d", v/100, v%100) }
