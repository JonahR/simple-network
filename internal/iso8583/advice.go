package iso8583

// Authorization advice: the network tells an issuer about an authorization
// it approved on the issuer's behalf (stand-in processing) while the issuer
// could not answer. The issuer must honor it and place the hold.
const (
	MTIAuthAdvice         = "0120"
	MTIAuthAdviceResponse = "0130"
)

// AuthAdvice is an 0120. It carries a one-way hash of the card number, not
// the number itself, so queued advices can be stored safely; the issuer
// finds the account by hash.
type AuthAdvice struct {
	MTI             string `json:"mti"`
	NetworkTxnID    string `json:"network_txn_id"`
	PAN             string `json:"de2_pan"` // Masked
	PANHash         string `json:"pan_sha256"`
	Expiry          string `json:"de14_expiry"`
	Amount          int64  `json:"de4_amount"`
	Currency        string `json:"de49_currency"`
	STAN            string `json:"de11_stan"`
	RRN             string `json:"de37_rrn"`
	AuthCode        string `json:"de38_auth_code"` // The network's approval code
	ResponseCode    string `json:"de39_response_code"`
	MCC             string `json:"de18_mcc"`
	EntryMode       string `json:"de22_pos_entry_mode"`
	MerchantNameLoc string `json:"de43_merchant_name_location"`
	Reason          string `json:"reason"` // Why the network stood in
}

// AuthAdviceResponse is the issuer's 0130 acknowledgement.
type AuthAdviceResponse struct {
	MTI          string `json:"mti"`
	NetworkTxnID string `json:"network_txn_id"`
	Held         bool   `json:"held"`           // A hold is in place for the approval
	Note         string `json:"note,omitempty"` // Why not, or why it was already there
}
