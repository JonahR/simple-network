package clearing

import (
	"slices"

	"github.com/JonahR/simple-network/internal/card"
	"github.com/JonahR/simple-network/internal/iso8583"
)

// Channel is whether the card was present at the sale.
type Channel string

const (
	CardPresent    Channel = "card_present"
	CardNotPresent Channel = "card_not_present"
)

// ChannelOf classifies a POS entry mode (DE22).
func ChannelOf(entryMode string) Channel {
	switch entryMode {
	case iso8583.EntryChip, iso8583.EntryContactless, iso8583.EntryMagstripe:
		return CardPresent
	default:
		return CardNotPresent
	}
}

// Program is one interchange program: a rate for the transactions it matches.
// Empty match lists match anything.
type Program struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Products   []card.Product `json:"products,omitempty"`
	Channels   []Channel      `json:"channels,omitempty"`
	RateBPS    int64          `json:"rate_bps"`    // Basis points of the amount (150 = 1.50%)
	FixedMinor int64          `json:"fixed_minor"` // Plus a fixed amount per transaction
}

func (p Program) matches(product card.Product, ch Channel) bool {
	return (len(p.Products) == 0 || slices.Contains(p.Products, product)) &&
		(len(p.Channels) == 0 || slices.Contains(p.Channels, ch))
}

// FeeTable is the network's versioned fee schedule (D12): ordered interchange
// programs, where the first match wins, plus network fees on both sides.
type FeeTable struct {
	Version               string    `json:"version"`
	Programs              []Program `json:"programs"`
	AcquirerAssessmentBPS int64     `json:"acquirer_assessment_bps"`
	AcquirerPerItemMinor  int64     `json:"acquirer_per_item_minor"`
	IssuerAssessmentBPS   int64     `json:"issuer_assessment_bps"`
}

// DefaultFees is a small table in the shape KT 06 recommends.
func DefaultFees() FeeTable {
	cp, cnp := []Channel{CardPresent}, []Channel{CardNotPresent}
	return FeeTable{
		Version: "2026-10-01",
		Programs: []Program{
			{ID: "DEBIT_CP", Name: "Debit, card present", Products: []card.Product{card.Debit}, Channels: cp, RateBPS: 80, FixedMinor: 15},
			{ID: "DEBIT_CNP", Name: "Debit, card not present", Products: []card.Product{card.Debit}, Channels: cnp, RateBPS: 165, FixedMinor: 15},
			{ID: "PREPAID", Name: "Prepaid and HSA/FSA", Products: []card.Product{card.Prepaid, card.Healthcare}, RateBPS: 115, FixedMinor: 15},
			{ID: "COMMERCIAL_FLEET", Name: "Commercial fleet", Products: []card.Product{card.Fleet}, RateBPS: 250, FixedMinor: 10},
			{ID: "CP_CREDIT_STD", Name: "Credit, card present", Products: []card.Product{card.Credit}, Channels: cp, RateBPS: 150, FixedMinor: 10},
			{ID: "CNP_CREDIT_STD", Name: "Credit, card not present", Products: []card.Product{card.Credit}, Channels: cnp, RateBPS: 180, FixedMinor: 10},
			{ID: "DEFAULT", Name: "Standard", RateBPS: 200, FixedMinor: 10},
		},
		AcquirerAssessmentBPS: 14,
		AcquirerPerItemMinor:  2,
		IssuerAssessmentBPS:   5,
	}
}

// Pricing is the fees on one cleared transaction.
type Pricing struct {
	ProgramID   string `json:"program_id"`
	FeeVersion  string `json:"fee_version"`
	Interchange int64  `json:"interchange"`  // Acquirer pays issuer
	AcquirerFee int64  `json:"acquirer_fee"` // Acquirer pays network
	IssuerFee   int64  `json:"issuer_fee"`   // Issuer pays network
}

// Price finds the transaction's interchange program and computes its fees.
// Cash back earns no interchange, so it is left out of the base.
func (t FeeTable) Price(product card.Product, ch Channel, amount, cashback int64) Pricing {
	prog := t.Programs[len(t.Programs)-1]
	for _, p := range t.Programs {
		if p.matches(product, ch) {
			prog = p
			break
		}
	}
	base := amount - cashback
	return Pricing{
		ProgramID:   prog.ID,
		FeeVersion:  t.Version,
		Interchange: BPS(base, prog.RateBPS) + prog.FixedMinor,
		AcquirerFee: BPS(amount, t.AcquirerAssessmentBPS) + t.AcquirerPerItemMinor,
		IssuerFee:   BPS(amount, t.IssuerAssessmentBPS),
	}
}

// BPS returns rate basis points of amount, rounded half up per transaction
// (KT 05: explicit rounding, published to members).
func BPS(amount, rate int64) int64 {
	return (amount*rate + 5_000) / 10_000
}
