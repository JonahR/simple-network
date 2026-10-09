// Package network is the card network switch: it validates authorization
// requests from acquirers, routes them to issuers by BIN, detokenizes wallet
// payments, translates PIN blocks, and records every step it takes.
package network

import "github.com/JonahR/simple-network/internal/card"

// BinEntry is one row of the routing table.
type BinEntry struct {
	Prefix     string       `json:"prefix"` // 6-8 digits; the longest matching prefix wins
	IssuerID   string       `json:"issuer_id,omitempty"`
	Product    card.Product `json:"product,omitempty"`
	Label      string       `json:"label"`
	TokenRange bool         `json:"token_range"` // Numbers here are device tokens; look them up in the vault
}

// BinTable routes card numbers by longest-prefix match.
type BinTable struct {
	Version  string     `json:"version"`
	Entries  []BinEntry `json:"entries"`
	byPrefix map[string]BinEntry
}

// NewBinTable indexes entries for lookup.
func NewBinTable(version string, entries []BinEntry) *BinTable {
	t := &BinTable{Version: version, Entries: entries, byPrefix: map[string]BinEntry{}}
	for _, e := range entries {
		t.byPrefix[e.Prefix] = e
	}
	return t
}

// Lookup returns the entry with the longest prefix (8 down to 6 digits) of pan.
func (t *BinTable) Lookup(pan string) (BinEntry, bool) {
	for n := min(8, len(pan)); n >= 6; n-- {
		if e, ok := t.byPrefix[pan[:n]]; ok {
			return e, true
		}
	}
	return BinEntry{}, false
}

// DefaultBINs is the routing table for the simulation's two issuers and the
// wallet token ranges.
func DefaultBINs() *BinTable {
	return NewBinTable("2026-10-09.1", []BinEntry{
		{Prefix: "424242", IssuerID: "FSB", Product: card.Credit, Label: "Everyday Credit"},
		{Prefix: "400005", IssuerID: "FSB", Product: card.Debit, Label: "Checking Debit"},
		{Prefix: "471600", IssuerID: "FSB", Product: card.Healthcare, Label: "Health Savings"},
		{Prefix: "400000", IssuerID: "FSB", Product: card.Credit, Label: "Test cards"},
		{Prefix: "555555", IssuerID: "UCB", Product: card.Credit, Label: "Rewards Plus"},
		{Prefix: "435880", IssuerID: "UCB", Product: card.Prepaid, Label: "Gift Card"},
		{Prefix: "556800", IssuerID: "UCB", Product: card.Fleet, Label: "Fleet Card"},
		{Prefix: "485932", IssuerID: "UCB", Product: card.Credit, Label: "Pay Later virtual cards"},
		{Prefix: "489537", TokenRange: true, Label: "Wallet tokens"},
		{Prefix: "522093", TokenRange: true, Label: "Wallet tokens"},
	})
}
