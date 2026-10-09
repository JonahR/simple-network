// Package demokeys holds the published triple-DES PIN keys the simulation
// uses by default. Each participant shares a key with the network: the POS
// (acting for its acquirer) encrypts under the acquirer key, and the network
// translates PIN blocks into each issuer's key. Override them with env vars.
// They protect nothing; never reuse them outside this simulation.
package demokeys

import (
	"encoding/hex"
	"fmt"
	"os"
)

const (
	AcquirerPIN = "0123456789ABCDEFFEDCBA9876543210"
	IssuerFSB   = "A1B2C3D4E5F60718293A4B5C6D7E8F90"
	IssuerUCB   = "0F1E2D3C4B5A69788796A5B4C3D2E1F0"
)

// Load returns the key in env var name, or def when it is unset. It returns
// an error unless the key is 16 or 24 bytes of hex.
func Load(name, def string) ([]byte, error) {
	v := os.Getenv(name)
	if v == "" {
		v = def
	}
	key, err := hex.DecodeString(v)
	if err != nil || (len(key) != 16 && len(key) != 24) {
		return nil, fmt.Errorf("%s must be 32 or 48 hex characters", name)
	}
	return key, nil
}
