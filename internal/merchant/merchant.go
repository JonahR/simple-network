// Package merchant is the list of businesses signed up with the acquirer and
// the POS terminal each one uses. The POS offers them in its merchant picker
// and the acquirer onboards the same list, so every terminal the POS can be
// switched to is one the acquirer recognizes.
package merchant

import (
	"slices"
	"strconv"
)

// Merchant is one business and its terminals.
type Merchant struct {
	ID          string   `json:"merchant_id"` // DE42, 15 digits
	Name        string   `json:"name"`
	City        string   `json:"city"`
	Country     string   `json:"country"`
	MCC         string   `json:"mcc"`          // DE18, from the merchant agreement
	TimeZone    string   `json:"time_zone"`    // IANA zone for DE12/DE13 local time
	Terminals   []string `json:"terminals"`    // DE41 IDs, 8 characters; the first is the POS lane
	DiscountBPS int64    `json:"discount_bps"` // Merchant discount rate in basis points (250 = 2.50%)
}

// HasTerminal reports whether the terminal is registered to this merchant.
func (m Merchant) HasTerminal(id string) bool { return slices.Contains(m.Terminals, id) }

// Catalog is every merchant with a POS terminal, in picker order. The first
// is the default and can be reconfigured from the environment (FromEnv).
// They span merchant categories, so the POS prompts and clearing tolerances
// get exercised, and US time zones, so local transaction times differ.
var Catalog = []Merchant{
	{ID: "000000000012345", Name: "Simple Coffee Co", City: "San Francisco", Country: "US", MCC: "5814",
		TimeZone: "America/Los_Angeles", Terminals: []string{"TERM0001"}, DiscountBPS: 250},
	{ID: "000000000023456", Name: "Corner Grocery", City: "Oakland", Country: "US", MCC: "5411",
		TimeZone: "America/Los_Angeles", Terminals: []string{"TERM0101", "TERM0102"}, DiscountBPS: 190},
	{ID: "000000000034567", Name: "Bayside Fuel", City: "San Jose", Country: "US", MCC: "5542",
		TimeZone: "America/Los_Angeles", Terminals: []string{"PUMP0001", "PUMP0002", "PUMP0003"}, DiscountBPS: 210},
	{ID: "000000000045678", Name: "Harbor Pharmacy", City: "Seattle", Country: "US", MCC: "5912",
		TimeZone: "America/Los_Angeles", Terminals: []string{"PHRM0001"}, DiscountBPS: 220},
	{ID: "000000000056789", Name: "Blue Door Bistro", City: "Chicago", Country: "US", MCC: "5812",
		TimeZone: "America/Chicago", Terminals: []string{"BSTR0001"}, DiscountBPS: 260},
	{ID: "000000000067890", Name: "Summit Lodge", City: "Denver", Country: "US", MCC: "7011",
		TimeZone: "America/Denver", Terminals: []string{"HOTL0001"}, DiscountBPS: 280},
	{ID: "000000000078901", Name: "Desert Ride Taxi", City: "Phoenix", Country: "US", MCC: "4121",
		TimeZone: "America/Phoenix", Terminals: []string{"TAXI0001"}, DiscountBPS: 300},
	{ID: "000000000089012", Name: "Metro Transit", City: "New York", Country: "US", MCC: "4111",
		TimeZone: "America/New_York", Terminals: []string{"TRNS0001"}, DiscountBPS: 150},
	{ID: "000000000090123", Name: "Northside Clinic", City: "Boston", Country: "US", MCC: "8011",
		TimeZone: "America/New_York", Terminals: []string{"CLNC0001"}, DiscountBPS: 240},
	{ID: "000000000101234", Name: "Pixel Electronics", City: "Austin", Country: "US", MCC: "5732",
		TimeZone: "America/Chicago", Terminals: []string{"ELEC0001"}, DiscountBPS: 230},
	{ID: "000000000112345", Name: "Thread & Needle", City: "Miami", Country: "US", MCC: "5651",
		TimeZone: "America/New_York", Terminals: []string{"APRL0001"}, DiscountBPS: 250},
	{ID: "000000000123456", Name: "Aloha Surf Shop", City: "Honolulu", Country: "US", MCC: "5941",
		TimeZone: "Pacific/Honolulu", Terminals: []string{"SURF0001"}, DiscountBPS: 270},
	{ID: "000000000134567", Name: "Northern Lights Books", City: "Anchorage", Country: "US", MCC: "5942",
		TimeZone: "America/Anchorage", Terminals: []string{"BOOK0001"}, DiscountBPS: 240},
}

// FromEnv returns the catalog with its first (default) merchant reconfigured
// from the environment: MERCHANT_ID, TERMINAL_ID, MERCHANT_NAME,
// MERCHANT_CITY, MERCHANT_COUNTRY, MCC, MERCHANT_TZ and MERCHANT_DISCOUNT_BPS.
// getenv returns "" for an unset variable. The POS and the acquirer both call
// it with the same settings, so they agree on the default merchant.
func FromEnv(getenv func(string) string) []Merchant {
	out := slices.Clone(Catalog)
	m := out[0]
	m.Terminals = slices.Clone(m.Terminals)
	set := func(key string, field *string) {
		if v := getenv(key); v != "" {
			*field = v
		}
	}
	set("MERCHANT_ID", &m.ID)
	set("TERMINAL_ID", &m.Terminals[0])
	set("MERCHANT_NAME", &m.Name)
	set("MERCHANT_CITY", &m.City)
	set("MERCHANT_COUNTRY", &m.Country)
	set("MCC", &m.MCC)
	set("MERCHANT_TZ", &m.TimeZone)
	if bps, err := strconv.ParseInt(getenv("MERCHANT_DISCOUNT_BPS"), 10, 64); err == nil {
		m.DiscountBPS = bps
	}
	out[0] = m
	return out
}
