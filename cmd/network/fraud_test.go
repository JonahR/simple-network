package main

import (
	"io/fs"
	"regexp"
	"testing"

	"github.com/JonahR/simple-network/internal/learn"
)

// Every label the fraud page renders at runtime explains itself in Help
// mode: literal data-term attributes and the KPI tiles' terms.
func TestFraudPageRuntimeTermsExist(t *testing.T) {
	app, err := fs.ReadFile(webFS, "web/fraud/app.js")
	if err != nil {
		t.Fatal(err)
	}
	terms, err := learn.Terms()
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, re := range []string{
		`data-term="([a-z0-9-]+)"`,
		`\["[A-Z][A-Za-z ]+", [^\]]+, "([a-z0-9-]+)"\]`, // KPI tiles: [label, value, sub, term]
	} {
		for _, m := range regexp.MustCompile(re).FindAllStringSubmatch(string(app), -1) {
			ids = append(ids, m[1])
		}
	}
	if len(ids) < 12 {
		t.Fatalf("found only %d runtime terms; did app.js change shape?", len(ids))
	}
	for _, id := range ids {
		if _, ok := terms[id]; !ok {
			t.Errorf("fraud app.js uses unknown term %q", id)
		}
	}
}
