package card

// Product is the kind of account behind a card. Terminals use it to decide
// which prompts to show (PIN, cash back, fleet data, healthcare amounts).
type Product string

const (
	Credit     Product = "credit"
	Debit      Product = "debit"
	Prepaid    Product = "prepaid"
	Fleet      Product = "fleet"
	Healthcare Product = "healthcare" // HSA/FSA
)

// ProductNames maps products to display names.
var ProductNames = map[Product]string{
	Credit:     "Credit",
	Debit:      "Debit",
	Prepaid:    "Prepaid",
	Fleet:      "Fleet",
	Healthcare: "HSA/FSA",
}

// BINProducts is the BIN table loaded onto terminals. In production the
// network or acquirer distributes it; BINs not listed are treated as credit.
var BINProducts = map[string]Product{
	"400005": Debit,
	"435880": Prepaid,
	"556800": Fleet,
	"471600": Healthcare,
}

// ProductFor returns the product for a PAN based on its BIN.
func ProductFor(pan string) Product {
	if p, ok := BINProducts[BIN(pan)]; ok {
		return p
	}
	return Credit
}
