// Package issuer simulates an issuing bank: it owns cardholder accounts,
// decides authorizations, places and releases holds, and verifies PINs.
package issuer

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"

	"github.com/JonahR/simple-network/internal/card"
	"github.com/JonahR/simple-network/internal/iso8583"
	"github.com/JonahR/simple-network/internal/pin"
)

// Account is one card account at the bank. Amounts are in minor units.
type Account struct {
	PAN     string
	Product card.Product
	Expiry  string // YYMM
	CVV     string
	PINHash string // sha256(PAN|PIN); empty when the card has no PIN
	Blocked bool

	// Limit is the credit limit, or the starting balance for debit, prepaid,
	// and HSA/FSA accounts. Available is what can still be authorized.
	Limit     int64
	Available int64
}

// AutoEnroll opens an account the first time the bank sees a card in a BIN
// range, for ranges whose numbers are issued outside this simulation: the
// POS test-card generator and buy-now-pay-later virtual cards.
type AutoEnroll struct {
	BIN     string
	Product card.Product
	Limit   int64
}

// Bank is one issuer.
type Bank struct {
	ID     string
	Name   string
	pinKey []byte

	mu       sync.Mutex
	accounts map[string]*Account
	enroll   []AutoEnroll
	holds    map[string]hold     // by network transaction ID
	reversed map[string]struct{} // reversals that arrived before their authorization
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
		accounts: map[string]*Account{},
		enroll:   enroll,
		holds:    map[string]hold{},
		reversed: map[string]struct{}{},
	}
	for _, a := range accounts {
		a.Available = a.Limit
		b.accounts[a.PAN] = &a
	}
	return b
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

	code, approved := b.decide(req)
	resp := iso8583.AuthResponse{
		MTI:              iso8583.MTIAuthResponse,
		PAN:              req.PAN,
		ProcessingCode:   req.ProcessingCode,
		Amount:           approved,
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
	if iso8583.IsApproved(code) {
		resp.AuthCode = authCode()
		b.accounts[req.PAN].Available -= approved
		b.holds[req.NetworkTxnID] = hold{pan: req.PAN, amount: approved}
	}
	return resp
}

// decide returns the response code and the amount to approve. The caller holds b.mu.
func (b *Bank) decide(req iso8583.AuthRequest) (string, int64) {
	if _, ok := b.reversed[req.NetworkTxnID]; ok {
		// The network already gave up on this authorization; never hold for it.
		return iso8583.RCDoNotHonor, 0
	}
	acct := b.account(req.PAN, req.Expiry)
	switch {
	case acct == nil:
		return iso8583.RCInvalidCard, 0
	case acct.Blocked:
		return iso8583.RCRestrictedCard, 0
	case req.Expiry != acct.Expiry:
		return iso8583.RCExpiredCard, 0
	case req.CVV2 != "" && acct.CVV != "" && req.CVV2 != acct.CVV:
		return iso8583.RCCVVMismatch, 0
	}

	if req.PINData != "" {
		p, err := pin.Extract(req.PINData, req.PAN, b.pinKey)
		if err != nil || acct.PINHash == "" || HashPIN(req.PAN, p) != acct.PINHash {
			return iso8583.RCIncorrectPIN, 0
		}
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
	if cashback > 0 && acct.Product != card.Debit {
		return iso8583.RCNotPermitted, 0
	}

	amount := req.Amount
	partial := false
	// HSA/FSA cards pay only for eligible items; approve that part (IIAS).
	if acct.Product == card.Healthcare {
		if healthcare <= 0 {
			return iso8583.RCNotPermitted, 0
		}
		if healthcare < amount {
			amount, partial = healthcare, true
		}
	}
	if amount > acct.Available {
		// Prepaid cards approve what is left on the card; the cardholder pays
		// the rest another way.
		if (acct.Product == card.Prepaid || acct.Product == card.Healthcare) && acct.Available > 0 && cashback == 0 {
			return iso8583.RCPartialApproval, acct.Available
		}
		return iso8583.RCInsufficientFunds, 0
	}
	if partial {
		return iso8583.RCPartialApproval, amount
	}
	return iso8583.RCApproved, amount
}

// account finds the card's account, opening one for auto-enroll ranges.
// Those numbers were issued outside the simulation, so the first expiry
// seen becomes the account's expiry.
func (b *Bank) account(pan, expiry string) *Account {
	if a, ok := b.accounts[pan]; ok {
		return a
	}
	for _, e := range b.enroll {
		if strings.HasPrefix(pan, e.BIN) {
			a := &Account{PAN: pan, Product: e.Product, Expiry: expiry, Limit: e.Limit, Available: e.Limit}
			b.accounts[pan] = a
			return a
		}
	}
	return nil
}

// Reverse releases the hold for an authorization. A reversal that arrives
// before its authorization is remembered, so the late authorization is
// declined instead of holding funds nobody will collect.
func (b *Bank) Reverse(adv iso8583.ReversalAdvice) iso8583.ReversalResponse {
	b.mu.Lock()
	defer b.mu.Unlock()
	resp := iso8583.ReversalResponse{MTI: iso8583.MTIReversalResponse, NetworkTxnID: adv.NetworkTxnID}
	if h, ok := b.holds[adv.NetworkTxnID]; ok {
		b.accounts[h.pan].Available += h.amount
		delete(b.holds, adv.NetworkTxnID)
		resp.Matched = true
	} else {
		b.reversed[adv.NetworkTxnID] = struct{}{}
	}
	return resp
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

func authCode() string {
	const chars = "ABCDEFGHJKLMNPQRSTUVWXYZ0123456789"
	buf := make([]byte, 6)
	rand.Read(buf)
	for i := range buf {
		buf[i] = chars[int(buf[i])%len(chars)]
	}
	return string(buf)
}
