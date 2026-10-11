// Package dispute runs chargebacks (KT 08): an issuer disputes a cleared
// sale for its cardholder, citing a reason code; the acquirer accepts or
// represents with evidence; the issuer accepts or goes to pre-arbitration;
// and the network decides arbitration. Every stage that moves money adds an
// item to the next clearing cycle. Reason codes, their conditions, time
// limits and valid responses are data, as KT 08 recommends.
package dispute

// Evidence is a kind of response an acquirer can make for its merchant.
// DefeatedBy lists the issuer rebuttals that answer it at arbitration.
type Evidence struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	DefeatedBy  []string `json:"defeated_by,omitempty"`
}

// Rebuttal is an issuer's answer to the merchant's evidence at
// pre-arbitration.
type Rebuttal struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Rebuttals lists every issuer rebuttal.
var Rebuttals = []Rebuttal{
	{"wrong_address", "Delivered to a different address", "The delivery address isn't the cardholder's billing or shipping address."},
	{"new_device", "Device new to this account", "The earlier purchases came from a different device or IP address."},
	{"expert_opinion", "Independent assessment", "A third party confirms the item was defective or materially different."},
	{"same_items", "Same items on both receipts", "Both charges list the same goods at the same time."},
	{"cancellation_proof", "Cancellation confirmation", "The cardholder has the merchant's confirmation that the subscription was cancelled."},
	{"different_amount_agreed", "Written quote for less", "The cardholder has the merchant's quote for a lower amount."},
}

// EvidenceTypes lists every kind of evidence.
var EvidenceTypes = []Evidence{
	{"refund_issued", "Credit already issued", "The merchant refunded the cardholder for the disputed amount.", nil},
	{"proof_of_delivery", "Proof of delivery", "Delivery confirmation to the cardholder's address, or proof the service was used.", []string{"wrong_address"}},
	{"as_described", "Item matched its description", "Listing, photos or terms showing the cardholder got what was described.", []string{"expert_opinion"}},
	{"compelling_evidence", "Compelling evidence", "Earlier undisputed purchases by the same cardholder from the same device or account.", []string{"new_device"}},
	{"chip_read", "Chip read at the terminal", "The terminal log shows the chip was read, so the card was genuine.", nil},
	{"correct_amount", "Receipt with the correct amount", "The signed receipt or order matches the amount cleared.", []string{"different_amount_agreed"}},
	{"separate_purchases", "Two separate purchases", "Both charges are for different goods or services.", []string{"same_items"}},
	{"cancellation_not_received", "No cancellation received", "The cardholder never cancelled before the charge, under the agreed terms.", []string{"cancellation_proof"}},
}

// Eligibility rules a reason code can require.
const (
	RuleNone       = ""
	RuleCardAbsent = "card_absent" // Keyed or online; not 3-D Secure authenticated or approved in stand-in
	RuleSwiped     = "swiped"      // Magnetic stripe or fallback, not chip
	RuleDuplicate  = "duplicate"   // Another cleared sale with the same card, merchant and amount that day
	RuleRecurring  = "recurring"   // A merchant-initiated recurring card-on-file charge
)

// Reason is a chargeback reason code.
type Reason struct {
	Code        string   `json:"code"`
	Category    string   `json:"category"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Rule        string   `json:"rule,omitempty"`
	Responses   []string `json:"responses"` // Evidence IDs that can answer it
}

// Reasons follows Visa's four groups (KT 08).
var Reasons = []Reason{
	{"10.4", "Fraud", "Other fraud: card-absent", "The cardholder didn't make this keyed or online purchase.",
		RuleCardAbsent, []string{"compelling_evidence", "refund_issued"}},
	{"10.1", "Fraud", "EMV liability shift counterfeit", "A counterfeit copy of a chip card was swiped at a terminal.",
		RuleSwiped, []string{"chip_read", "refund_issued"}},
	{"12.5", "Processing error", "Incorrect amount", "The cardholder was charged more than they agreed to.",
		RuleNone, []string{"correct_amount", "refund_issued"}},
	{"12.6", "Processing error", "Duplicate processing", "The same purchase was cleared twice.",
		RuleDuplicate, []string{"separate_purchases", "refund_issued"}},
	{"13.1", "Consumer dispute", "Merchandise or services not received", "The cardholder paid but never got what they bought.",
		RuleNone, []string{"proof_of_delivery", "refund_issued"}},
	{"13.2", "Consumer dispute", "Cancelled recurring transaction", "The cardholder cancelled, but the merchant charged again.",
		RuleRecurring, []string{"cancellation_not_received", "refund_issued"}},
	{"13.3", "Consumer dispute", "Not as described or defective", "What arrived wasn't what the merchant described.",
		RuleNone, []string{"as_described", "refund_issued"}},
}

// Time limits in days (KT 08: typical values).
const (
	ChargebackDays     = 120 // From clearing to the first chargeback
	RepresentmentDays  = 30  // For the acquirer to answer a chargeback
	PreArbitrationDays = 30  // For the issuer to answer a representment
	ArbitrationDays    = 30  // For the acquirer to answer pre-arbitration
	ArbitrationFee     = 25_000
)

func reason(code string) (Reason, bool) {
	for _, r := range Reasons {
		if r.Code == code {
			return r, true
		}
	}
	return Reason{}, false
}

func rebuttal(id string) (Rebuttal, bool) {
	for _, r := range Rebuttals {
		if r.ID == id {
			return r, true
		}
	}
	return Rebuttal{}, false
}

func evidence(id string) (Evidence, bool) {
	for _, e := range EvidenceTypes {
		if e.ID == id {
			return e, true
		}
	}
	return Evidence{}, false
}
