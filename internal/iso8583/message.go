// Package iso8583 models card network messages as JSON, using the field
// numbering of ISO 8583 so the shape can later map onto the binary format.
package iso8583

import "strings"

// Message type indicators.
const (
	MTIAuthRequest      = "0100"
	MTIAuthResponse     = "0110"
	MTIReversalAdvice   = "0420"
	MTIReversalResponse = "0430"
)

// MTINames maps message type indicators to display names.
var MTINames = map[string]string{
	MTIAuthRequest:      "Authorization request",
	MTIAuthResponse:     "Authorization response",
	MTIReversalAdvice:   "Reversal advice",
	MTIReversalResponse: "Reversal advice response",
}

// Processing code (DE3) parts: two digits each for the transaction type,
// the account debited, and the account credited.
const (
	TxnPurchase         = "00"
	TxnPurchaseCashback = "09"
	TxnRefund           = "20"

	AccountDefault  = "00"
	AccountChecking = "20"
	AccountCredit   = "30"
)

// ProcessingCode builds DE3 from a transaction type and the account debited.
func ProcessingCode(txn, fromAccount string) string {
	return txn + fromAccount + AccountDefault
}

// POS entry modes (DE22).
const (
	EntryManual           = "010"
	EntryMagstripe        = "901"
	EntryChip             = "051"
	EntryContactless      = "071"
	EntryEcommerce        = "812"
	EntryCredentialOnFile = "100"
)

// EntryModes maps entry mode codes to display names.
var EntryModes = map[string]string{
	EntryManual:           "Manual key entry",
	EntryMagstripe:        "Magnetic stripe",
	EntryChip:             "Chip (EMV)",
	EntryContactless:      "Contactless",
	EntryEcommerce:        "E-commerce",
	EntryCredentialOnFile: "Credential on file",
}

// Credential-on-file indicators: who started a charge to a stored card.
const (
	COFCustomerInitiated   = "cit"
	COFMerchantRecurring   = "mit_recurring"
	COFMerchantUnscheduled = "mit_unscheduled"
)

// COFIndicators maps credential-on-file indicators to display names.
var COFIndicators = map[string]string{
	COFCustomerInitiated:   "Customer-initiated",
	COFMerchantRecurring:   "Merchant-initiated · recurring",
	COFMerchantUnscheduled: "Merchant-initiated · unscheduled",
}

// Additional amount types (DE54).
const (
	AmountCashback   = "40"
	AmountHealthcare = "4S" // Total amount eligible for HSA/FSA
)

// MCCs maps common merchant category codes (DE18) to display names.
var MCCs = map[string]string{
	"4111": "Commuter transport",
	"4121": "Taxis and rideshares",
	"5310": "Discount stores",
	"5411": "Grocery stores, supermarkets",
	"5541": "Service stations",
	"5542": "Automated fuel dispensers",
	"5812": "Restaurants",
	"5814": "Fast food restaurants",
	"5912": "Drug stores, pharmacies",
	"5999": "Miscellaneous retail",
	"7011": "Hotels, motels, resorts",
	"8011": "Doctors",
	"8062": "Hospitals",
}

// Currency codes (DE49), ISO 4217 numeric.
var Currencies = map[string]string{
	"840": "USD",
	"978": "EUR",
	"826": "GBP",
	"124": "CAD",
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

// AdditionalAmount is one entry in DE54.
type AdditionalAmount struct {
	Type   string `json:"type"`
	Amount int64  `json:"amount"` // Minor units
}

// FleetData carries the prompts a fleet card requires at the pump.
type FleetData struct {
	Odometer  string `json:"odometer"`
	VehicleID string `json:"vehicle_id"`
	DriverID  string `json:"driver_id"`
}

// AuthRequest is an 0100 authorization request.
type AuthRequest struct {
	MTI               string             `json:"mti"`                               // Message type indicator
	PAN               string             `json:"de2_pan"`                           // Primary account number or device token
	ProcessingCode    string             `json:"de3_processing_code"`               // Transaction and account types
	Amount            int64              `json:"de4_amount"`                        // Total in minor units, including cash back
	TransmissionTime  string             `json:"de7_transmission_datetime"`         // MMDDhhmmss, UTC
	STAN              string             `json:"de11_stan"`                         // System trace audit number
	LocalTime         string             `json:"de12_local_time"`                   // hhmmss
	LocalDate         string             `json:"de13_local_date"`                   // MMDD
	Expiry            string             `json:"de14_expiry"`                       // YYMM
	MCC               string             `json:"de18_mcc"`                          // Merchant category code
	EntryMode         string             `json:"de22_pos_entry_mode"`               // How the card was read
	AcquirerID        string             `json:"de32_acquirer_id,omitempty"`        // Acquiring institution
	Track2            string             `json:"de35_track2,omitempty"`             // Magnetic stripe data
	RRN               string             `json:"de37_rrn"`                          // Retrieval reference number
	TerminalID        string             `json:"de41_terminal_id"`                  // Card acceptor terminal ID
	MerchantID        string             `json:"de42_merchant_id"`                  // Card acceptor ID
	MerchantNameLoc   string             `json:"de43_merchant_name_location"`       // Name and location
	Fleet             *FleetData         `json:"de48_fleet,omitempty"`              // Fleet card prompts
	COFIndicator      string             `json:"de48_cof_indicator,omitempty"`      // Who initiated a card-on-file charge
	Currency          string             `json:"de49_currency"`                     // ISO 4217 numeric
	PINData           string             `json:"de52_pin_data,omitempty"`           // Encrypted PIN block
	AdditionalAmounts []AdditionalAmount `json:"de54_additional_amounts,omitempty"` // Cash back, healthcare amounts
	ARQC              string             `json:"de55_arqc,omitempty"`               // Cryptogram from a chip, contactless card, or wallet
	CVV2              string             `json:"cvv2,omitempty"`                    // Card verification value; never logged or stored
	CardholderName    string             `json:"cardholder_name,omitempty"`
	WalletProvider    string             `json:"wallet_provider,omitempty"` // Set when DE2 is a device token, e.g. "apple_pay"

	// Set by the network on the request it forwards to the issuer.
	NetworkTxnID string `json:"network_txn_id,omitempty"`
	Token        string `json:"token,omitempty"` // The device token, when the network replaced it with the card number in DE2
}

// Redacted returns a copy safe for display and logging: the PAN is masked
// (including inside track 2), the PIN block is hidden, and the CVV removed.
func (r AuthRequest) Redacted(mask func(string) string) AuthRequest {
	r.PAN = mask(r.PAN)
	if r.Track2 != "" {
		pan, _, _ := strings.Cut(r.Track2, "=")
		r.Track2 = mask(pan) + "=****"
	}
	if r.PINData != "" {
		r.PINData = "ENCRYPTED"
	}
	r.CVV2 = ""
	if r.Token != "" {
		r.Token = mask(r.Token)
	}
	return r
}

// AuthResponse is an 0110 authorization response.
type AuthResponse struct {
	MTI              string `json:"mti"`
	PAN              string `json:"de2_pan"`
	ProcessingCode   string `json:"de3_processing_code"`
	Amount           int64  `json:"de4_amount"` // Approved amount; less than requested on a partial approval
	TransmissionTime string `json:"de7_transmission_datetime"`
	STAN             string `json:"de11_stan"`
	RRN              string `json:"de37_rrn"`
	AuthCode         string `json:"de38_auth_code,omitempty"` // Issuer's approval code
	ResponseCode     string `json:"de39_response_code"`
	TerminalID       string `json:"de41_terminal_id"`
	MerchantID       string `json:"de42_merchant_id"`
	Currency         string `json:"de49_currency"`
	NetworkTxnID     string `json:"network_txn_id"`
	ResponseText     string `json:"response_text"`
}

// Redacted returns a copy with the PAN masked.
func (r AuthResponse) Redacted(mask func(string) string) AuthResponse {
	r.PAN = mask(r.PAN)
	return r
}

// ReversalAdvice is an 0420 message telling an issuer to release the hold
// for an authorization whose response never reached the acquirer.
type ReversalAdvice struct {
	MTI          string `json:"mti"`
	NetworkTxnID string `json:"network_txn_id"`
	PAN          string `json:"de2_pan"`
	Amount       int64  `json:"de4_amount"`
	STAN         string `json:"de11_stan"`
	RRN          string `json:"de37_rrn"`
	Reason       string `json:"reason"`
}

// ReversalResponse is the issuer's 0430 acknowledgement.
type ReversalResponse struct {
	MTI          string `json:"mti"`
	NetworkTxnID string `json:"network_txn_id"`
	Matched      bool   `json:"matched"` // Whether a hold was found and released
}
