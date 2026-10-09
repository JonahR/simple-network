package network

import "sync"

// Token maps a device token to the card it stands for. The vault is the only
// place the network keeps that link (D17: a separate service later).
type Token struct {
	Token       string `json:"token"`
	PAN         string `json:"pan"`
	TokenExpiry string `json:"token_expiry"` // YYMM
	CardExpiry  string `json:"card_expiry"`  // YYMM
	Wallet      string `json:"wallet"`
	Active      bool   `json:"active"`
}

// Vault stores device tokens.
type Vault struct {
	mu     sync.RWMutex
	tokens map[string]Token
}

// NewVault creates a vault holding tokens.
func NewVault(tokens []Token) *Vault {
	v := &Vault{tokens: map[string]Token{}}
	for _, t := range tokens {
		v.tokens[t.Token] = t
	}
	return v
}

// Lookup returns the token record for a device token.
func (v *Vault) Lookup(token string) (Token, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	t, ok := v.tokens[token]
	return t, ok
}

// SetActive suspends or reactivates a token, as when a phone is lost.
func (v *Vault) SetActive(token string, active bool) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	t, ok := v.tokens[token]
	if ok {
		t.Active = active
		v.tokens[token] = t
	}
	return ok
}

// All returns every token.
func (v *Vault) All() []Token {
	v.mu.RLock()
	defer v.mu.RUnlock()
	out := make([]Token, 0, len(v.tokens))
	for _, t := range v.tokens {
		out = append(out, t)
	}
	return out
}

// DefaultVault holds the tokens provisioned to the POS mobile wallets.
func DefaultVault() *Vault {
	const everyday, rewards = "4242424242424242", "5555555555554444"
	return NewVault([]Token{
		{Token: "4895372051310681", PAN: everyday, TokenExpiry: "3409", CardExpiry: "3312", Wallet: "apple_pay", Active: true},
		{Token: "4895373708421368", PAN: everyday, TokenExpiry: "3411", CardExpiry: "3312", Wallet: "google_pay", Active: true},
		{Token: "4895377555006883", PAN: everyday, TokenExpiry: "3406", CardExpiry: "3312", Wallet: "samsung_pay", Active: true},
		{Token: "5220930238452479", PAN: rewards, TokenExpiry: "3503", CardExpiry: "3208", Wallet: "apple_pay", Active: true},
		{Token: "5220934871046911", PAN: rewards, TokenExpiry: "3501", CardExpiry: "3208", Wallet: "google_pay", Active: true},
		{Token: "5220933784416278", PAN: rewards, TokenExpiry: "3505", CardExpiry: "3208", Wallet: "samsung_pay", Active: true},
	})
}
