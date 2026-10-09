// Package iso8583 models card network messages as JSON, using the field
// numbering of ISO 8583 so the shape can later map onto the binary format.
package iso8583

// Message type indicators.
const (
	MTIAuthRequest  = "0100"
	MTIAuthResponse = "0110"
)

// Processing codes (DE3).
const (
	ProcPurchase = "000000"
	ProcRefund   = "200000"
)

// POS entry modes (DE22).
const (
	EntryManual      = "010"
	EntryMagstripe   = "901"
	EntryChip        = "051"
	EntryContactless = "071"
	EntryEcommerce   = "812"
)

// EntryModes maps entry mode codes to display names.
var EntryModes = map[string]string{
	EntryManual:      "Manual key entry",
	EntryMagstripe:   "Magnetic stripe",
	EntryChip:        "Chip (EMV)",
	EntryContactless: "Contactless",
	EntryEcommerce:   "E-commerce",
}

// Currency codes (DE49), ISO 4217 numeric.
var Currencies = map[string]string{
	"840": "USD",
	"978": "EUR",
	"826": "GBP",
	"124": "CAD",
}

// AuthRequest is an 0100 authorization request.
type AuthRequest struct {
	MTI              string `json:"mti"`                         // Message type indicator
	PAN              string `json:"de2_pan"`                     // Primary account number
	ProcessingCode   string `json:"de3_processing_code"`         // Transaction type
	Amount           int64  `json:"de4_amount"`                  // Amount in minor units (cents)
	TransmissionTime string `json:"de7_transmission_datetime"`   // MMDDhhmmss, UTC
	STAN             string `json:"de11_stan"`                   // System trace audit number
	LocalTime        string `json:"de12_local_time"`             // hhmmss
	LocalDate        string `json:"de13_local_date"`             // MMDD
	Expiry           string `json:"de14_expiry"`                 // YYMM
	MCC              string `json:"de18_mcc"`                    // Merchant category code
	EntryMode        string `json:"de22_pos_entry_mode"`         // How the card was read
	RRN              string `json:"de37_rrn"`                    // Retrieval reference number
	TerminalID       string `json:"de41_terminal_id"`            // Card acceptor terminal ID
	MerchantID       string `json:"de42_merchant_id"`            // Card acceptor ID
	MerchantNameLoc  string `json:"de43_merchant_name_location"` // Name and location
	Currency         string `json:"de49_currency"`               // ISO 4217 numeric
	CVV2             string `json:"cvv2,omitempty"`              // Card verification value; never logged or stored
	CardholderName   string `json:"cardholder_name,omitempty"`
	WalletProvider   string `json:"wallet_provider,omitempty"`  // Set when DE2 is a device token, e.g. "apple_pay"
	Cryptogram       string `json:"token_cryptogram,omitempty"` // One-time cryptogram proving the token was used on its device
}

// Mobile wallet providers. Each provisions its own device tokens.
const (
	WalletApplePay   = "apple_pay"
	WalletGooglePay  = "google_pay"
	WalletSamsungPay = "samsung_pay"
)

// Wallets maps wallet provider codes to display names.
var Wallets = map[string]string{
	WalletApplePay:   "Apple Pay",
	WalletGooglePay:  "Google Pay",
	WalletSamsungPay: "Samsung Pay",
}

// Redacted returns a copy safe for display and logging: the PAN is masked
// and the CVV removed.
func (r AuthRequest) Redacted(mask func(string) string) AuthRequest {
	r.PAN = mask(r.PAN)
	r.CVV2 = ""
	return r
}
