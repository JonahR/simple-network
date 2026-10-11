package dispute

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/JonahR/simple-network/internal/clearing"
	"github.com/JonahR/simple-network/internal/iso8583"
)

// Facts are what the network knows about a cleared sale, for deciding
// whether a chargeback is allowed.
type Facts struct {
	Sale        clearing.Item
	ClearedOn   string // Business date of the cycle that cleared it
	ECI         string // 3-D Secure result; 05 is authenticated
	COF         string // Credential-on-file indicator
	StandIn     bool   // Approved by the network on the issuer's behalf
	Refunded    int64  // Refunds linked to the sale so far
	DuplicateOf string // Another cleared sale with the same card, merchant and amount that day
}

// Source looks up the facts of a cleared sale.
type Source interface {
	Facts(networkTxnID string) (Facts, bool)
}

// Money settles a dispute's money movements (the clearing engine).
type Money interface {
	AddDispute(m clearing.DisputeMoney) (clearing.Item, error)
	BusinessDate() string
}

// Notifier tells an issuer whether its cardholder should hold a provisional
// credit for a dispute. It must be idempotent.
type Notifier interface {
	NotifyDispute(ctx context.Context, n Notice) error
}

// Notice is what an issuer is told after every change to a dispute.
type Notice struct {
	DisputeID    string `json:"dispute_id"`
	NetworkTxnID string `json:"network_txn_id"`
	Reason       string `json:"reason"`
	State        string `json:"state"`
	Amount       int64  `json:"amount"`
	Credit       bool   `json:"credit"` // The cardholder keeps the disputed amount
	Summary      string `json:"summary"`
}

// States. Open states name whose turn it is.
const (
	StateChargeback     = "chargeback"      // Acquirer: accept or represent
	StateRepresented    = "represented"     // Issuer: accept or pre-arbitration
	StatePreArbitration = "pre_arbitration" // Acquirer: accept or ask for arbitration
	StateClosed         = "closed"
)

// Outcomes of a closed dispute.
const (
	OutcomeMerchantLiable   = "merchant_liable"     // The chargeback stands
	OutcomeCardholderRebill = "cardholder_rebilled" // The merchant's evidence stood; the cardholder pays
)

// Actors.
const (
	ActorIssuer   = "issuer"
	ActorAcquirer = "acquirer"
	ActorNetwork  = "network"
)

// Event is one step in a dispute's life.
type Event struct {
	At       time.Time `json:"at"`
	Day      string    `json:"day"` // Network business date when it happened
	Stage    string    `json:"stage"`
	Actor    string    `json:"actor"`
	Note     string    `json:"note"`
	Evidence string    `json:"evidence,omitempty"`
	Money    string    `json:"money,omitempty"` // The money it moved, if any
	CycleID  string    `json:"cycle_id,omitempty"`
}

// Dispute is one chargeback case.
type Dispute struct {
	ID           string  `json:"id"`
	NetworkTxnID string  `json:"network_txn_id"`
	ARN          string  `json:"arn"`
	IssuerID     string  `json:"issuer_id"`
	AcquirerID   string  `json:"acquirer_id"`
	MerchantID   string  `json:"merchant_id"`
	MerchantName string  `json:"merchant_name"`
	PAN          string  `json:"pan"` // Masked
	Currency     string  `json:"currency"`
	SaleAmount   int64   `json:"sale_amount"`
	Amount       int64   `json:"amount"` // Disputed
	Reason       string  `json:"reason"`
	State        string  `json:"state"`
	Outcome      string  `json:"outcome,omitempty"`
	Deadline     string  `json:"deadline,omitempty"` // Business date the current actor must act by
	Evidence     string  `json:"evidence,omitempty"` // The acquirer's response
	Rebuttal     string  `json:"rebuttal,omitempty"` // The issuer's answer to it
	Filed        string  `json:"filed"`              // Business date
	Events       []Event `json:"events"`
	notified     string  // State the issuer last acknowledged
}

// Turn returns whose move it is, or "" when closed.
func (d Dispute) Turn() string {
	switch d.State {
	case StateChargeback, StatePreArbitration:
		return ActorAcquirer
	case StateRepresented:
		return ActorIssuer
	}
	return ""
}

// CardholderCredit reports whether the cardholder keeps a credit for the
// disputed amount: from filing until the merchant wins.
func (d Dispute) CardholderCredit() bool {
	return d.Outcome != OutcomeCardholderRebill
}

// Engine runs disputes.
type Engine struct {
	Source    Source
	Money     Money
	Notifiers map[string]Notifier // By issuer ID
	Now       func() time.Time

	mu       sync.Mutex
	disputes []*Dispute // Oldest first
	seq      int
	offset   int // Simulated days added to the business date
}

// ErrNotFound is returned for an unknown dispute ID.
var ErrNotFound = errors.New("no such dispute")

// Today is the network's date for deadlines: the clearing business date
// plus any simulated days.
func (e *Engine) Today() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.today()
}

func (e *Engine) today() string {
	d, err := time.Parse("2006-01-02", e.Money.BusinessDate())
	if err != nil {
		return e.Money.BusinessDate()
	}
	return d.AddDate(0, 0, e.offset).Format("2006-01-02")
}

func addDays(date string, n int) string {
	d, err := time.Parse("2006-01-02", date)
	if err != nil {
		return date
	}
	return d.AddDate(0, 0, n).Format("2006-01-02")
}

func daysBetween(from, to string) int {
	a, err1 := time.Parse("2006-01-02", from)
	b, err2 := time.Parse("2006-01-02", to)
	if err1 != nil || err2 != nil {
		return 0
	}
	return int(b.Sub(a).Hours() / 24)
}

// Eligible checks whether an issuer may file a chargeback for a sale. It
// returns the reason it may not, or "".
func (e *Engine) Eligible(networkTxnID, reasonCode string, amount int64) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, why := e.eligible(networkTxnID, reasonCode, amount)
	return why
}

func (e *Engine) eligible(txnID, reasonCode string, amount int64) (Facts, string) {
	f, ok := e.Source.Facts(txnID)
	r, rok := reason(reasonCode)
	switch {
	case !ok:
		return f, "no cleared sale with that network transaction ID"
	case !rok:
		return f, "unknown reason code " + reasonCode
	case daysBetween(f.ClearedOn, e.today()) > ChargebackDays:
		return f, fmt.Sprintf("outside the %d-day chargeback window (cleared %s)", ChargebackDays, f.ClearedOn)
	case amount <= 0:
		return f, "the disputed amount must be positive"
	}
	open := e.disputed(txnID)
	if room := f.Sale.Amount - f.Refunded - open; amount > room {
		if f.Refunded > 0 && room <= 0 {
			return f, fmt.Sprintf("the merchant already refunded %s: credit already issued", money(f.Refunded))
		}
		return f, fmt.Sprintf("at most %s can be disputed (sale %s, refunded %s, already disputed %s)",
			money(max(room, 0)), money(f.Sale.Amount), money(f.Refunded), money(open))
	}
	switch r.Rule {
	case RuleCardAbsent:
		switch {
		case f.Sale.Channel == clearing.CardPresent:
			return f, "the card was present: card-absent fraud doesn't apply"
		case f.ECI == "05":
			return f, "the sale was 3-D Secure authenticated: liability shifted to the issuer"
		case f.StandIn:
			return f, "approved in stand-in under the issuer's own limits: liability is the issuer's"
		}
	case RuleSwiped:
		if f.Sale.EntryMode != iso8583.EntryMagstripe && f.Sale.EntryMode != iso8583.EntryFallback {
			return f, "the chip (or a wallet) was read: counterfeit liability stays with the issuer"
		}
	case RuleDuplicate:
		if f.DuplicateOf == "" {
			return f, "no other cleared sale with the same card, merchant and amount that day"
		}
	case RuleRecurring:
		if f.COF != iso8583.COFMerchantRecurring {
			return f, "not a merchant-initiated recurring charge"
		}
	}
	return f, ""
}

// disputed sums amounts already disputed for a sale, excluding cases the
// merchant won. The caller holds e.mu.
func (e *Engine) disputed(txnID string) int64 {
	var sum int64
	for _, d := range e.disputes {
		if d.NetworkTxnID == txnID && d.Outcome != OutcomeCardholderRebill {
			sum += d.Amount
		}
	}
	return sum
}

// File opens a first chargeback for an issuer. The disputed amount moves
// from the acquirer to the issuer in the next settlement.
func (e *Engine) File(ctx context.Context, txnID, reasonCode string, amount int64, note string) (Dispute, error) {
	e.mu.Lock()
	e.sweep()
	f, why := e.eligible(txnID, reasonCode, amount)
	if why != "" {
		e.mu.Unlock()
		return Dispute{}, errors.New(why)
	}
	e.seq++
	s := f.Sale
	d := &Dispute{
		ID: fmt.Sprintf("D%d", e.seq), NetworkTxnID: txnID, ARN: s.ARN, IssuerID: s.IssuerID, AcquirerID: s.AcquirerID,
		MerchantID: s.MerchantID, MerchantName: s.MerchantName, PAN: s.PAN, Currency: s.Currency,
		SaleAmount: s.Amount, Amount: amount, Reason: reasonCode, State: StateChargeback,
		Filed: e.today(), Deadline: addDays(e.today(), RepresentmentDays),
	}
	r, _ := reason(reasonCode)
	ev := Event{Stage: "First chargeback", Actor: ActorIssuer, Note: fmt.Sprintf("%s %s. %s", r.Code, r.Name, note)}
	if err := e.move(d, &ev, clearing.DisputeChargeback, amount, ""); err != nil {
		e.seq--
		e.mu.Unlock()
		return Dispute{}, err
	}
	e.log(d, ev)
	e.disputes = append(e.disputes, d)
	out := *d
	e.mu.Unlock()
	e.notify(ctx, out)
	return out, nil
}

// Accept closes a dispute in the other side's favor: the acquirer accepts a
// chargeback or pre-arbitration, or the issuer accepts a representment.
func (e *Engine) Accept(ctx context.Context, id, actor, note string) (Dispute, error) {
	return e.act(ctx, id, actor, func(d *Dispute) error {
		switch d.State {
		case StateChargeback:
			d.State, d.Outcome = StateClosed, OutcomeMerchantLiable
			e.log(d, Event{Stage: "Chargeback accepted", Actor: actor, Note: orDefault(note, "The merchant accepts; the chargeback stands.")})
		case StateRepresented:
			d.State, d.Outcome = StateClosed, OutcomeCardholderRebill
			e.log(d, Event{Stage: "Representment accepted", Actor: actor, Note: orDefault(note, "The issuer accepts the merchant's evidence and rebills the cardholder.")})
		case StatePreArbitration:
			ev := Event{Stage: "Pre-arbitration accepted", Actor: actor, Note: orDefault(note, "The merchant gives up; the money goes back to the issuer.")}
			if err := e.move(d, &ev, clearing.DisputeChargeback, d.Amount, ""); err != nil {
				return err
			}
			d.State, d.Outcome = StateClosed, OutcomeMerchantLiable
			e.log(d, ev)
		}
		d.Deadline = ""
		return nil
	})
}

// Represent is the acquirer's second presentment with evidence. The money
// moves back to the acquirer in the next settlement.
func (e *Engine) Represent(ctx context.Context, id, evidenceID, note string) (Dispute, error) {
	return e.act(ctx, id, ActorAcquirer, func(d *Dispute) error {
		if d.State != StateChargeback {
			return errors.New("only a first chargeback can be represented")
		}
		if err := e.validEvidence(d, evidenceID); err != nil {
			return err
		}
		ev, _ := evidence(evidenceID)
		e2 := Event{Stage: "Representment", Actor: ActorAcquirer, Evidence: evidenceID, Note: ev.Name + ". " + note}
		if err := e.move(d, &e2, clearing.DisputeRepresentment, d.Amount, ""); err != nil {
			return err
		}
		d.State, d.Evidence, d.Deadline = StateRepresented, evidenceID, addDays(e.today(), PreArbitrationDays)
		e.log(d, e2)
		return nil
	})
}

// PreArbitrate is the issuer rejecting the merchant's evidence with a
// rebuttal. An empty rebuttal is allowed but loses at arbitration.
func (e *Engine) PreArbitrate(ctx context.Context, id, rebuttalID, note string) (Dispute, error) {
	return e.act(ctx, id, ActorIssuer, func(d *Dispute) error {
		if d.State != StateRepresented {
			return errors.New("pre-arbitration answers a representment")
		}
		name := "No new information"
		if rebuttalID != "" {
			r, ok := rebuttal(rebuttalID)
			if !ok {
				return fmt.Errorf("unknown rebuttal %q", rebuttalID)
			}
			name = r.Name
		}
		d.State, d.Rebuttal, d.Deadline = StatePreArbitration, rebuttalID, addDays(e.today(), ArbitrationDays)
		e.log(d, Event{Stage: "Pre-arbitration", Actor: ActorIssuer, Evidence: rebuttalID, Note: name + ". " + note})
		return nil
	})
}

// Arbitrate is the acquirer refusing pre-arbitration: the network decides.
// The issuer wins if its rebuttal answers the merchant's evidence (the
// evidence's DefeatedBy list); otherwise the merchant's evidence stands.
// The loser pays the arbitration fee.
func (e *Engine) Arbitrate(ctx context.Context, id, note string) (Dispute, error) {
	return e.act(ctx, id, ActorAcquirer, func(d *Dispute) error {
		if d.State != StatePreArbitration {
			return errors.New("arbitration follows pre-arbitration")
		}
		e.log(d, Event{Stage: "Arbitration requested", Actor: ActorAcquirer, Note: orDefault(note, "The merchant stands by its evidence.")})
		ev0, _ := evidence(d.Evidence)
		rb, _ := rebuttal(d.Rebuttal)
		merchantWins := d.Rebuttal == "" || !slices.Contains(ev0.DefeatedBy, d.Rebuttal)
		var ev Event
		if merchantWins {
			why := "the issuer offered no rebuttal"
			if d.Rebuttal != "" {
				why = fmt.Sprintf("%q doesn't answer %q", rb.Name, ev0.Name)
			}
			ev = Event{Stage: "Arbitration decided", Actor: ActorNetwork,
				Note: fmt.Sprintf("The merchant wins: %s. The issuer pays the %s fee and rebills the cardholder.", why, money(ArbitrationFee))}
			if err := e.move(d, &ev, clearing.DisputeFee, ArbitrationFee, clearing.RoleIssuer); err != nil {
				return err
			}
			d.Outcome = OutcomeCardholderRebill
		} else {
			ev = Event{Stage: "Arbitration decided", Actor: ActorNetwork,
				Note: fmt.Sprintf("The issuer wins: %q answers %q. The money goes back to the issuer and the acquirer pays the %s fee.", rb.Name, ev0.Name, money(ArbitrationFee))}
			if err := e.move(d, &ev, clearing.DisputeChargeback, d.Amount, ""); err != nil {
				return err
			}
			fee := Event{}
			if err := e.move(d, &fee, clearing.DisputeFee, ArbitrationFee, clearing.RoleAcquirer); err != nil {
				return err
			}
			ev.Money += "; " + fee.Money
			d.Outcome = OutcomeMerchantLiable
		}
		d.State, d.Deadline = StateClosed, ""
		e.log(d, ev)
		return nil
	})
}

func evidenceName(id string) string {
	if ev, ok := evidence(id); ok {
		return ev.Name
	}
	return "No evidence"
}

// validEvidence checks that evidence can answer the dispute's reason. The
// caller holds e.mu.
func (e *Engine) validEvidence(d *Dispute, evidenceID string) error {
	r, _ := reason(d.Reason)
	if _, ok := evidence(evidenceID); !ok {
		return fmt.Errorf("unknown evidence %q", evidenceID)
	}
	if !slices.Contains(r.Responses, evidenceID) {
		return fmt.Errorf("%s doesn't answer reason %s %s", evidenceName(evidenceID), r.Code, r.Name)
	}
	if evidenceID == "refund_issued" {
		if f, ok := e.Source.Facts(d.NetworkTxnID); !ok || f.Refunded < d.Amount {
			return errors.New("no refund on record covers the disputed amount")
		}
	}
	return nil
}

// act runs a turn-checked change to a dispute and notifies its issuer.
func (e *Engine) act(ctx context.Context, id, actor string, change func(*Dispute) error) (Dispute, error) {
	e.mu.Lock()
	e.sweep()
	d := e.find(id)
	if d == nil {
		e.mu.Unlock()
		return Dispute{}, ErrNotFound
	}
	if d.State == StateClosed {
		e.mu.Unlock()
		return *d, errors.New("the dispute is closed")
	}
	if d.Turn() != actor {
		e.mu.Unlock()
		return *d, fmt.Errorf("it is the %s's turn", d.Turn())
	}
	if err := change(d); err != nil {
		e.mu.Unlock()
		return *d, err
	}
	out := clone(d)
	e.mu.Unlock()
	e.notify(ctx, out)
	return out, nil
}

// move settles a dispute's money in the next cycle and notes it on ev. The
// caller holds e.mu.
func (e *Engine) move(d *Dispute, ev *Event, kind string, amount int64, feePayer string) error {
	f, ok := e.Source.Facts(d.NetworkTxnID)
	if !ok {
		return errors.New("the cleared sale is no longer on record")
	}
	it, err := e.Money.AddDispute(clearing.DisputeMoney{Kind: kind, DisputeID: d.ID, Original: f.Sale, Amount: amount, FeePayer: feePayer})
	if err != nil {
		return err
	}
	desc := map[string]string{
		clearing.DisputeChargeback:    fmt.Sprintf("%s from acquirer %s to issuer %s", money(amount), d.AcquirerID, d.IssuerID),
		clearing.DisputeRepresentment: fmt.Sprintf("%s from issuer %s back to acquirer %s", money(amount), d.IssuerID, d.AcquirerID),
		clearing.DisputeFee:           fmt.Sprintf("%s fee from the %s to the network", money(amount), feePayer),
	}[kind]
	if ev.Money != "" {
		ev.Money += "; "
	}
	ev.Money += desc + " in cycle " + it.CycleID
	ev.CycleID = it.CycleID
	return nil
}

func (e *Engine) log(d *Dispute, ev Event) {
	ev.At, ev.Day = e.Now().UTC(), e.today()
	d.Events = append(d.Events, ev)
}

// Get returns a copy of one dispute.
func (e *Engine) Get(id string) (Dispute, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if d := e.find(id); d != nil {
		return clone(d), true
	}
	return Dispute{}, false
}

func (e *Engine) find(id string) *Dispute {
	for _, d := range e.disputes {
		if d.ID == id {
			return d
		}
	}
	return nil
}

// sweep closes disputes whose deadline passed (KT 08: deadlines are
// enforced by the network). Whoever missed it loses. The caller holds e.mu.
func (e *Engine) sweep() []Dispute {
	today := e.today()
	var changed []Dispute
	for _, d := range e.disputes {
		if d.State == StateClosed || d.Deadline == "" || today <= d.Deadline {
			continue
		}
		switch d.State {
		case StateChargeback:
			d.State, d.Outcome = StateClosed, OutcomeMerchantLiable
			e.log(d, Event{Stage: "Deadline missed", Actor: ActorNetwork, Note: fmt.Sprintf("The acquirer didn't answer by %s; the chargeback stands.", d.Deadline)})
		case StateRepresented:
			d.State, d.Outcome = StateClosed, OutcomeCardholderRebill
			e.log(d, Event{Stage: "Deadline missed", Actor: ActorNetwork, Note: fmt.Sprintf("The issuer didn't answer by %s; the representment stands and the cardholder is rebilled.", d.Deadline)})
		case StatePreArbitration:
			ev := Event{Stage: "Deadline missed", Actor: ActorNetwork, Note: fmt.Sprintf("The acquirer didn't answer pre-arbitration by %s; the issuer gets the money back.", d.Deadline)}
			if e.move(d, &ev, clearing.DisputeChargeback, d.Amount, "") != nil {
				continue
			}
			d.State, d.Outcome = StateClosed, OutcomeMerchantLiable
			e.log(d, ev)
		}
		d.Deadline = ""
		changed = append(changed, clone(d))
	}
	return changed
}

// Advance moves the simulated date forward and enforces deadlines.
func (e *Engine) Advance(ctx context.Context, days int) []Dispute {
	e.mu.Lock()
	e.offset += days
	changed := e.sweep()
	e.mu.Unlock()
	for _, d := range changed {
		e.notify(ctx, d)
	}
	return changed
}

// notify tells the issuer the dispute's state. Failures are retried by Sync.
func (e *Engine) notify(ctx context.Context, d Dispute) {
	n, ok := e.Notifiers[d.IssuerID]
	if !ok {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	err := n.NotifyDispute(cctx, Notice{
		DisputeID: d.ID, NetworkTxnID: d.NetworkTxnID, Reason: d.Reason, State: stateLabel(d), Amount: d.Amount,
		Credit: d.CardholderCredit(), Summary: summary(d),
	})
	if err != nil {
		return
	}
	e.mu.Lock()
	if live := e.find(d.ID); live != nil {
		live.notified = stateLabel(d)
	}
	e.mu.Unlock()
}

// Sync enforces deadlines and re-sends notices issuers haven't acknowledged.
func (e *Engine) Sync(ctx context.Context) {
	e.mu.Lock()
	changed := e.sweep()
	var pending []Dispute
	for _, d := range e.disputes {
		if d.notified != stateLabel(*d) {
			pending = append(pending, clone(d))
		}
	}
	e.mu.Unlock()
	_ = changed
	for _, d := range pending {
		e.notify(ctx, d)
	}
}

func stateLabel(d Dispute) string {
	if d.State == StateClosed {
		return d.Outcome
	}
	return d.State
}

func summary(d Dispute) string {
	r, _ := reason(d.Reason)
	switch {
	case d.Outcome == OutcomeCardholderRebill:
		return fmt.Sprintf("Dispute %s (%s) lost: the merchant's evidence stood; the cardholder is rebilled.", d.ID, r.Code)
	case d.Outcome == OutcomeMerchantLiable:
		return fmt.Sprintf("Dispute %s (%s) won: the chargeback stands.", d.ID, r.Code)
	default:
		return fmt.Sprintf("Dispute %s (%s %s) open: %s.", d.ID, r.Code, r.Name, d.State)
	}
}

func clone(d *Dispute) Dispute {
	c := *d
	c.Events = slices.Clone(d.Events)
	return c
}

// View is every dispute (newest first) and the reference tables.
type View struct {
	Today     string     `json:"today"`
	Offset    int        `json:"offset_days"`
	Disputes  []Dispute  `json:"disputes"`
	Reasons   []Reason   `json:"reasons"`
	Evidence  []Evidence `json:"evidence"`
	Rebuttals []Rebuttal `json:"rebuttals"`
	Limits    Limits     `json:"limits"`
}

// Limits are the time limits and fee.
type Limits struct {
	Chargeback, Representment, PreArbitration, Arbitration int
	Fee                                                    int64
}

// View returns a copy of every dispute.
func (e *Engine) View() View {
	e.mu.Lock()
	defer e.mu.Unlock()
	v := View{Today: e.today(), Offset: e.offset, Disputes: []Dispute{}, Reasons: Reasons, Evidence: EvidenceTypes, Rebuttals: Rebuttals,
		Limits: Limits{ChargebackDays, RepresentmentDays, PreArbitrationDays, ArbitrationDays, ArbitrationFee}}
	for i := len(e.disputes) - 1; i >= 0; i-- {
		v.Disputes = append(v.Disputes, clone(e.disputes[i]))
	}
	return v
}

// State is the saved dispute state.
type State struct {
	Disputes []Dispute `json:"disputes"`
	Notified []string  `json:"notified"` // Parallel to Disputes
	Seq      int       `json:"seq"`
	Offset   int       `json:"offset_days"`
}

// SaveState copies the disputes.
func (e *Engine) SaveState() State {
	e.mu.Lock()
	defer e.mu.Unlock()
	st := State{Disputes: []Dispute{}, Notified: []string{}, Seq: e.seq, Offset: e.offset}
	for _, d := range e.disputes {
		st.Disputes = append(st.Disputes, clone(d))
		st.Notified = append(st.Notified, d.notified)
	}
	return st
}

// RestoreState replaces the disputes with saved ones.
func (e *Engine) RestoreState(st State) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.disputes, e.seq, e.offset = nil, st.Seq, st.Offset
	for i := range st.Disputes {
		d := st.Disputes[i]
		if i < len(st.Notified) {
			d.notified = st.Notified[i]
		}
		e.disputes = append(e.disputes, &d)
	}
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func money(minor int64) string { return fmt.Sprintf("$%d.%02d", minor/100, minor%100) }
