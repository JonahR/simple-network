package fraud

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/JonahR/simple-network/internal/card"
	"github.com/JonahR/simple-network/internal/iso8583"
)

const (
	// slots is how many buckets a velocity window is split into. Counts are
	// exact to within one bucket (a twelfth of the window).
	slots = 12
	// maxKeys bounds the subjects tracked at once. At thousands of
	// authorizations a second most are cards seen once; idle ones are swept.
	maxKeys = 200_000
	// maxCases and maxCaseTxns bound the case queue.
	maxCases    = 500
	maxCaseTxns = 20
	// histMinutes is how far back each rule's activity chart reaches.
	histMinutes = 30
	sweepEvery  = 30 * time.Second
)

// Case statuses.
const (
	CaseOpen      = "open"
	CaseConfirmed = "confirmed" // An analyst agreed it is fraud
	CaseCleared   = "cleared"   // An analyst found it legitimate; any block is lifted
)

// Case is one incident for an analyst: a rule firing for one subject. Hits
// from the same rule and subject while the case is open join it.
type Case struct {
	ID       string    `json:"id"`
	Opened   time.Time `json:"opened"`
	Updated  time.Time `json:"updated"`
	RuleID   string    `json:"rule_id"`
	RuleName string    `json:"rule_name"`
	Mode     string    `json:"mode"`   // The rule's mode when it fired
	Action   string    `json:"action"` // What it did (or, in test mode, would have done)
	Subject  string    `json:"subject"`
	Label    string    `json:"label"` // The subject as people read it; card numbers masked
	Reason   string    `json:"reason"`
	Hits     int       `json:"hits"`
	Declined int       `json:"declined"` // Authorizations the case's rule or block declined
	Txns     []string  `json:"txns"`     // Network transaction IDs, newest last
	Status   string    `json:"status"`
	Note     string    `json:"note,omitempty"`
	BlockID  string    `json:"block_id,omitempty"`
	key      string
}

// Block stops every authorization from one subject until it expires or is lifted.
type Block struct {
	ID       string    `json:"id"`
	Subject  string    `json:"subject"`
	Label    string    `json:"label"`
	RuleID   string    `json:"rule_id"`
	RuleName string    `json:"rule_name"`
	Created  time.Time `json:"created"`
	Until    time.Time `json:"until"`
	Hits     int       `json:"hits"` // Authorizations declined because of it
	CaseID   string    `json:"case_id"`
	key      string
}

// RuleStats is what one rule has done since it was last changed.
type RuleStats struct {
	Hits     int64 `json:"hits"`     // Times it fired (in test mode: would have)
	Declined int64 `json:"declined"` // Authorizations it declined, directly or by its blocks
	// PerMinute is hits in each of the last 30 minutes, oldest first.
	PerMinute []int64 `json:"per_minute"`
}

// Verdict is the fraud department's answer for one authorization.
type Verdict struct {
	Decline bool
	Detail  string // One line for the switch's trace
	Hits    []Hit
}

// Hit is one rule that fired.
type Hit struct {
	RuleID, RuleName, Mode, Action, Reason, CaseID string
}

// Engine screens authorizations against the rules. It is safe for
// concurrent use.
type Engine struct {
	now func() time.Time

	mu       sync.Mutex
	rules    []*ruleState
	keys     map[string]*keyState // By subject|key
	blocks   map[string]*Block    // By subject|key
	cases    []*Case              // Oldest first
	openCase map[string]*Case     // By rule|subject|key
	seq      int
	screened int64
	declined int64
	swept    time.Time
}

type ruleState struct {
	Rule
	version int
	hits    int64
	decl    int64
	hist    [histMinutes]int64
	histAt  [histMinutes]int64 // Unix minute each bucket counts
}

// keyState holds one subject's velocity windows, one per rule that counts it.
type keyState struct {
	windows  map[string]*window // By rule ID + version
	lastSeen time.Time
	idle     time.Duration // Longest window among them: how long to keep it
}

type window struct {
	width  time.Duration // Window / slots
	counts [slots]int64
	stamps [slots]int64 // Which bucket number each slot holds
}

func (w *window) add(now time.Time, v int64) {
	n := now.UnixNano() / int64(w.width)
	i := n % slots
	if w.stamps[i] != n {
		w.stamps[i], w.counts[i] = n, 0
	}
	w.counts[i] += v
}

func (w *window) sum(now time.Time) int64 {
	n := now.UnixNano() / int64(w.width)
	var s int64
	for i := range slots {
		if w.stamps[i] > n-slots {
			s += w.counts[i]
		}
	}
	return s
}

// NewEngine creates an engine with rules.
func NewEngine(rules []Rule, now func() time.Time) *Engine {
	e := &Engine{now: now, keys: map[string]*keyState{}, blocks: map[string]*Block{}, openCase: map[string]*Case{}}
	for _, r := range rules {
		r := r
		if err := r.Validate(); err != nil {
			panic(fmt.Sprintf("fraud rule %q: %v", r.Name, err))
		}
		if r.ID == "" {
			r.ID = e.newRuleID(r.Name)
		}
		e.rules = append(e.rules, &ruleState{Rule: r})
	}
	return e
}

// KeyOf returns the key a subject is counted and blocked under for a
// request, and how to show it. Card numbers are hashed for the key and
// masked for the label, so the engine never holds a clear PAN.
func KeyOf(subject string, req iso8583.AuthRequest) (key, label string) {
	merchant := strings.TrimSpace(firstN(req.MerchantNameLoc, 25))
	switch subject {
	case SubjectCard:
		sum := sha256.Sum256([]byte(req.PAN))
		return hex.EncodeToString(sum[:12]), card.Mask(req.PAN)
	case SubjectTerminal:
		return req.AcquirerID + "|" + req.MerchantID + "|" + req.TerminalID, fmt.Sprintf("Terminal %s at %s", req.TerminalID, orID(merchant, req.MerchantID))
	case SubjectMerchant:
		return req.AcquirerID + "|" + req.MerchantID, fmt.Sprintf("%s (MID %s)", orID(merchant, "Merchant"), req.MerchantID)
	case SubjectAcquirer:
		return req.AcquirerID, "Acquirer " + req.AcquirerID
	case SubjectBIN:
		bin := firstN(req.PAN, 6)
		return bin, "BIN " + bin
	}
	return "", ""
}

var subjects = []string{SubjectCard, SubjectTerminal, SubjectMerchant, SubjectAcquirer, SubjectBIN}

// Screen checks an authorization before it is sent to the issuer: against
// active blocks, then every rule. txnID ties any case to the network's trace.
func (e *Engine) Screen(req iso8583.AuthRequest, txnID string) Verdict {
	now := e.now()
	e.mu.Lock()
	defer e.mu.Unlock()
	e.sweep(now)
	e.screened++

	keys := map[string][2]string{}
	for _, s := range subjects {
		k, l := KeyOf(s, req)
		keys[s] = [2]string{k, l}
	}

	// A blocked subject is declined before any rule runs.
	for _, s := range subjects {
		b, ok := e.blocks[s+"|"+keys[s][0]]
		if !ok {
			continue
		}
		if !now.Before(b.Until) {
			delete(e.blocks, s+"|"+keys[s][0])
			continue
		}
		b.Hits++
		e.declined++
		if rs := e.rule(b.RuleID); rs != nil {
			rs.decl++
		}
		if c := e.caseByID(b.CaseID); c != nil {
			c.Declined++
			c.addTxn(txnID, now)
		}
		return Verdict{Decline: true, Detail: fmt.Sprintf("%s is blocked until %s by rule %q", b.Label, b.Until.Local().Format("15:04:05"), b.RuleName)}
	}

	var v Verdict
	var effect *ruleState
	active := 0
	for _, rs := range e.rules {
		if rs.Mode == ModeOff || !rs.matches(req) {
			continue
		}
		active++
		key, label := keys[rs.Subject][0], keys[rs.Subject][1]
		fired, reason := rs.evaluate(e, key, req, now)
		if !fired {
			continue
		}
		rs.hit(now)
		c := e.recordCase(rs, key, label, reason, txnID, now)
		v.Hits = append(v.Hits, Hit{RuleID: rs.ID, RuleName: rs.Name, Mode: rs.Mode, Action: rs.Action, Reason: reason, CaseID: c.ID})
		if rs.Mode == ModeOn && rs.Action != ActionReview && (effect == nil || rs.Action == ActionBlock) {
			effect = rs
		}
	}

	if effect != nil {
		v.Decline = true
		e.declined++
		effect.decl++
		key, label := keys[effect.Subject][0], keys[effect.Subject][1]
		c := e.openCase[effect.ID+"|"+effect.Subject+"|"+key]
		c.Declined++
		if effect.Action == ActionBlock {
			e.seq++
			b := &Block{
				ID: fmt.Sprintf("B%d", e.seq), Subject: effect.Subject, Label: label, RuleID: effect.ID, RuleName: effect.Name,
				Created: now, Until: now.Add(time.Duration(effect.BlockSec) * time.Second), CaseID: c.ID, key: key,
			}
			e.blocks[effect.Subject+"|"+key] = b
			c.BlockID = b.ID
		}
	}
	v.Detail = detail(v, effect, active)
	return v
}

func detail(v Verdict, effect *ruleState, active int) string {
	if len(v.Hits) == 0 {
		return fmt.Sprintf("Screened against %d rule%s: no hits", active, plural1(active))
	}
	var parts []string
	for _, h := range v.Hits {
		switch {
		case h.Mode == ModeTest:
			parts = append(parts, fmt.Sprintf("%q (test mode, no effect): %s", h.RuleName, h.Reason))
		case h.Action == ActionReview:
			parts = append(parts, fmt.Sprintf("%q: %s, case opened for review", h.RuleName, h.Reason))
		default:
			parts = append(parts, fmt.Sprintf("%q: %s", h.RuleName, h.Reason))
		}
	}
	out := strings.Join(parts, "; ")
	if effect != nil {
		if effect.Action == ActionBlock {
			out += fmt.Sprintf(" → declined 59 and %s blocked for %s", SubjectNames[effect.Subject], duration(effect.BlockSec))
		} else {
			out += " → declined 59"
		}
	}
	return out
}

func plural1(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// evaluate reports whether the rule fires for this request, and why.
func (rs *ruleState) evaluate(e *Engine, key string, req iso8583.AuthRequest, now time.Time) (bool, string) {
	if rs.Kind == KindAmount {
		if req.Amount > rs.Threshold {
			return true, fmt.Sprintf("%s purchase, more than %s", money(req.Amount), money(rs.Threshold))
		}
		return false, ""
	}
	w := e.window(rs, key, now)
	var n int64
	switch rs.Measure {
	case MeasureAttempts:
		w.add(now, 1) // This attempt counts
		n = w.sum(now)
	case MeasureSpend:
		n = w.sum(now) + req.Amount // Would this purchase take it over?
	default:
		n = w.sum(now)
	}
	if n <= rs.Threshold {
		return false, ""
	}
	count := fmt.Sprintf("%d %s", n, MeasureNames[rs.Measure])
	if rs.Measure == MeasureSpend {
		count = money(n) + " " + MeasureNames[rs.Measure] + " including this one"
	}
	return true, fmt.Sprintf("%s within %s, more than %s", count, duration(rs.WindowSec), limit(rs.Rule))
}

func limit(r Rule) string {
	if r.Measure == MeasureSpend {
		return money(r.Threshold)
	}
	return fmt.Sprint(r.Threshold)
}

// systemCodes are declines that say nothing about the card or who used it:
// the issuer or switch failing, or a duplicate.
var systemCodes = map[string]bool{
	iso8583.RCIssuerUnavailable: true, iso8583.RCSystemError: true, iso8583.RCDuplicate: true,
	iso8583.RCLateResponse: true, iso8583.RCSuspectedMalfunc: true,
}

// Observe counts an authorization's outcome. fraudDeclined is set when the
// fraud department itself declined it; those don't count as declines, or a
// block would feed itself.
func (e *Engine) Observe(req iso8583.AuthRequest, code string, approvedAmount int64, fraudDeclined bool) {
	approved := iso8583.IsApproved(code)
	declined := !approved && !fraudDeclined && !systemCodes[code]
	if !approved && !declined {
		return
	}
	now := e.now()
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, rs := range e.rules {
		if rs.Mode == ModeOff || rs.Kind != KindVelocity || !rs.matches(req) {
			continue
		}
		var v int64
		switch {
		case rs.Measure == MeasureDeclines && declined:
			v = 1
		case rs.Measure == MeasureApprovals && approved:
			v = 1
		case rs.Measure == MeasureSpend && approved:
			v = approvedAmount
		}
		if v == 0 {
			continue
		}
		key, _ := KeyOf(rs.Subject, req)
		e.window(rs, key, now).add(now, v)
	}
}

// window returns the rule's counter for a subject, creating it. The caller
// holds e.mu.
func (e *Engine) window(rs *ruleState, key string, now time.Time) *window {
	ks := e.keys[rs.Subject+"|"+key]
	if ks == nil {
		if len(e.keys) >= maxKeys {
			e.evict()
		}
		ks = &keyState{windows: map[string]*window{}}
		e.keys[rs.Subject+"|"+key] = ks
	}
	ks.lastSeen = now
	ks.idle = max(ks.idle, rs.window())
	id := fmt.Sprintf("%s#%d", rs.ID, rs.version)
	w := ks.windows[id]
	if w == nil {
		w = &window{width: max(rs.window()/slots, time.Millisecond)}
		ks.windows[id] = w
	}
	return w
}

// sweep forgets subjects idle for longer than their longest window, and
// expired blocks. The caller holds e.mu.
func (e *Engine) sweep(now time.Time) {
	if now.Sub(e.swept) < sweepEvery {
		return
	}
	e.swept = now
	for k, ks := range e.keys {
		if now.Sub(ks.lastSeen) > ks.idle {
			delete(e.keys, k)
		}
	}
	for k, b := range e.blocks {
		if !now.Before(b.Until) {
			delete(e.blocks, k)
		}
	}
}

// evict drops the least recently seen tenth of the subjects when the table
// is full. The caller holds e.mu.
func (e *Engine) evict() {
	type seen struct {
		k string
		t time.Time
	}
	all := make([]seen, 0, len(e.keys))
	for k, ks := range e.keys {
		all = append(all, seen{k, ks.lastSeen})
	}
	slices.SortFunc(all, func(a, b seen) int { return a.t.Compare(b.t) })
	for _, s := range all[:len(all)/10+1] {
		delete(e.keys, s.k)
	}
}

func (rs *ruleState) hit(now time.Time) {
	rs.hits++
	m := now.Unix() / 60
	i := m % histMinutes
	if rs.histAt[i] != m {
		rs.histAt[i], rs.hist[i] = m, 0
	}
	rs.hist[i]++
}

// recordCase adds a hit to the open case for this rule and subject, or opens
// one. The caller holds e.mu.
func (e *Engine) recordCase(rs *ruleState, key, label, reason, txnID string, now time.Time) *Case {
	ck := rs.ID + "|" + rs.Subject + "|" + key
	if c := e.openCase[ck]; c != nil && c.Status == CaseOpen {
		c.Hits++
		c.Reason, c.Mode = reason, rs.Mode
		c.addTxn(txnID, now)
		return c
	}
	e.seq++
	c := &Case{
		ID: fmt.Sprintf("C%d", e.seq), Opened: now, Updated: now, RuleID: rs.ID, RuleName: rs.Name, Mode: rs.Mode,
		Action: rs.Action, Subject: rs.Subject, Label: label, Reason: reason, Hits: 1, Status: CaseOpen, key: key,
	}
	c.addTxn(txnID, now)
	e.openCase[ck] = c
	e.cases = append(e.cases, c)
	if len(e.cases) > maxCases {
		old := e.cases[0]
		e.cases = e.cases[1:]
		if e.openCase[old.RuleID+"|"+old.Subject+"|"+old.key] == old {
			delete(e.openCase, old.RuleID+"|"+old.Subject+"|"+old.key)
		}
	}
	return c
}

func (c *Case) addTxn(id string, now time.Time) {
	c.Updated = now
	if id == "" {
		return
	}
	c.Txns = append(c.Txns, id)
	if len(c.Txns) > maxCaseTxns {
		c.Txns = c.Txns[1:]
	}
}

func (e *Engine) rule(id string) *ruleState {
	for _, rs := range e.rules {
		if rs.ID == id {
			return rs
		}
	}
	return nil
}

func (e *Engine) caseByID(id string) *Case {
	for i := len(e.cases) - 1; i >= 0; i-- {
		if e.cases[i].ID == id {
			return e.cases[i]
		}
	}
	return nil
}

func (e *Engine) newRuleID(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
	}
	base := strings.Trim(b.String(), "-")
	if base == "" {
		base = "rule"
	}
	id := base
	for n := 2; e.rule(id) != nil; n++ {
		id = fmt.Sprintf("%s-%d", base, n)
	}
	return id
}

// AddRule adds a rule and returns it with its ID.
func (e *Engine) AddRule(r Rule) (Rule, error) {
	if err := r.Validate(); err != nil {
		return Rule{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	r.ID = e.newRuleID(r.Name)
	e.rules = append(e.rules, &ruleState{Rule: r})
	return r, nil
}

// UpdateRule replaces a rule. Its counters and statistics start over, since
// they were counted under the old definition.
func (e *Engine) UpdateRule(id string, r Rule) (Rule, error) {
	if err := r.Validate(); err != nil {
		return Rule{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	rs := e.rule(id)
	if rs == nil {
		return Rule{}, fmt.Errorf("no rule %q", id)
	}
	r.ID = id
	*rs = ruleState{Rule: r, version: rs.version + 1}
	return r, nil
}

// SetMode switches a rule on, to test mode, or off, keeping its statistics.
func (e *Engine) SetMode(id, mode string) error {
	if ModeNames[mode] == "" {
		return fmt.Errorf("mode must be on, test, or off")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	rs := e.rule(id)
	if rs == nil {
		return fmt.Errorf("no rule %q", id)
	}
	rs.Mode = mode
	return nil
}

// DeleteRule removes a rule. Its blocks stay until they expire or are lifted.
func (e *Engine) DeleteRule(id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	i := slices.IndexFunc(e.rules, func(rs *ruleState) bool { return rs.ID == id })
	if i < 0 {
		return false
	}
	e.rules = slices.Delete(e.rules, i, i+1)
	return true
}

// LiftBlock removes a block before it expires.
func (e *Engine) LiftBlock(id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for k, b := range e.blocks {
		if b.ID == id {
			delete(e.blocks, k)
			return true
		}
	}
	return false
}

// ResolveCase records an analyst's decision. Clearing a case lifts its block.
func (e *Engine) ResolveCase(id, status, note string) error {
	if status != CaseConfirmed && status != CaseCleared && status != CaseOpen {
		return fmt.Errorf("status must be open, confirmed, or cleared")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	c := e.caseByID(id)
	if c == nil {
		return fmt.Errorf("no case %q", id)
	}
	c.Status, c.Note, c.Updated = status, note, e.now()
	if status == CaseCleared && c.BlockID != "" {
		delete(e.blocks, c.Subject+"|"+c.key)
	}
	return nil
}

// SubjectKey names one subject, as KeyOf returns it.
type SubjectKey struct {
	Subject string `json:"subject"`
	Key     string `json:"key"`
}

// Forget drops what the engine knows about some subjects: their counters
// and blocks, and it closes their open cases with note. The Risk lab uses it
// so one drill's traffic doesn't spill into the next.
func (e *Engine) Forget(keys []SubjectKey, note string) (cases int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, k := range keys {
		delete(e.keys, k.Subject+"|"+k.Key)
		delete(e.blocks, k.Subject+"|"+k.Key)
		for ck, c := range e.openCase {
			if c.Subject == k.Subject && c.key == k.Key {
				if c.Status == CaseOpen {
					c.Status, c.Note, c.Updated = CaseCleared, note, e.now()
					cases++
				}
				delete(e.openCase, ck)
			}
		}
	}
	return cases
}

// RuleView is a rule with its sentence and statistics.
type RuleView struct {
	Rule
	Sentence string    `json:"sentence"`
	Stats    RuleStats `json:"stats"`
}

// Snapshot is everything the fraud page shows.
type Snapshot struct {
	Rules    []RuleView `json:"rules"`
	Blocks   []Block    `json:"blocks"` // Active, newest first
	Cases    []Case     `json:"cases"`  // Newest first
	Screened int64      `json:"screened"`
	Declined int64      `json:"declined"`
}

// Snapshot copies the engine's state.
func (e *Engine) Snapshot() Snapshot {
	now := e.now()
	m := now.Unix() / 60
	e.mu.Lock()
	defer e.mu.Unlock()
	s := Snapshot{Rules: []RuleView{}, Blocks: []Block{}, Cases: []Case{}, Screened: e.screened, Declined: e.declined}
	for _, rs := range e.rules {
		st := RuleStats{Hits: rs.hits, Declined: rs.decl, PerMinute: make([]int64, histMinutes)}
		for back := range histMinutes {
			mm := m - int64(histMinutes-1-back)
			if i := mm % histMinutes; rs.histAt[i] == mm {
				st.PerMinute[back] = rs.hist[i]
			}
		}
		s.Rules = append(s.Rules, RuleView{Rule: rs.Rule, Sentence: rs.Sentence(), Stats: st})
	}
	for _, b := range e.blocks {
		if now.Before(b.Until) {
			s.Blocks = append(s.Blocks, *b)
		}
	}
	slices.SortFunc(s.Blocks, func(a, b Block) int { return b.Created.Compare(a.Created) })
	for i := len(e.cases) - 1; i >= 0; i-- {
		c := *e.cases[i]
		c.Txns = slices.Clone(c.Txns)
		s.Cases = append(s.Cases, c)
	}
	return s
}

func firstN(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func orID(name, id string) string {
	if name == "" {
		return id
	}
	return name
}
