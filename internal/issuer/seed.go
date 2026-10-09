package issuer

import "github.com/JonahR/simple-network/internal/card"

// Seed data for the two simulated banks. Card numbers, expiries, and CVVs
// match the cards in the POS wallet. Limits are small enough that repeated
// purchases eventually decline or partially approve.

// FirstSimpleBankAccounts returns First Simple Bank's accounts.
func FirstSimpleBankAccounts() ([]Account, []AutoEnroll) {
	return []Account{
			{PAN: "4242424242424242", Holder: "Jane Doe", Product: card.Credit, Expiry: "3312", CVV: "123", Limit: 500_000},
			{PAN: "4000056655665556", Holder: "Jane Doe", Product: card.Debit, Expiry: "3305", CVV: "789",
				PINHash: HashPIN("4000056655665556", "1234"), Limit: 250_000},
			{PAN: "4716006861111015", Holder: "Jane Doe", Product: card.Healthcare, Expiry: "3211", CVV: "852", Limit: 50_000},
		}, []AutoEnroll{
			{BIN: "400000", Product: card.Credit, Holder: "Test cardholder", Limit: 200_000}, // POS "Fill test card" range
		}
}

// UnionCardBankAccounts returns Union Card Bank's accounts.
func UnionCardBankAccounts() ([]Account, []AutoEnroll) {
	return []Account{
			{PAN: "5555555555554444", Holder: "John Smith", Product: card.Credit, Expiry: "3208", CVV: "456", Limit: 30_000},
			{PAN: "4358805984634941", Holder: "Gift card", Product: card.Prepaid, Expiry: "3207", CVV: "321", Limit: 10_000},
			{PAN: "5568007143162129", Holder: "Acme Logistics", Product: card.Fleet, Expiry: "3302", CVV: "654", Limit: 100_000},
		}, []AutoEnroll{
			{BIN: "485932", Product: card.Credit, Holder: "Pay Later customer", Limit: 100_000}, // Pay Later single-use virtual cards
		}
}
