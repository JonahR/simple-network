package main

import "github.com/JonahR/simple-network/internal/merchant"

// Merchant is a business the acquirer has signed up to accept cards. The
// acquirer, not the terminal, is the source of truth for its category code
// and pricing. The POS merchants come from the shared catalog
// (internal/merchant).
type Merchant = merchant.Merchant

// acquirerOnlyMerchants are signed up here but have no POS lane.
var acquirerOnlyMerchants = []Merchant{
	// The Risk lab (cmd/risklab) sends its drills as this merchant's terminal,
	// so they are easy to pick out on every page.
	{ID: "000000000099001", Name: "Risk Lab", City: "San Francisco", Country: "US", MCC: "5999",
		TimeZone: "America/Los_Angeles", Terminals: []string{"RISK0001"}, DiscountBPS: 250},
}
