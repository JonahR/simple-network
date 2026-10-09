package main

import "slices"

// Merchant is a business the acquirer has signed up to accept cards. The
// acquirer, not the terminal, is the source of truth for its category code
// and pricing.
type Merchant struct {
	ID          string   `json:"merchant_id"` // DE42
	Name        string   `json:"name"`
	City        string   `json:"city"`
	Country     string   `json:"country"`
	MCC         string   `json:"mcc"`          // DE18, from the merchant agreement
	Terminals   []string `json:"terminals"`    // DE41 IDs deployed at this merchant
	DiscountBPS int64    `json:"discount_bps"` // Merchant discount rate in basis points (250 = 2.50%)
}

// HasTerminal reports whether the terminal is registered to this merchant.
func (m Merchant) HasTerminal(id string) bool { return slices.Contains(m.Terminals, id) }

// demoMerchants are signed up alongside the merchant configured from the
// environment, so the back office shows more than one account.
var demoMerchants = []Merchant{
	{ID: "000000000023456", Name: "Corner Grocery", City: "Oakland", Country: "US", MCC: "5411",
		Terminals: []string{"TERM0101", "TERM0102"}, DiscountBPS: 190},
	{ID: "000000000034567", Name: "Bayside Fuel", City: "San Jose", Country: "US", MCC: "5542",
		Terminals: []string{"PUMP0001", "PUMP0002", "PUMP0003"}, DiscountBPS: 210},
}
