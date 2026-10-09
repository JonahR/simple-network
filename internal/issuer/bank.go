// Package issuer simulates an issuing bank: it owns cardholder accounts,
// decides authorizations, places and releases holds, and verifies PINs.
// It keeps a log of every decision, with each check it ran, for its
// back-office page.
package issuer

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/JonahR/simple-network/internal/card"
	"github.com/JonahR/simple-network/internal/clearing"
	"github.com/JonahR/simple-network/internal/iso8583"
	"github.com/JonahR/simple-network/internal/pin"
)

// Account is one card account at the bank. Amounts are in minor units.
type Account struct {
	ID      string
	PAN     string
	Holder  string
	Product card.Product
	Expiry  string // YYMM
	CVV     string
	PINHash string // sha256(PAN|PIN); empty when the card has no PIN
	Blocked bool   // Frozen by the cardholder or the bank

	// Limit is the credit limit, or the starting balance for debit, prepaid,
	// and HSA/FSA accounts. Available is what can still be authorized: the
	// limit minus holds and posted charges. Posted is what clearing has
	// turned from holds into charges.
	Limit     int64
	Available int64
	Posted    int64
}

// AutoEnroll opens an account the first time the bank sees a card in a BIN
// range, for ranges whose numbers are issued outside this simulation: the
// POS test-card generator and buy-now-pay-later virtual cards.
type AutoEnroll struct {
	BIN     string
	Product card.Product
	Holder  string
	Limit   int64
}

// Check results.
const (
	Pass = "pass"
	Fail = "fail"
	Skip = "skip" // Not applicable to this request
)

// Check is one rule the bank applied to an authorization.
type Check struct {
	Name   string `json:"name"`
	Result string `json:"result"`
	Detail string `json:"detail"`
}

// Decision is the bank's record of one authorization. Card numbers in it
// are masked.
type Decision struct {
	Time            time.Time `json:"time"`
	NetworkTxnID    string    `json:"network_txn_id"`
	AccountID       string    `json:"account_id,omitempty"`
	PAN             string    `json:"pan"`
	Token           string    `json:"token,omitempty"`
	Wallet          string    `json:"wallet,omitempty"`
	Product         string    `json:"product,omitempty"`
	EntryMode       string    `json:"entry_mode"`
	Merchant        string    `json:"merchant"`
	MCC             string    `json:"mcc"`
	Currency        string    `json:"currency"`
	Requested       int64     `json:"requested"`
	Approved        int64     `json:"approved"`
	ResponseCode    string    `json:"response_code"`
	ResponseText    string    `json:"response_text"`
	AuthCode        string    `json:"auth_code,omitempty"`
	Checks          []Check   `json:"checks"`
	Limit           int64     `json:"limit"`
	AvailableBefore int64     `json:"available_before"`
	AvailableAfter  int64     `json:"available_after"`
	Hold            string    `json:"hold"` // "active", "released", "posted", or "none"
}

// Reversal is the bank's record of an 0420.
type Reversal struct {
	Time         time.Time `json:"time"`
	NetworkTxnID string    `json:"network_txn_id"`
	Reason       string    `json:"reason"`
	Matched      bool      `json:"matched"`
	Released     int64     `json:"released"`
	AccountID    string    `json:"account_id,omitempty"`
}

// maxLog caps the decisions and reversals kept in memory.
const maxLog = 500

// Bank is one issuer.
type Bank struct {
	ID     string
	Name   string
	pinKey []byte
	now    func() time.Time

	mu          sync.Mutex
	accounts    map[string]*Account // by PAN
	order       []string            // PANs in the order accounts were opened
	enroll      []AutoEnroll
	holds       map[string]hold     // by network transaction ID
	reversed    map[string]struct{} // reversals that arrived before their authorization
	decisions   []Decision
	reversals   []Reversal
	postedFiles map[string]clearing.IssuerAck // Clearing files already posted, by file ID
}

type hold struct {
	pan    string
	amount int64
}

// NewBank creates a bank with the given accounts and PIN key.
func NewBank(id, name string, pinKey []byte, accounts []Account, enroll []AutoEnroll) *Bank {
	b := &Bank{
		ID:       id,
		Name:     name,
		pinKey:   pinKey,
		now:      time.Now,
		accounts: map[string]*Account{},
		enroll:   enroll,
		holds:    map[string]hold{},
		reversed: map[string]struct{}{},
	}
	for _, a := range accounts {
		a.Available = a.Limit
		b.open(&a)
	}
	return b
}

// open adds an account. The caller holds b.mu or owns b.
func (b *Bank) open(a *Account) {
	if a.ID == "" {
		a.ID = fmt.Sprintf("%s-%03d", strings.ToLower(b.ID), len(b.order)+1)
	}
	b.accounts[a.PAN] = a
	b.order = append(b.order, a.PAN)
}

// HashPIN returns the stored form of a PIN, so the bank never keeps it in the clear.
func HashPIN(pan, p string) string {
	sum := sha256.Sum256([]byte(pan + "|" + p))
	return hex.EncodeToString(sum[:])
}

// Authorize decides an authorization request and, if approved, places a hold.
func (b *Bank) Authorize(req iso8583.AuthRequest) iso8583.AuthResponse {
	b.mu.Lock()
	defer b.mu.Unlock()

	d := Decision{
		Time:         b.now().UTC(),
		NetworkTxnID: req.NetworkTxnID,
		PAN:          card.Mask(req.PAN),
		Wallet:       req.WalletProvider,
		EntryMode:    req.EntryMode,
		Merchant:     strings.TrimSpace(firstN(req.MerchantNameLoc, 25)),
		MCC:          req.MCC,
		Currency:     req.Currency,
		Requested:    req.Amount,
		Hold:         "none",
	}
	if req.Token != "" {
		d.Token = card.Mask(req.Token)
	}
	acct, code, approved := b.decide(req, &d)

	resp := iso8583.AuthResponse{
		MTI:              iso8583.MTIAuthResponse,
		PAN:              req.PAN,
		ProcessingCode:   req.ProcessingCode,
		Amount:           req.Amount, // A decline echoes the requested amount
		TransmissionTime: req.TransmissionTime,
		STAN:             req.STAN,
		RRN:              req.RRN,
		ResponseCode:     code,
		TerminalID:       req.TerminalID,
		MerchantID:       req.MerchantID,
		Currency:         req.Currency,
		NetworkTxnID:     req.NetworkTxnID,
		ResponseText:     iso8583.ResponseText(code),
	}
	if acct != nil {
		d.AccountID, d.Product, d.Limit = acct.ID, string(acct.Product), acct.Limit
		d.AvailableBefore = acct.Available
	}
	if iso8583.IsApproved(code) {
		resp.AuthCode = authCode()
		resp.Amount = approved // Less than requested on a partial approval
		acct.Available -= approved
		b.holds[req.NetworkTxnID] = hold{pan: req.PAN, amount: approved}
		d.Hold = "active"
	}
	if acct != nil {
		d.AvailableAfter = acct.Available
	}
	if iso8583.IsApproved(code) {
		d.Approved = approved // A decline's DE4 echoes the request, but nothing was approved
	}
	d.ResponseCode, d.ResponseText, d.AuthCode = code, resp.ResponseText, resp.AuthCode
	b.decisions = append(b.decisions, d)
	if len(b.decisions) > maxLog {
		b.decisions = b.decisions[1:]
	}
	return resp
}

// decide runs the bank's checks in order, recording each one in d. It
// returns the account (if found), the response code, and the amount to
// approve. The caller holds b.mu.
func (b *Bank) decide(req iso8583.AuthRequest, d *Decision) (*Account, string, int64) {
	check := func(name, result, detail string) {
		d.Checks = append(d.Checks, Check{Name: name, Result: result, Detail: detail})
	}
	fail := func(name, detail, code string) (string, int64) {
		check(name, Fail, detail)
		return code, 0
	}

	if _, ok := b.reversed[req.NetworkTxnID]; ok {
		// The network already gave up on this authorization; never hold for it.
		code, amt := fail("Reversal", "The network already sent an 0420 for this authorization, so no hold is placed", iso8583.RCDoNotHonor)
		return nil, code, amt
	}

	acct, opened := b.account(req.PAN, req.Expiry)
	if acct == nil {
		code, amt := fail("Account", "No account for "+card.Mask(req.PAN), iso8583.RCInvalidCard)
		return nil, code, amt
	}
	detail := fmt.Sprintf("%s · %s · %s", acct.ID, card.ProductNames[acct.Product], acct.Holder)
	if opened {
		detail += " (opened on first use for this BIN range)"
	}
	check("Account", Pass, detail)

	if acct.Blocked {
		code, amt := fail("Card status", "Card is frozen", iso8583.RCRestrictedCard)
		return acct, code, amt
	}
	check("Card status", Pass, "Active")

	if req.Expiry != acct.Expiry {
		code, amt := fail("Expiry", fmt.Sprintf("Sent %s, card expires %s", yymm(req.Expiry), yymm(acct.Expiry)), iso8583.RCExpiredCard)
		return acct, code, amt
	}
	check("Expiry", Pass, "Matches "+yymm(acct.Expiry))

	switch {
	case req.CVV2 == "":
		check("CVV2", Skip, "Not sent: the card or phone was read, or the card is on file")
	case acct.CVV == "":
		check("CVV2", Skip, "No CVV on record for this card")
	case req.CVV2 != acct.CVV:
		code, amt := fail("CVV2", "Does not match the card", iso8583.RCCVVMismatch)
		return acct, code, amt
	default:
		check("CVV2", Pass, "Matches the card")
	}

	if req.PINData == "" {
		check("PIN", Skip, "No PIN entered")
	} else {
		p, err := pin.Extract(req.PINData, req.PAN, b.pinKey)
		if err != nil || acct.PINHash == "" || HashPIN(req.PAN, p) != acct.PINHash {
			code, amt := fail("PIN", fmt.Sprintf("Decrypted the PIN block under the %s key; the PIN is wrong", b.ID), iso8583.RCIncorrectPIN)
			return acct, code, amt
		}
		check("PIN", Pass, fmt.Sprintf("Decrypted under the %s key and verified", b.ID))
	}

	var cashback, healthcare int64
	for _, a := range req.AdditionalAmounts {
		switch a.Type {
		case iso8583.AmountCashback:
			cashback = a.Amount
		case iso8583.AmountHealthcare:
			healthcare = a.Amount
		}
	}
	amount := req.Amount
	partial := false
	switch {
	case cashback > 0 && acct.Product != card.Debit:
		code, amt := fail("Product rules", "Cash back is only allowed on debit cards", iso8583.RCNotPermitted)
		return acct, code, amt
	case acct.Product == card.Healthcare && healthcare <= 0:
		code, amt := fail("Product rules", "HSA/FSA cards need an eligible healthcare amount (DE54)", iso8583.RCNotPermitted)
		return acct, code, amt
	case acct.Product == card.Healthcare && healthcare < amount:
		// HSA/FSA cards pay only for eligible items (IIAS).
		amount, partial = healthcare, true
		check("Product rules", Pass, fmt.Sprintf("HSA/FSA: %s of %s is eligible", money(healthcare, req.Currency), money(req.Amount, req.Currency)))
	case cashback > 0:
		check("Product rules", Pass, fmt.Sprintf("Debit: %s cash back included", money(cashback, req.Currency)))
	default:
		check("Product rules", Pass, "No restrictions for "+strings.ToLower(card.ProductNames[acct.Product]))
	}

	if amount > acct.Available {
		// Prepaid and HSA/FSA cards approve what is left; the cardholder pays
		// the rest another way.
		if (acct.Product == card.Prepaid || acct.Product == card.Healthcare) && acct.Available > 0 && cashback == 0 {
			check("Funds", Pass, fmt.Sprintf("Only %s available of %s: partial approval", money(acct.Available, req.Currency), money(amount, req.Currency)))
			return acct, iso8583.RCPartialApproval, acct.Available
		}
		code, amt := fail("Funds", fmt.Sprintf("%s available, %s needed", money(acct.Available, req.Currency), money(amount, req.Currency)), iso8583.RCInsufficientFunds)
		return acct, code, amt
	}
	check("Funds", Pass, fmt.Sprintf("%s available, %s held", money(acct.Available, req.Currency), money(amount, req.Currency)))
	if partial {
		return acct, iso8583.RCPartialApproval, amount
	}
	return acct, iso8583.RCApproved, amount
}

// account finds the card's account, opening one for auto-enroll ranges.
// Those numbers were issued outside the simulation, so the first expiry
// seen becomes the account's expiry. The caller holds b.mu.
func (b *Bank) account(pan, expiry string) (acct *Account, opened bool) {
	if a, ok := b.accounts[pan]; ok {
		return a, false
	}
	for _, e := range b.enroll {
		if strings.HasPrefix(pan, e.BIN) {
			a := &Account{PAN: pan, Product: e.Product, Holder: e.Holder, Expiry: expiry, Limit: e.Limit, Available: e.Limit}
			b.open(a)
			return a, true
		}
	}
	return nil, false
}

// Reverse releases the hold for an authorization. A reversal that arrives
// before its authorization is remembered, so the late authorization is
// declined instead of holding funds nobody will collect.
func (b *Bank) Reverse(adv iso8583.ReversalAdvice) iso8583.ReversalResponse {
	b.mu.Lock()
	defer b.mu.Unlock()
	resp := iso8583.ReversalResponse{MTI: iso8583.MTIReversalResponse, NetworkTxnID: adv.NetworkTxnID,
		STAN: adv.STAN, ResponseCode: iso8583.RCApproved} // 00: advice received
	rev := Reversal{Time: b.now().UTC(), NetworkTxnID: adv.NetworkTxnID, Reason: reversalReason(adv)}
	if h, ok := b.holds[adv.NetworkTxnID]; ok {
		acct := b.accounts[h.pan]
		acct.Available += h.amount
		delete(b.holds, adv.NetworkTxnID)
		resp.Matched = true
		rev.Matched, rev.Released, rev.AccountID = true, h.amount, acct.ID
		for i := range b.decisions {
			if b.decisions[i].NetworkTxnID == adv.NetworkTxnID {
				b.decisions[i].Hold = "released"
			}
		}
	} else {
		b.reversed[adv.NetworkTxnID] = struct{}{}
	}
	b.reversals = append(b.reversals, rev)
	if len(b.reversals) > maxLog {
		b.reversals = b.reversals[1:]
	}
	return resp
}

// SetBlocked freezes or unfreezes an account. It reports whether the account exists.
func (b *Bank) SetBlocked(accountID string, blocked bool) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, a := range b.accounts {
		if a.ID == accountID {
			a.Blocked = blocked
			return true
		}
	}
	return false
}

// Snapshot returns a copy of an account, for tests and display.
func (b *Bank) Snapshot(pan string) (Account, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	a, ok := b.accounts[pan]
	if !ok {
		return Account{}, false
	}
	return *a, true
}

// AccountView is an account as the back office shows it: masked, with its holds.
type AccountView struct {
	ID        string `json:"id"`
	PAN       string `json:"pan"`
	Holder    string `json:"holder"`
	Product   string `json:"product"`
	Expiry    string `json:"expiry"`
	HasPIN    bool   `json:"has_pin"`
	Blocked   bool   `json:"blocked"`
	Limit     int64  `json:"limit"`
	Available int64  `json:"available"`
	Held      int64  `json:"held"`
	Holds     int    `json:"holds"`
	Posted    int64  `json:"posted"`
}

// State is everything the back office shows, copied under the lock.
type State struct {
	Accounts  []AccountView `json:"accounts"`
	Decisions []Decision    `json:"decisions"` // Newest first
	Reversals []Reversal    `json:"reversals"` // Newest first
}

// State returns a masked copy of the bank's accounts and logs.
func (b *Bank) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	held := map[string][2]int64{} // PAN -> amount, count
	for _, h := range b.holds {
		v := held[h.pan]
		held[h.pan] = [2]int64{v[0] + h.amount, v[1] + 1}
	}
	// Empty lists, not nil: the page reads them as JSON arrays.
	s := State{Accounts: []AccountView{}, Decisions: []Decision{}, Reversals: []Reversal{}}
	for _, pan := range b.order {
		a := b.accounts[pan]
		s.Accounts = append(s.Accounts, AccountView{
			ID: a.ID, PAN: card.Mask(a.PAN), Holder: a.Holder, Product: string(a.Product), Expiry: yymm(a.Expiry),
			HasPIN: a.PINHash != "", Blocked: a.Blocked, Limit: a.Limit, Available: a.Available,
			Held: held[pan][0], Holds: int(held[pan][1]), Posted: a.Posted,
		})
	}
	s.Decisions = append(s.Decisions, b.decisions...)
	slices.Reverse(s.Decisions)
	for i := range s.Decisions {
		s.Decisions[i].Checks = slices.Clone(s.Decisions[i].Checks)
	}
	s.Reversals = append(s.Reversals, b.reversals...)
	slices.Reverse(s.Reversals)
	return s
}

// yymm formats a YYMM expiry as MM/YY.
func yymm(e string) string {
	if len(e) != 4 {
		return e
	}
	return e[2:] + "/" + e[:2]
}

// symbols prefixes amounts in check details by ISO 4217 numeric currency.
var symbols = map[string]string{"840": "$", "978": "€", "826": "£", "124": "CA$"}

func money(minor int64, currency string) string {
	sym, ok := symbols[currency]
	if !ok {
		sym = iso8583.Currencies[currency] + " "
	}
	return fmt.Sprintf("%s%d.%02d", sym, minor/100, minor%100)
}

func firstN(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func authCode() string {
	const chars = "ABCDEFGHJKLMNPQRSTUVWXYZ0123456789"
	buf := make([]byte, 6)
	rand.Read(buf)
	for i := range buf {
		buf[i] = chars[int(buf[i])%len(chars)]
	}
	return string(buf)
}

// reversalReason describes why the network reversed: the DE39 reason code
// and its meaning, e.g. "68 Response received too late".
func reversalReason(adv iso8583.ReversalAdvice) string {
	if adv.ResponseCode == "" {
		return ""
	}
	return adv.ResponseCode + " " + iso8583.ResponseText(adv.ResponseCode)
}
