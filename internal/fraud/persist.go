package fraud

// EngineState is the fraud department's saved state: its rules (including
// ones built in the page's rule builder) with their totals, its cases and
// active blocks. Sliding-window counters cover minutes and start empty
// after a restart. Subject keys for cards are hashes, never card numbers.
type EngineState struct {
	Rules    []SavedRule  `json:"rules"`
	Cases    []SavedCase  `json:"cases"` // Oldest first
	Blocks   []SavedBlock `json:"blocks"`
	Seq      int          `json:"seq"`
	Screened int64        `json:"screened"`
	Declined int64        `json:"declined"`
}

// SavedRule is a rule and how often it has fired.
type SavedRule struct {
	Rule
	Hits     int64 `json:"hits"`
	Declines int64 `json:"declines"`
}

// SavedCase is a case with the subject key it was opened under.
type SavedCase struct {
	Case
	Key string `json:"key"`
}

// SavedBlock is a block with the subject key it applies to.
type SavedBlock struct {
	Block
	Key string `json:"key"`
}

// SaveState copies the engine's rules, cases and blocks.
func (e *Engine) SaveState() EngineState {
	e.mu.Lock()
	defer e.mu.Unlock()
	st := EngineState{Rules: []SavedRule{}, Cases: []SavedCase{}, Blocks: []SavedBlock{},
		Seq: e.seq, Screened: e.screened, Declined: e.declined}
	for _, rs := range e.rules {
		st.Rules = append(st.Rules, SavedRule{Rule: rs.Rule, Hits: rs.hits, Declines: rs.decl})
	}
	for _, c := range e.cases {
		cc := *c
		cc.Txns = append([]string(nil), c.Txns...)
		st.Cases = append(st.Cases, SavedCase{Case: cc, Key: c.key})
	}
	for _, b := range e.blocks {
		st.Blocks = append(st.Blocks, SavedBlock{Block: *b, Key: b.key})
	}
	return st
}

// RestoreState replaces the engine's rules, cases and blocks with saved
// ones. Blocks that have expired are dropped. Saved rules that no longer
// validate are skipped, so a rule-format change can't stop the network.
func (e *Engine) RestoreState(st EngineState) {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	var rules []*ruleState
	for _, sr := range st.Rules {
		if sr.Validate() != nil {
			continue
		}
		rules = append(rules, &ruleState{Rule: sr.Rule, hits: sr.Hits, decl: sr.Declines})
	}
	if len(rules) > 0 {
		e.rules = rules
	}
	e.keys = map[string]*keyState{}
	e.cases, e.openCase = nil, map[string]*Case{}
	for _, sc := range st.Cases {
		c := sc.Case
		c.key = sc.Key
		e.cases = append(e.cases, &c)
		if c.Status == CaseOpen {
			e.openCase[c.RuleID+"|"+c.Subject+"|"+c.key] = &c
		}
	}
	e.blocks = map[string]*Block{}
	for _, sb := range st.Blocks {
		if !sb.Until.After(now) {
			continue
		}
		b := sb.Block
		b.key = sb.Key
		e.blocks[b.Subject+"|"+b.key] = &b
	}
	e.seq, e.screened, e.declined = st.Seq, st.Screened, st.Declined
}
