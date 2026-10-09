// Package fraud is the network's fraud department: a rule engine that screens
// every authorization the switch routes.
//
// A rule is plain data, so it can be added from the fraud page, the API, or
// DefaultRules without code. Each rule reads as one sentence:
//
//	When one terminal has more than 5 declines within 1m → block it for 2m.
//	When a purchase is more than $1,000.00, keyed or online → open a case.
//
// Velocity rules count what a card, terminal, merchant, acquirer, or BIN
// range did in a sliding window; amount rules look at one purchase. A rule
// that fires declines the authorization (59 Suspected fraud), blocks the
// source for a while, or only opens a case for an analyst. In test mode it
// records what it would have done and changes nothing.
package fraud

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/JonahR/simple-network/internal/iso8583"
)

// Rule kinds.
const (
	KindVelocity = "velocity" // Counts activity for one subject over a window
	KindAmount   = "amount"   // Looks at a single purchase
)

// Subjects a velocity rule counts for, and a block applies to.
const (
	SubjectCard     = "card"
	SubjectTerminal = "terminal"
	SubjectMerchant = "merchant"
	SubjectAcquirer = "acquirer"
	SubjectBIN      = "bin"
)

// What a velocity rule counts.
const (
	MeasureAttempts  = "attempts"  // Authorizations screened
	MeasureDeclines  = "declines"  // Declines, except outages and the fraud department's own
	MeasureApprovals = "approvals" // Approvals
	MeasureSpend     = "spend"     // Approved amount, minor units
)

// Actions.
const (
	ActionDecline = "decline" // Decline this authorization with 59
	ActionBlock   = "block"   // Decline it and everything from the subject for BlockSec
	ActionReview  = "review"  // Let it through and open a case
)

// Modes.
const (
	ModeOn   = "on"
	ModeTest = "test" // Record what the rule would do; change nothing
	ModeOff  = "off"
)

// Rule is one fraud rule.
type Rule struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Why  string `json:"why,omitempty"` // The fraud pattern it is for
	Mode string `json:"mode"`
	Kind string `json:"kind"`
	// Subject is what a velocity rule counts for and what a block applies
	// to. Amount rules use it only to block.
	Subject string `json:"subject,omitempty"`
	Measure string `json:"measure,omitempty"`
	// Threshold: the rule fires when the count (or amount, in minor units)
	// is more than this.
	Threshold int64 `json:"threshold"`
	WindowSec int   `json:"window_sec,omitempty"`
	// Filters: when set, the rule only looks at these entry modes (DE22) and
	// merchant categories (DE18).
	EntryModes []string `json:"entry_modes,omitempty"`
	MCCs       []string `json:"mccs,omitempty"`
	Action     string   `json:"action"`
	BlockSec   int      `json:"block_sec,omitempty"`
}

// Labels for the sentence a rule reads as, and for the rule builder.
var (
	SubjectNames = map[string]string{
		SubjectCard: "card", SubjectTerminal: "terminal", SubjectMerchant: "merchant",
		SubjectAcquirer: "acquirer", SubjectBIN: "BIN range",
	}
	MeasureNames = map[string]string{
		MeasureAttempts: "authorizations", MeasureDeclines: "declines",
		MeasureApprovals: "approvals", MeasureSpend: "approved spend",
	}
	ModeNames = map[string]string{ModeOn: "On", ModeTest: "Test", ModeOff: "Off"}
)

// Validate checks a rule and fills in defaults.
func (r *Rule) Validate() error {
	r.Name = strings.TrimSpace(r.Name)
	if r.Mode == "" {
		r.Mode = ModeTest
	}
	if r.Kind == KindAmount && r.Subject == "" {
		r.Subject = SubjectCard
	}
	switch {
	case r.Name == "":
		return errors.New("give the rule a name")
	case len(r.Name) > 80:
		return errors.New("name is too long")
	case ModeNames[r.Mode] == "":
		return fmt.Errorf("mode must be on, test, or off")
	case r.Kind != KindVelocity && r.Kind != KindAmount:
		return fmt.Errorf("kind must be velocity or amount")
	case SubjectNames[r.Subject] == "":
		return fmt.Errorf("unknown subject %q", r.Subject)
	case r.Kind == KindVelocity && MeasureNames[r.Measure] == "":
		return fmt.Errorf("unknown measure %q", r.Measure)
	case r.Kind == KindVelocity && (r.WindowSec < 1 || r.WindowSec > 7*86400):
		return errors.New("window must be between 1 second and 7 days")
	case r.Threshold < 0:
		return errors.New("threshold can't be negative")
	case r.Action != ActionDecline && r.Action != ActionBlock && r.Action != ActionReview:
		return fmt.Errorf("action must be decline, block, or review")
	case r.Action == ActionBlock && (r.BlockSec < 1 || r.BlockSec > 7*86400):
		return errors.New("block time must be between 1 second and 7 days")
	}
	for _, m := range r.EntryModes {
		if iso8583.EntryModes[m] == "" {
			return fmt.Errorf("unknown entry mode %q", m)
		}
	}
	for _, m := range r.MCCs {
		if len(m) != 4 || strings.Trim(m, "0123456789") != "" {
			return fmt.Errorf("MCC %q must be 4 digits", m)
		}
	}
	if r.Kind == KindAmount {
		r.Measure, r.WindowSec = "", 0
	}
	if r.Action != ActionBlock {
		r.BlockSec = 0
	}
	return nil
}

// Sentence describes the rule in one line.
func (r Rule) Sentence() string {
	var b strings.Builder
	if r.Kind == KindAmount {
		fmt.Fprintf(&b, "When a purchase is more than %s", money(r.Threshold))
	} else {
		count := fmt.Sprintf("%d %s", r.Threshold, MeasureNames[r.Measure])
		if r.Measure == MeasureSpend {
			count = money(r.Threshold) + " " + MeasureNames[r.Measure]
		}
		fmt.Fprintf(&b, "When one %s has more than %s within %s", SubjectNames[r.Subject], count, duration(r.WindowSec))
	}
	if len(r.EntryModes) > 0 {
		names := make([]string, len(r.EntryModes))
		for i, m := range r.EntryModes {
			names[i] = strings.ToLower(iso8583.EntryModes[m])
		}
		b.WriteString(", by " + strings.Join(names, " or "))
	}
	if len(r.MCCs) > 0 {
		b.WriteString(", at MCC " + strings.Join(r.MCCs, " or "))
	}
	b.WriteString(" → " + r.actionPhrase())
	return b.String()
}

func (r Rule) actionPhrase() string {
	if r.Action == ActionBlock {
		return fmt.Sprintf("decline it and block the %s for %s", SubjectNames[r.Subject], duration(r.BlockSec))
	}
	if r.Action == ActionDecline {
		return "decline it (59 Suspected fraud)"
	}
	return "let it through and open a case"
}

func (r Rule) matches(req iso8583.AuthRequest) bool {
	return (len(r.EntryModes) == 0 || slices.Contains(r.EntryModes, req.EntryMode)) &&
		(len(r.MCCs) == 0 || slices.Contains(r.MCCs, req.MCC))
}

func (r Rule) window() time.Duration { return time.Duration(r.WindowSec) * time.Second }

// DefaultRules is the rule set the network starts with.
func DefaultRules() []Rule {
	return []Rule{
		{
			ID: "card-testing-terminal", Name: "Card testing at a terminal", Mode: ModeOn,
			Why:  "Many declines from one terminal in a short time is the signature of someone trying guessed or stolen card numbers.",
			Kind: KindVelocity, Subject: SubjectTerminal, Measure: MeasureDeclines, Threshold: 5, WindowSec: 60,
			Action: ActionBlock, BlockSec: 120,
		},
		{
			ID: "card-repeat-declines", Name: "Repeated declines on one card", Mode: ModeOn,
			Why:  "A card declined again and again is often someone guessing its CVV2, expiry, or PIN.",
			Kind: KindVelocity, Subject: SubjectCard, Measure: MeasureDeclines, Threshold: 3, WindowSec: 600,
			Action: ActionDecline,
		},
		{
			ID: "card-rapid-use", Name: "Rapid-fire use of one card", Mode: ModeOn,
			Why:  "No cardholder buys ten things a minute; a stolen card being emptied does.",
			Kind: KindVelocity, Subject: SubjectCard, Measure: MeasureAttempts, Threshold: 10, WindowSec: 60,
			Action: ActionDecline,
		},
		{
			ID: "large-keyed", Name: "Large keyed-in or online purchase", Mode: ModeTest,
			Why:  "Big card-not-present purchases carry the most fraud; worth a look before trusting them.",
			Kind: KindAmount, Subject: SubjectCard, Threshold: 100_000,
			EntryModes: []string{iso8583.EntryManual, iso8583.EntryEcommerce}, Action: ActionReview,
		},
		{
			ID: "merchant-decline-storm", Name: "Decline storm at a merchant", Mode: ModeTest,
			Why:  "A merchant suddenly declining many different cards may be compromised or used for testing.",
			Kind: KindVelocity, Subject: SubjectMerchant, Measure: MeasureDeclines, Threshold: 20, WindowSec: 60,
			Action: ActionReview,
		},
	}
}

// Templates are ready-made rules the builder offers, starting in test mode.
func Templates() []Rule {
	t := DefaultRules()
	t = append(t,
		Rule{
			Name: "Daily spend limit per card", Mode: ModeTest,
			Why:  "Caps how much one card can spend in a day, limiting the damage from a stolen card.",
			Kind: KindVelocity, Subject: SubjectCard, Measure: MeasureSpend, Threshold: 500_000, WindowSec: 86400,
			Action: ActionDecline,
		},
		Rule{
			Name: "BIN attack", Mode: ModeTest,
			Why:  "Bots walk through a whole BIN range; many declines across one range means an attack on that issuer.",
			Kind: KindVelocity, Subject: SubjectBIN, Measure: MeasureDeclines, Threshold: 30, WindowSec: 60,
			Action: ActionBlock, BlockSec: 300,
		},
		Rule{
			Name: "Keyed entry at a fuel pump", Mode: ModeTest,
			Why:  "Pumps read cards; a typed-in card number at one is almost never the cardholder.",
			Kind: KindAmount, Subject: SubjectCard, Threshold: 0, EntryModes: []string{iso8583.EntryManual},
			MCCs: []string{"5542"}, Action: ActionDecline,
		},
	)
	for i := range t {
		t[i].ID, t[i].Mode = "", ModeTest
	}
	return t
}

func money(minor int64) string {
	whole := fmt.Sprintf("%d", minor/100)
	for i := len(whole) - 3; i > 0; i -= 3 {
		whole = whole[:i] + "," + whole[i:]
	}
	return fmt.Sprintf("$%s.%02d", whole, minor%100)
}

func duration(sec int) string {
	switch {
	case sec%86400 == 0:
		return plural(sec/86400, "day")
	case sec%3600 == 0:
		return plural(sec/3600, "hour")
	case sec%60 == 0:
		return plural(sec/60, "minute")
	}
	return plural(sec, "second")
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}
