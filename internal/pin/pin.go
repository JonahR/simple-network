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
	pinField := fmt.Sprintf("0%X%s", len(pin), pin)
	pinField += strings.Repeat("F", 16-len(pinField))
	// PAN field: four zeros, then the 12 rightmost PAN digits excluding the
	// check digit, left-padded with zeros when the PAN is shorter.
	p := strings.Repeat("0", 13) + pan
	panField := "0000" + p[len(p)-13:len(p)-1]

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

// Translate re-encrypts a PIN block from one key to another without exposing
// the PIN, as the network does between the acquirer's and issuer's keys. In
// production this happens inside a hardware security module.
func Translate(encrypted string, from, to []byte) (string, error) {
	clear, err := Decrypt(encrypted, from)
	if err != nil {
		return "", err
	}
	c, err := tripleDES(to)
	if err != nil {
		return "", err
	}
	block, _ := hex.DecodeString(clear)
	out := make([]byte, 8)
	c.Encrypt(out, block)
	return strings.ToUpper(hex.EncodeToString(out)), nil
}

// Extract decrypts a PIN block and recovers the PIN, as the issuer does to
// verify it.
func Extract(encrypted, pan string, key []byte) (string, error) {
	clear, err := Decrypt(encrypted, key)
	if err != nil {
		return "", err
	}
	if len(pan) < 13 {
		return "", errors.New("PAN too short for PIN block")
	}
	a, _ := hex.DecodeString(clear)
	b, _ := hex.DecodeString("0000" + pan[len(pan)-13:len(pan)-1])
	for i := range a {
		a[i] ^= b[i]
	}
	field := strings.ToUpper(hex.EncodeToString(a))
	n := int(a[0] & 0x0F)
	if field[0] != '0' || n < 4 || n > 12 {
		return "", errors.New("PIN block is not format 0")
	}
	pin := field[2 : 2+n]
	if err := Validate(pin); err != nil || strings.Trim(field[2+n:], "F") != "" {
		return "", errors.New("PIN block is malformed")
	}
	return pin, nil
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
