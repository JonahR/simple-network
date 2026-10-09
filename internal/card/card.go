// Package card provides helpers for primary account numbers (PANs):
// Luhn validation, masking, expiry checks, and test card generation.
package card

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"
)

const (
	MinPANLength = 12
	MaxPANLength = 19
	BINLength    = 6
)

var (
	ErrPANFormat = errors.New("card number must be 12-19 digits")
	ErrLuhn      = errors.New("card number fails Luhn check")
	ErrExpiry    = errors.New("expiry must be MM/YY")
	ErrExpired   = errors.New("card is expired")
	ErrCVV       = errors.New("CVV must be 3 or 4 digits")
)

// Normalize strips spaces and dashes from a user-entered card number.
func Normalize(pan string) string {
	return strings.NewReplacer(" ", "", "-", "").Replace(pan)
}

// ValidatePAN checks length, digits, and the Luhn check digit.
func ValidatePAN(pan string) error {
	if len(pan) < MinPANLength || len(pan) > MaxPANLength || !isDigits(pan) {
		return ErrPANFormat
	}
	if !Luhn(pan) {
		return ErrLuhn
	}
	return nil
}

// Luhn reports whether the digit string has a valid Luhn check digit.
func Luhn(digits string) bool {
	return luhnSum(digits)%10 == 0
}

func luhnSum(digits string) int {
	sum := 0
	double := false
	for i := len(digits) - 1; i >= 0; i-- {
		d := int(digits[i] - '0')
		if double {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		double = !double
	}
	return sum
}

// BIN returns the bank identification number (first six digits).
func BIN(pan string) string {
	if len(pan) < BINLength {
		return pan
	}
	return pan[:BINLength]
}

// Mask hides all but the BIN and last four digits, e.g. 424242******4242.
func Mask(pan string) string {
	if len(pan) <= BINLength+4 {
		return strings.Repeat("*", len(pan))
	}
	return pan[:BINLength] + strings.Repeat("*", len(pan)-BINLength-4) + pan[len(pan)-4:]
}

// ParseExpiry parses "MM/YY" (or "MMYY") and returns the ISO 8583 YYMM form.
// It rejects cards whose expiry month has already ended as of now.
func ParseExpiry(s string, now time.Time) (string, error) {
	s = strings.ReplaceAll(strings.TrimSpace(s), "/", "")
	if len(s) != 4 || !isDigits(s) {
		return "", ErrExpiry
	}
	month := int(s[0]-'0')*10 + int(s[1]-'0')
	year := 2000 + int(s[2]-'0')*10 + int(s[3]-'0')
	if month < 1 || month > 12 {
		return "", ErrExpiry
	}
	// A card is valid through the last day of its expiry month.
	firstOfNextMonth := time.Date(year, time.Month(month)+1, 1, 0, 0, 0, 0, now.Location())
	if !now.Before(firstOfNextMonth) {
		return "", ErrExpired
	}
	return s[2:] + s[:2], nil
}

// ValidateCVV checks that the CVV is 3 or 4 digits.
func ValidateCVV(cvv string) error {
	if (len(cvv) != 3 && len(cvv) != 4) || !isDigits(cvv) {
		return ErrCVV
	}
	return nil
}

// Generate returns a random Luhn-valid PAN of the given length starting with bin.
func Generate(bin string, length int) (string, error) {
	if !isDigits(bin) || len(bin) >= length || length > MaxPANLength {
		return "", fmt.Errorf("invalid bin %q for length %d", bin, length)
	}
	var b strings.Builder
	b.WriteString(bin)
	for b.Len() < length-1 {
		b.WriteByte(byte('0' + rand.IntN(10)))
	}
	body := b.String()
	// Choose the check digit that makes the full number pass Luhn.
	check := (10 - luhnSum(body+"0")%10) % 10
	return body + string(rune('0'+check)), nil
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
