package iso8583

// Response codes (DE39).
const (
	RCApproved          = "00"
	RCPartialApproval   = "10"
	RCInvalidMerchant   = "03"
	RCDoNotHonor        = "05"
	RCInvalidCard       = "14"
	RCNoSuchIssuer      = "15"
	RCSuspectedMalfunc  = "22" // Reversal reason: suspected malfunction
	RCFormatError       = "30"
	RCInsufficientFunds = "51"
	RCExpiredCard       = "54"
	RCIncorrectPIN      = "55"
	RCNotPermitted      = "57"
	RCSuspectedFraud    = "59"
	RCRestrictedCard    = "62"
	RCLateResponse      = "68" // Reversal reason: response received too late
	RCIssuerUnavailable = "91"
	RCDuplicate         = "94"
	RCSystemError       = "96"
	RCCVVMismatch       = "N7"
)

// ResponseCode describes a DE39 value.
type ResponseCode struct {
	Text     string `json:"text"`
	Approved bool   `json:"approved"`
	// Source is who normally sends it: "issuer", "network", or "acquirer".
	Source string `json:"source"`
}

// ResponseCodes is the registry of response codes the network understands.
var ResponseCodes = map[string]ResponseCode{
	RCApproved:          {"Approved", true, "issuer"},
	RCPartialApproval:   {"Partial approval", true, "issuer"},
	RCInvalidMerchant:   {"Invalid merchant", false, "acquirer"},
	RCDoNotHonor:        {"Do not honor", false, "issuer"},
	RCInvalidCard:       {"Invalid card number", false, "issuer"},
	RCNoSuchIssuer:      {"No such issuer", false, "network"},
	RCSuspectedMalfunc:  {"Suspected malfunction", false, "network"},
	RCFormatError:       {"Format error", false, "network"},
	RCInsufficientFunds: {"Insufficient funds", false, "issuer"},
	RCExpiredCard:       {"Expired card", false, "issuer"},
	RCIncorrectPIN:      {"Incorrect PIN", false, "issuer"},
	RCNotPermitted:      {"Transaction not permitted to cardholder", false, "issuer"},
	RCSuspectedFraud:    {"Suspected fraud", false, "issuer"},
	RCRestrictedCard:    {"Restricted card", false, "issuer"},
	RCLateResponse:      {"Response received too late", false, "network"},
	RCIssuerUnavailable: {"Issuer or switch unavailable", false, "network"},
	RCDuplicate:         {"Duplicate transmission", false, "network"},
	RCSystemError:       {"System malfunction", false, "network"},
	RCCVVMismatch:       {"CVV2 mismatch", false, "issuer"},
}

// ResponseText returns the description of a response code.
func ResponseText(code string) string {
	if rc, ok := ResponseCodes[code]; ok {
		return rc.Text
	}
	return "Unknown response code"
}

// IsApproved reports whether a response code approves the transaction.
func IsApproved(code string) bool {
	return ResponseCodes[code].Approved
}
