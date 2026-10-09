package issuer

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"unicode"

	"github.com/JonahR/simple-network/internal/iso8583"
)

// frictionlessLimit is the amount at or below which the ACS authenticates
// without asking the cardholder anything (a simple risk-based decision).
const frictionlessLimit = 10_000 // $100.00

// maxOTPTries is how many wrong codes end a challenge.
const maxOTPTries = 3

// challenge is a 3-D Secure authentication waiting for the cardholder's code.
type challenge struct {
	pan    string
	amount int64
	otp    string
	tries  int
}

// cavvRecord is an authentication value the ACS issued. It is valid for one
// authorization of that card and amount.
type cavvRecord struct {
	pan    string
	amount int64
	used   bool
}

// Authenticate3DS is the bank's access control server (ACS): it decides
// whether an online shopper is the cardholder. Small amounts are approved
// without friction; larger ones need a one-time code.
func (b *Bank) Authenticate3DS(r iso8583.ThreeDSRequest) iso8583.ThreeDSResult {
	b.mu.Lock()
	defer b.mu.Unlock()
	res := iso8583.ThreeDSResult{ID: r.ID, IssuerName: b.Name}
	acct, _ := b.account(r.PAN, r.Expiry)
	switch {
	case acct == nil:
		res.Status, res.Reason = "N", "No account for this card"
		return res
	case acct.Blocked:
		res.Status, res.Reason = "N", "Card is frozen"
		return res
	case r.Expiry != acct.Expiry:
		res.Status, res.Reason = "N", "Expiry doesn't match"
		return res
	case r.Amount <= frictionlessLimit:
		return b.authenticated(res, r.PAN, r.Amount, "Low amount and a known card: authenticated without asking the cardholder (frictionless)")
	}
	otp := randomDigits(6)
	b.challenges[r.ID] = &challenge{pan: r.PAN, amount: r.Amount, otp: otp}
	res.Status = "C"
	// A real ACS texts the code to the cardholder's phone; the simulation shows it.
	res.Challenge = fmt.Sprintf("%s sent a one-time code to the cardholder's phone. (Simulation: the code is %s.)", b.Name, otp)
	return res
}

// Challenge3DS checks the cardholder's one-time code.
func (b *Bank) Challenge3DS(c iso8583.ThreeDSChallenge) iso8583.ThreeDSResult {
	b.mu.Lock()
	defer b.mu.Unlock()
	res := iso8583.ThreeDSResult{ID: c.ID, IssuerName: b.Name}
	ch, ok := b.challenges[c.ID]
	switch {
	case !ok:
		res.Status, res.Reason = "N", "No challenge in progress for this authentication"
	case strings.TrimSpace(c.OTP) == ch.otp:
		delete(b.challenges, c.ID)
		return b.authenticated(res, ch.pan, ch.amount, "The cardholder entered the right code")
	default:
		ch.tries++
		if ch.tries >= maxOTPTries {
			delete(b.challenges, c.ID)
			res.Status, res.Reason = "N", "Too many wrong codes: authentication failed"
		} else {
			res.Status = "C"
			res.Challenge = fmt.Sprintf("Wrong code. %d tries left. (Simulation: the code is %s.)", maxOTPTries-ch.tries, ch.otp)
		}
	}
	return res
}

// authenticated issues a CAVV for one authorization of pan and amount. The
// caller holds b.mu.
func (b *Bank) authenticated(res iso8583.ThreeDSResult, pan string, amount int64, reason string) iso8583.ThreeDSResult {
	raw := make([]byte, 20)
	rand.Read(raw)
	cavv := base64.StdEncoding.EncodeToString(raw)
	b.cavvs[cavv] = &cavvRecord{pan: pan, amount: amount}
	res.Status, res.ECI, res.CAVV, res.Reason = "Y", "05", cavv, reason
	return res
}

// useCAVV checks that an authorization's CAVV was issued by this ACS for this
// card and amount, and not used before. The caller holds b.mu.
func (b *Bank) useCAVV(req iso8583.AuthRequest) error {
	rec, ok := b.cavvs[req.CAVV]
	switch {
	case !ok:
		return errors.New("CAVV wasn't issued by this bank's ACS")
	case rec.used:
		return errors.New("CAVV was already used for another authorization")
	case rec.pan != req.PAN:
		return errors.New("CAVV was issued for a different card")
	case rec.amount != req.Amount:
		return fmt.Errorf("CAVV was issued for %s, not %s", money(rec.amount, req.Currency), money(req.Amount, req.Currency))
	}
	rec.used = true
	return nil
}

// avsResult compares a billing address with the one on file, the way AVS
// does: the ZIP code, and the house number of the street address.
func avsResult(acct *Account, a iso8583.AVSData) string {
	if acct.BillingZip == "" {
		return "U"
	}
	zip := digitsOnly(a.Zip)
	if len(zip) > 5 {
		zip = zip[:5] // ZIP+4 compares on the first five digits
	}
	zipOK := zip != "" && zip == acct.BillingZip
	streetOK := houseNumber(a.Street) != "" && houseNumber(a.Street) == houseNumber(acct.BillingStreet)
	switch {
	case zipOK && streetOK:
		return "Y"
	case zipOK:
		return "Z"
	case streetOK:
		return "A"
	}
	return "N"
}

func houseNumber(street string) string {
	f := strings.Fields(street)
	if len(f) == 0 {
		return ""
	}
	return digitsOnly(f[0])
}

func digitsOnly(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsDigit(r) {
			return r
		}
		return -1
	}, s)
}

func randomDigits(n int) string {
	var sb strings.Builder
	for range n {
		d, _ := rand.Int(rand.Reader, big.NewInt(10))
		sb.WriteByte(byte('0' + d.Int64()))
	}
	return sb.String()
}
