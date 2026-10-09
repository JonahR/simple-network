// Package pin builds and encrypts PIN blocks the way a PIN pad does.
//
// The PIN is formatted as an ISO 9564 format 0 block, XORed with the PAN,
// and encrypted under a triple-DES PIN key. Only the encrypted block belongs
// in an authorization request.
package pin

import (
	"crypto/cipher"
	"crypto/des"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

var ErrFormat = errors.New("PIN must be 4-12 digits")

// Validate checks that the PIN is 4-12 digits.
func Validate(pin string) error {
	if len(pin) < 4 || len(pin) > 12 {
		return ErrFormat
	}
	for i := 0; i < len(pin); i++ {
		if pin[i] < '0' || pin[i] > '9' {
			return ErrFormat
		}
	}
	return nil
}

// Format0 returns the clear ISO 9564 format 0 PIN block as 16 hex characters.
func Format0(pin, pan string) (string, error) {
	if err := Validate(pin); err != nil {
		return "", err
	}
	if len(pan) < 13 {
		return "", errors.New("PAN too short for PIN block")
	}
	pinField := fmt.Sprintf("0%X%s", len(pin), pin)
	pinField += strings.Repeat("F", 16-len(pinField))
	// PAN field: four zeros, then the 12 rightmost PAN digits excluding the check digit.
	panField := "0000" + pan[len(pan)-13:len(pan)-1]

	a, _ := hex.DecodeString(pinField)
	b, _ := hex.DecodeString(panField)
	for i := range a {
		a[i] ^= b[i]
	}
	return strings.ToUpper(hex.EncodeToString(a)), nil
}

// Encrypt builds the format 0 block and encrypts it with key, a 16- or 24-byte
// triple-DES key. It returns the encrypted block as 16 hex characters.
func Encrypt(pin, pan string, key []byte) (string, error) {
	block, err := Format0(pin, pan)
	if err != nil {
		return "", err
	}
	c, err := tripleDES(key)
	if err != nil {
		return "", err
	}
	clear, _ := hex.DecodeString(block)
	out := make([]byte, 8)
	c.Encrypt(out, clear)
	return strings.ToUpper(hex.EncodeToString(out)), nil
}

// Decrypt reverses Encrypt and returns the clear format 0 block. The issuer
// (or a hardware security module) does this to verify the PIN.
func Decrypt(encrypted string, key []byte) (string, error) {
	data, err := hex.DecodeString(encrypted)
	if err != nil || len(data) != 8 {
		return "", errors.New("encrypted PIN block must be 16 hex characters")
	}
	c, err := tripleDES(key)
	if err != nil {
		return "", err
	}
	out := make([]byte, 8)
	c.Decrypt(out, data)
	return strings.ToUpper(hex.EncodeToString(out)), nil
}

func tripleDES(key []byte) (cipher.Block, error) {
	switch len(key) {
	case 16: // double-length key K1K2 is used as K1K2K1
		key = append(append([]byte{}, key...), key[:8]...)
	case 24:
	default:
		return nil, errors.New("PIN key must be 16 or 24 bytes")
	}
	return des.NewTripleDESCipher(key)
}
