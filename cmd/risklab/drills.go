package main

import (
	"context"
	"fmt"
	"time"

	"github.com/JonahR/simple-network/internal/card"
	"github.com/JonahR/simple-network/internal/iso8583"
)

// Drill categories.
const (
	CategoryFraud   = "fraud"   // Someone trying to spend money that isn't theirs
	CategoryFailure = "failure" // Something breaking
)

// Hint says what to look for on another page after a drill.
type Hint struct {
	Page string `json:"page"` // acquirer, network, fraud, issuer, settlement
	Text string `json:"text"`
}

// Drill is one scenario on the Risk lab page.
type Drill struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	Title    string `json:"title"`
	Threat   string `json:"threat"` // What is going on, in the attacker's or operator's terms
	Defense  string `json:"defense"`
	Term     string `json:"term"` // Glossary term for the drill as a whole
	// Disrupts is set when the drill affects everyone's traffic for a while,
	// e.g. by slowing an issuer, and says how.
	Disrupts string `json:"disrupts,omitempty"`
	Watch    []Hint `json:"watch"`
	run      func(*runner)
}

// Cards the drills use. They match the issuers' seed accounts, the POS
// wallet, and the network's token vault.
var (
	everydayCredit = Card{PAN: "4242424242424242", Expiry: "3312", CVV2: "123"}
	checkingDebit  = Card{PAN: "4000056655665556", Expiry: "3305", CVV2: "789", PIN: "1234"}
	rewardsCredit  = Card{PAN: "5555555555554444", Expiry: "3208", CVV2: "456"}
	applePayToken  = Card{PAN: "4895372051310681", Expiry: "3409", Wallet: iso8583.WalletApplePay, ARQC: "9F26A1B2C3D4E5F6"}
	chipCryptogram = "9F2611223344556677"
)

func catalog() []Drill {
	return []Drill{
		{
			ID: "card-testing", Category: CategoryFraud, Title: "Card testing",
			Threat:  "A fraudster has a list of guessed card numbers and sends many $1 online purchases to learn which ones are real, before using the good ones for bigger fraud.",
			Defense: "The network rejects numbers that fail the Luhn check and the issuer declines numbers it never issued. The network's fraud department counts declines per terminal and blocks one that has too many.",
			Term:    "card-testing",
			Watch: []Hint{
				{"fraud", "A case for terminal RISK0001 under \"Card testing at a terminal\", and a block that the drill lifts when it ends."},
				{"network", "A run of 30 and 14 declines from terminal RISK0001, then 59s whose Fraud check step names the rule."},
				{"issuer", "First Simple Bank's decisions failing the Account check."},
				{"acquirer", "The Risk Lab merchant's declined count jumps."},
			},
			run: drillCardTesting,
		},
		{
			ID: "cvv-guessing", Category: CategoryFraud, Title: "Stolen card number, guessing the CVV",
			Threat:  "A card number and expiry leaked from a breached online shop, without the CVV2. The fraudster tries codes until one works.",
			Defense: "The issuer checks CVV2 and expiry on every card-not-present purchase. The fraud department declines a card that keeps getting declined.",
			Term:    "cvv2",
			Watch: []Hint{
				{"issuer", "Each guess is a decision failing the CVV2 check. The fifth never arrives: the network stopped it."},
				{"fraud", "A case under \"Repeated declines on one card\" for 424242******4242."},
			},
			run: drillCVVGuessing,
		},
		{
			ID: "pin-guessing", Category: CategoryFraud, Title: "Stolen debit card, guessing the PIN",
			Threat:  "A thief has the physical debit card and tries common PINs at a terminal.",
			Defense: "The PIN travels encrypted; the network translates it to the issuer's key and the issuer verifies it.",
			Term:    "pin",
			Watch: []Hint{
				{"network", "The translate_pin step on each attempt: the PIN block moves from the acquirer's key to First Simple Bank's."},
				{"issuer", "Incorrect PIN decisions on the checking debit card."},
			},
			run: drillPINGuessing,
		},
		{
			ID: "lost-card", Category: CategoryFraud, Title: "Lost card, frozen by the cardholder",
			Threat:  "The cardholder loses their wallet and freezes the card in their banking app. Whoever finds it tries to tap it and to use it online.",
			Defense: "The issuer checks card status before anything else.",
			Term:    "card-controls",
			Watch: []Hint{
				{"issuer", "The Everyday Credit card shows as frozen during the drill; its decisions fail Card status."},
			},
			run: drillLostCard,
		},
		{
			ID: "token-misuse", Category: CategoryFraud, Title: "Stolen wallet token",
			Threat:  "Someone copies the device token behind an Apple Pay card and tries to use it outside the phone: typed in by hand, without the cryptogram, or under another wallet. Then the phone is reported lost.",
			Defense: "The token vault only honors a token in its own domain (contactless or in-app, with a cryptogram, from its own wallet) and while it is active.",
			Term:    "network-token",
			Watch: []Hint{
				{"network", "Detokenize steps failing for each misuse; Routing & tokens shows the token suspended during the drill."},
				{"issuer", "Only the legitimate tap reaches First Simple Bank; it sees the card number, never the token."},
			},
			run: drillTokenMisuse,
		},
		{
			ID: "replay", Category: CategoryFraud, Title: "Replayed authorization",
			Threat:  "An attacker (or a confused terminal) resends an authorization it already got approved, hoping to be paid twice.",
			Defense: "The acquirer and the network both remember each request by its STAN and transmission time and answer a repeat with the original response.",
			Term:    "duplicate-detection",
			Watch: []Hint{
				{"network", "The replayed transaction's trace counts a duplicate; the issuer saw it once."},
				{"issuer", "One hold per replayed purchase, not two, then released by the void."},
			},
			run: drillReplay,
		},
		{
			ID: "rogue-sender", Category: CategoryFraud, Title: "Messages from outside the acquirer",
			Threat:  "A compromised system sends authorizations straight to the network, claiming to be the acquirer or a bank that isn't a member. A rogue terminal tries the acquirer.",
			Defense: "The acquirer only accepts terminals it signed up. The network checks the message format but not who sent it.",
			Term:    "message-authentication",
			Watch: []Hint{
				{"network", "Approvals from acquirer 999999, a bank that isn't a member, then voided."},
				{"acquirer", "The rogue terminal's attempt declined with 03; the direct-to-network approvals never appear here."},
			},
			run: drillRogueSender,
		},
		{
			ID: "malformed", Category: CategoryFailure, Title: "Malformed and misrouted messages",
			Threat:  "A buggy or hostile system sends broken messages: a zero amount, an unknown currency, a mistyped card number, a card from a network we don't route, the wrong message type.",
			Defense: "The network validates every 0100 before routing it and declines what it can't route.",
			Term:    "switch-validate",
			Watch: []Hint{
				{"network", "Validate and route steps failing; none of these reach an issuer."},
			},
			run: drillMalformed,
		},
		{
			ID: "issuer-timeout", Category: CategoryFailure, Title: "Issuer too slow",
			Threat:   "First Simple Bank's systems slow down and answer after the network has stopped waiting.",
			Defense:  "The network waits 5s, declines with 91, and sends the issuer an 0420 so a late approval doesn't leave a hold.",
			Term:     "timeout-reversal",
			Disrupts: "Every First Simple Bank card is slow for one purchase (about 5s), including POS sales.",
			Watch: []Hint{
				{"network", "The issuer step times out, then a reversal step acknowledged by First Simple Bank."},
				{"issuer", "The late authorization is declined: the 0420 arrived before it, so no hold is placed."},
			},
			run: drillIssuerTimeout,
		},
		{
			ID: "issuer-outage", Category: CategoryFailure, Title: "Issuer outage and circuit breaker",
			Threat:   "Union Card Bank goes down and refuses every connection.",
			Defense:  "After three failures in a row the network opens the issuer's circuit breaker and declines at once, without waiting on a dead bank.",
			Term:     "circuit-breaker",
			Disrupts: "Union Card Bank cards decline for about 15s after the drill while the breaker is open, including POS sales.",
			Watch: []Hint{
				{"network", "Issuers panel: Union Card Bank's breaker opens, then half-opens after 15s. The last two traces stop at the breaker step."},
			},
			run: drillIssuerOutage,
		},
	}
}

var drills = catalog()

func drillByID(id string) (Drill, bool) {
	for _, d := range drills {
		if d.ID == id {
			return d, true
		}
	}
	return Drill{}, false
}

func catalogView() []Drill { return drills }

func drillCardTesting(r *runner) {
	start := time.Now()
	var probes []Probe
	for i := range 20 {
		pan, _ := card.Generate("424242", 16)
		p := Probe{
			Via: ViaAcquirer, Entry: iso8583.EntryEcommerce, Amount: 100,
			Card:   Card{PAN: pan, Expiry: "2912", CVV2: "000"},
			Label:  "Guessed number under First Simple Bank's BIN",
			Expect: []string{iso8583.RCInvalidCard}, Control: "Issuer account lookup", Term: "issuer",
		}
		if i%5 == 4 {
			// A typo'd guess: the check digit is wrong.
			p.Card.PAN = pan[:15] + string(rune('0'+(pan[15]-'0'+1)%10))
			p.Label, p.Expect, p.Control, p.Term = "Guessed number with a bad check digit", []string{iso8583.RCFormatError}, "Luhn check", "luhn"
		}
		probes = append(probes, p)
	}
	results := r.burst(probes, 8)
	var reached, stopped int
	for _, res := range results {
		switch {
		case res.StopStep == "fraud":
			stopped++
		case res.StoppedAt == "issuer":
			reached++
		}
	}
	if stopped > 0 {
		r.check(fmt.Sprintf("Fraud department stopped %d of 20 guesses at the network; %d reached the issuer", stopped, reached),
			"Velocity rule", "velocity-check", true,
			"A terminal with too many declines is blocked, so the rest of the attack never reaches First Simple Bank.")
		return
	}
	r.finding(
		fmt.Sprintf("20 guesses from one terminal in %.1fs, and none was slowed down", time.Since(start).Seconds()),
		"Each attempt was judged on its own. Switch on a velocity rule in the network's Fraud department, such as \"Card testing at a terminal\", to block a terminal with many declines before they reach the issuer.",
		"velocity-check",
	)
}

func drillCVVGuessing(r *runner) {
	for _, guess := range []string{"000", "111", "999"} {
		c := everydayCredit
		c.CVV2 = guess
		r.probe(Probe{
			Label: "Guess CVV2 " + guess, Via: ViaAcquirer, Card: c, Entry: iso8583.EntryEcommerce, Amount: 4999,
			Expect: []string{iso8583.RCCVVMismatch}, Control: "CVV2 check", Term: "cvv2",
		})
	}
	c := everydayCredit
	c.Expiry = "2901"
	r.probe(Probe{
		Label: "Right CVV2, guessed expiry", Via: ViaAcquirer, Card: c, Entry: iso8583.EntryEcommerce, Amount: 4999,
		Expect: []string{iso8583.RCExpiredCard}, Control: "Expiry check", Term: "de14",
	})
	r.probe(Probe{
		Label: "Fifth try: right CVV2 and expiry", Via: ViaAcquirer, Card: everydayCredit, Entry: iso8583.EntryEcommerce, Amount: 4999,
		Expect: []string{iso8583.RCApproved}, Control: "CVV2 check", Term: "cvv2",
		Gap: "Four wrong answers in a row didn't block the card, so the fifth guess was approved. Switch on \"Repeated declines on one card\" in the Fraud department; issuers also block card-not-present use, or require 3-D Secure, after a few CVV2 failures.",
	})
}

func drillPINGuessing(r *runner) {
	for _, guess := range []string{"0000", "1111", "2580"} {
		c := checkingDebit
		c.PIN, c.ARQC = guess, chipCryptogram
		r.probe(Probe{
			Label: "Try PIN " + guess, Via: ViaAcquirer, Card: c, Entry: iso8583.EntryChip, Amount: 2000,
			Expect: []string{iso8583.RCIncorrectPIN}, Control: "PIN verification", Term: "pin",
		})
	}
	c := checkingDebit
	c.ARQC = chipCryptogram
	r.probe(Probe{
		Label: "Fourth try: the right PIN", Via: ViaAcquirer, Card: c, Entry: iso8583.EntryChip, Amount: 2000,
		Expect: []string{iso8583.RCApproved}, Control: "PIN try counter", Term: "pin-try-counter",
		Gap: "Three wrong PINs should have blocked the PIN (response 75) until the cardholder reset it. This issuer keeps no PIN try counter, so the fourth guess was approved.",
	})
}

func drillLostCard(r *runner) {
	if !r.action(r.ctx, "Cardholder freezes Everyday Credit in their banking app", func(ctx context.Context) error {
		return r.lab.setFrozen(ctx, "FSB", everydayCredit.PAN, true)
	}) {
		return
	}
	r.onFinish("Cardholder unfreezes the card", func(ctx context.Context) error {
		return r.lab.setFrozen(ctx, "FSB", everydayCredit.PAN, false)
	})
	tap := everydayCredit
	tap.CVV2, tap.ARQC = "", chipCryptogram
	r.probe(Probe{
		Label: "Finder taps the card", Via: ViaAcquirer, Card: tap, Entry: iso8583.EntryContactless, Amount: 1250,
		Expect: []string{iso8583.RCRestrictedCard}, Control: "Card status", Term: "card-controls",
	})
	r.probe(Probe{
		Label: "Finder types the card into a website", Via: ViaAcquirer, Card: everydayCredit, Entry: iso8583.EntryEcommerce, Amount: 8900,
		Expect: []string{iso8583.RCRestrictedCard}, Control: "Card status", Term: "card-controls",
	})
}

func drillTokenMisuse(r *runner) {
	r.probe(Probe{
		Label: "Baseline: the phone taps, with its cryptogram", Via: ViaAcquirer, Card: applePayToken, Entry: iso8583.EntryContactless, Amount: 650,
		Expect: []string{iso8583.RCApproved}, Control: "Detokenization", Term: "device-token",
	})
	keyed := applePayToken
	keyed.ARQC, keyed.Wallet, keyed.CVV2 = "", "", "123"
	r.probe(Probe{
		Label: "Token typed in by hand", Via: ViaAcquirer, Card: keyed, Entry: iso8583.EntryManual, Amount: 650,
		Expect: []string{iso8583.RCDoNotHonor}, ExpectStep: "detokenize", Control: "Token domain", Term: "network-token",
	})
	noCrypto := applePayToken
	noCrypto.ARQC = ""
	r.probe(Probe{
		Label: "Tap without the cryptogram", Via: ViaAcquirer, Card: noCrypto, Entry: iso8583.EntryContactless, Amount: 650,
		Expect: []string{iso8583.RCDoNotHonor}, ExpectStep: "detokenize", Control: "Cryptogram required", Term: "cryptogram",
	})
	other := applePayToken
	other.Wallet = iso8583.WalletGooglePay
	r.probe(Probe{
		Label: "Apple Pay token presented as Google Pay", Via: ViaAcquirer, Card: other, Entry: iso8583.EntryContactless, Amount: 650,
		Expect: []string{iso8583.RCDoNotHonor}, ExpectStep: "detokenize", Control: "Token domain", Term: "wallet-provider",
	})
	if !r.action(r.ctx, "Cardholder reports the phone lost: token suspended", func(ctx context.Context) error {
		return r.lab.setTokenActive(ctx, applePayToken.PAN, false)
	}) {
		return
	}
	r.onFinish("Token reactivated", func(ctx context.Context) error {
		return r.lab.setTokenActive(ctx, applePayToken.PAN, true)
	})
	r.probe(Probe{
		Label: "Thief taps the lost phone", Via: ViaAcquirer, Card: applePayToken, Entry: iso8583.EntryContactless, Amount: 650,
		Expect: []string{iso8583.RCRestrictedCard}, ExpectStep: "detokenize", Control: "Token status", Term: "token-status",
	})
}

func drillReplay(r *runner) {
	tap := rewardsCredit
	tap.CVV2, tap.ARQC = "", chipCryptogram
	for _, via := range []string{ViaAcquirer, ViaNetwork} {
		where := "the acquirer"
		if via == ViaNetwork {
			where = "the network"
		}
		first, req := r.probe(Probe{
			Label: "Original purchase sent to " + where, Via: via, Card: tap, Entry: iso8583.EntryContactless, Amount: 1800,
			Expect: []string{iso8583.RCApproved}, Control: "Issuer approval", Term: "authorization",
		})
		if first.NetworkTxnID == "" {
			continue
		}
		r.probe(Probe{
			Label: "Same message replayed to " + where, Via: via, Replay: &req,
			Expect: []string{iso8583.RCApproved}, Control: "Duplicate detection", Term: "duplicate-detection",
			Verify: func(resp iso8583.AuthResponse) (bool, string) {
				if resp.NetworkTxnID != first.NetworkTxnID {
					return false, fmt.Sprintf("A second authorization (%s) instead of the original %s", resp.NetworkTxnID, first.NetworkTxnID)
				}
				return true, "Answered with the original response (" + first.NetworkTxnID + "); the issuer saw it once"
			},
		})
	}
}

func drillRogueSender(r *runner) {
	r.probe(Probe{
		Label: "Rogue terminal ROGUE001 at the acquirer", Via: ViaAcquirer, TerminalID: "ROGUE001", Card: everydayCredit,
		Entry: iso8583.EntryEcommerce, Amount: 25000,
		Expect: []string{iso8583.RCInvalidMerchant}, Control: "Terminal registration", Term: "merchant",
	})
	gap := "Nothing proves who sent the message. Real networks only accept traffic over dedicated, mutually authenticated links from certified members, and check a MAC on each message."
	r.probe(Probe{
		Label: "Straight to the network, claiming to be acquirer 100001", Via: ViaNetwork, Card: everydayCredit,
		Entry: iso8583.EntryEcommerce, Amount: 25000,
		Expect: []string{iso8583.RCApproved}, Control: "Sender authentication", Term: "message-authentication", Gap: gap,
	})
	r.probe(Probe{
		Label: "Straight to the network as acquirer 999999, not a member", Via: ViaNetwork, AcquirerID: "999999", Card: everydayCredit,
		Entry: iso8583.EntryEcommerce, Amount: 25000,
		Expect: []string{iso8583.RCApproved}, Control: "Member check", Term: "message-authentication",
		Gap: "The network routed an authorization from a bank it has no agreement with. It should decline senders that aren't in its participant registry.",
	})
}

func drillMalformed(r *runner) {
	type bad struct {
		label   string
		card    Card
		mutate  func(*iso8583.AuthRequest)
		code    string
		step    string
		control string
		term    string
	}
	for _, b := range []bad{
		{"Zero amount", everydayCredit, func(q *iso8583.AuthRequest) { q.Amount = 0 }, iso8583.RCFormatError, "validate", "Message validation", "switch-validate"},
		{"Unknown currency 999", everydayCredit, func(q *iso8583.AuthRequest) { q.Currency = "999" }, iso8583.RCFormatError, "validate", "Message validation", "de49"},
		{"Card number with a typo", Card{PAN: "4242424242424241", Expiry: "3312", CVV2: "123"}, nil, iso8583.RCFormatError, "validate", "Luhn check", "luhn"},
		{"Wrong message type (0200)", everydayCredit, func(q *iso8583.AuthRequest) { q.MTI = "0200" }, iso8583.RCFormatError, "validate", "Message validation", "mti"},
		{"Card from a range we don't route", Card{PAN: "6011111111111117", Expiry: "3312", CVV2: "123"}, nil, iso8583.RCNoSuchIssuer, "route", "BIN routing", "bin"},
	} {
		r.probe(Probe{
			Label: b.label, Via: ViaNetwork, Card: b.card, Entry: iso8583.EntryEcommerce, Amount: 1000, Mutate: b.mutate,
			Expect: []string{b.code}, ExpectStep: b.step, Control: b.control, Term: b.term,
		})
	}
}

// withIssuerMode switches an issuer's fault mode for the rest of the drill
// and restores the mode it had. It reports whether the switch worked.
func withIssuerMode(r *runner, issuer, name, mode string) bool {
	prev, err := r.lab.issuerMode(r.ctx, issuer)
	if err != nil || prev == "" {
		prev = "normal"
	}
	if !r.action(r.ctx, fmt.Sprintf("%s set to %q", name, mode), func(ctx context.Context) error {
		return r.lab.setIssuerMode(ctx, issuer, mode)
	}) {
		return false
	}
	r.onFinish(fmt.Sprintf("%s set back to %q", name, prev), func(ctx context.Context) error {
		return r.lab.setIssuerMode(ctx, issuer, prev)
	})
	return true
}

func drillIssuerTimeout(r *runner) {
	if !withIssuerMode(r, "FSB", "First Simple Bank", "slow") {
		return
	}
	tap := everydayCredit
	tap.CVV2, tap.ARQC = "", chipCryptogram
	res, _ := r.probe(Probe{
		Label: "Tap while the issuer is slow", Via: ViaAcquirer, Card: tap, Entry: iso8583.EntryContactless, Amount: 1500,
		Expect: []string{iso8583.RCIssuerUnavailable}, ExpectStep: "issuer", Control: "Issuer timeout", Term: "timeout",
	})
	if res.NetworkTxnID == "" {
		return
	}
	_, ok := r.waitTrace(res.NetworkTxnID, 10*time.Second, func(t networkTrace) bool { return t.Reversed })
	r.check("Network's 0420 acknowledged by First Simple Bank", "Timeout reversal", "timeout-reversal", ok,
		"The issuer might have approved after the network gave up; the reversal makes sure no hold is left behind.")
}

func drillIssuerOutage(r *runner) {
	if !withIssuerMode(r, "UCB", "Union Card Bank", "down") {
		return
	}
	for i := 1; i <= 5; i++ {
		p := Probe{
			Label: fmt.Sprintf("Purchase %d while Union Card Bank is down", i), Via: ViaAcquirer, Card: rewardsCredit,
			Entry: iso8583.EntryEcommerce, Amount: 500, Expect: []string{iso8583.RCIssuerUnavailable},
			Control: "Issuer error handling", Term: "issuer-health",
		}
		// The first three fail at the issuer, unless other traffic already
		// tripped the breaker; from the fourth the breaker must be open.
		if i > 3 {
			p.ExpectStep, p.Control, p.Term = "breaker", "Circuit breaker", "circuit-breaker"
			p.Label += ": breaker open"
		}
		r.probe(p)
	}
}
