package ui

import (
	"regexp"
	"testing"

	"github.com/JonahR/simple-network/internal/learn"
)

// Every label the dispute desk renders explains itself in Help mode: literal
// data-term attributes, the STAGES table, and the actors' badges.
func TestDisputeDeskTermsExist(t *testing.T) {
	js, err := assets.ReadFile("disputes.js")
	if err != nil {
		t.Fatal(err)
	}
	terms, err := learn.Terms()
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{"issuer", "acquirer", "network"} // Timeline badges use the actor as the term
	for _, re := range []string{
		`data-term="([a-z0-9-]+)"`,
		`\["[A-Z][A-Za-z- ]+", "([a-z0-9-]+)", "[a-z]+"\]`,
	} {
		for _, m := range regexp.MustCompile(re).FindAllStringSubmatch(string(js), -1) {
			ids = append(ids, m[1])
		}
	}
	if len(ids) < 20 {
		t.Fatalf("found only %d terms; did disputes.js change shape?", len(ids))
	}
	for _, id := range ids {
		if _, ok := terms[id]; !ok {
			t.Errorf("disputes.js uses unknown term %q", id)
		}
	}
}
