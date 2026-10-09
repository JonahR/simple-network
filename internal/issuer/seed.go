package issuer

import "github.com/JonahR/simple-network/internal/card"

// Seed data for the two simulated banks. Card numbers, expiries, and CVVs
// match the cards in the POS wallet. Limits are small enough that repeated
// purchases eventually decline or partially approve.

// FirstSimpleBankAccounts returns First Simple Bank's accounts.
func FirstSimpleBankAccounts() ([]Account, []AutoEnroll) {
	return []Account{
			{PAN: "4242424242424242", Product: card.Credit, Expiry: "2912", CVV: "123", Limit: 500_000},
			{PAN: "4000056655665556", Product: card.Debit, Expiry: "2905", CVV: "789",
				PINHash: HashPIN("4000056655665556", "1234"), Limit: 250_000},
			{PAN: "4716006861111015", Product: card.Healthcare, Expiry: "2811", CVV: "852", Limit: 50_000},
		}, []AutoEnroll{
			{BIN: "400000", Product: card.Credit, Limit: 200_000}, // POS "Fill test card" range
		}
}

// UnionCardBankAccounts returns Union Card Bank's accounts.
func UnionCardBankAccounts() ([]Account, []AutoEnroll) {
	return []Account{
			{PAN: "5555555555554444", Product: card.Credit, Expiry: "2808", CVV: "456", Limit: 30_000},
			{PAN: "4358805984634941", Product: card.Prepaid, Expiry: "2807", CVV: "321", Limit: 10_000},
			{PAN: "5568007143162129", Product: card.Fleet, Expiry: "2902", CVV: "654", Limit: 100_000},
		}, []AutoEnroll{
			{BIN: "485932", Product: card.Credit, Limit: 100_000}, // Pay Later single-use virtual cards
		}
}
