package main

import (
	"io/fs"
	"regexp"
	"testing"
	"time"

	"github.com/JonahR/simple-network/internal/card"
	"github.com/JonahR/simple-network/internal/issuer"
	"github.com/JonahR/simple-network/internal/network"
)

// The demo cards are defined three times: in the POS wallet and card vault,
// in the issuers' records, and in the network's token vault. An expiry that
// differs in one place is declined with 54, so they must all agree.
func TestDemoCardExpiriesMatchIssuerAndVault(t *testing.T) {
	fa, _ := issuer.FirstSimpleBankAccounts()
	ua, _ := issuer.UnionCardBankAccounts()
	cards := map[string]string{} // PAN -> YYMM
	for _, a := range append(fa, ua...) {
		cards[a.PAN] = a.Expiry
	}
	vault := network.DefaultVault()

	check := func(number, mmyy string) {
		t.Helper()
		yymm, err := card.ParseExpiry(mmyy, time.Now()) // Real clock: fails once a demo card expires
		if err != nil {
			t.Errorf("%s: expiry %s: %v", number, mmyy, err)
			return
		}
		if want, ok := cards[number]; ok {
			if yymm != want {
				t.Errorf("card %s expires %s at the POS but %s at the issuer", number, yymm, want)
			}
			return
		}
		tok, ok := vault.Lookup(number)
		if !ok {
			t.Errorf("%s is neither an issuer card nor a vault token", number)
			return
		}
		if yymm != tok.TokenExpiry {
			t.Errorf("token %s expires %s at the POS but %s in the vault", number, yymm, tok.TokenExpiry)
		}
		if tok.CardExpiry != cards[tok.PAN] {
			t.Errorf("vault says card %s expires %s; issuer says %s", tok.PAN, tok.CardExpiry, cards[tok.PAN])
		}
		if tok.TokenExpiry <= tok.CardExpiry {
			t.Errorf("token %s expires %s, not after its card (%s)", number, tok.TokenExpiry, tok.CardExpiry)
		}
	}

	app, err := fs.ReadFile(webFS, "web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	matches := regexp.MustCompile(`number: "(\d+)", expiry: "(\d\d/\d\d)"`).FindAllStringSubmatch(string(app), -1)
	if len(matches) < 12 {
		t.Fatalf("found %d wallet cards and tokens in app.js, want at least 12", len(matches))
	}
	for _, m := range matches {
		check(m[1], m[2])
	}
	for _, c := range cardsOnFile {
		check(c.PAN, c.Expiry)
	}
}
