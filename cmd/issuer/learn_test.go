package main

import (
	"io/fs"
	"regexp"
	"testing"

	"github.com/JonahR/simple-network/internal/learn"
)

// Every label on the issuer page explains itself in Help mode. These tests
// fail when a label points at a term the glossary doesn't have.

func TestIssuerLabelsUseKnownTerms(t *testing.T) {
	bad, err := learn.UnknownTerms(webFS, "web")
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range bad {
		t.Errorf("unknown data-term: %s", b)
	}
}

func TestIssuerRuntimeTermsExist(t *testing.T) {
	app, err := fs.ReadFile(webFS, "web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	terms, err := learn.Terms()
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	// Literal data-term attributes, KPI and status terms, and the CHECK_TERMS map's values.
	for _, re := range []string{
		`data-term="([a-z0-9-]+)"`,
		`dataset\.term = "([a-z0-9-]+)"`,
		`\["[A-Z][A-Za-z ]+", "([a-z0-9-]+)", `,
		`\["(?:good|bad|)", "[^"]+", "([a-z0-9-]+)"\]`,
		`\|\| "([a-z0-9-]+)"\}"`,
	} {
		for _, m := range regexp.MustCompile(re).FindAllStringSubmatch(string(app), -1) {
			ids = append(ids, m[1])
		}
	}
	checkMap := regexp.MustCompile(`(?s)CHECK_TERMS = \{(.*?)\};`).FindStringSubmatch(string(app))
	if checkMap == nil {
		t.Fatal("CHECK_TERMS not found in app.js")
	}
	values := regexp.MustCompile(`: "([a-z0-9-]+)"`).FindAllStringSubmatch(checkMap[1], -1)
	if len(values) != 8 {
		t.Errorf("CHECK_TERMS has %d terms, want one per check (8)", len(values))
	}
	for _, v := range values {
		ids = append(ids, v[1])
	}
	if len(ids) < 25 {
		t.Fatalf("found only %d runtime terms; did app.js change shape?", len(ids))
	}
	for _, id := range ids {
		if _, ok := terms[id]; !ok {
			t.Errorf("app.js uses unknown term %q", id)
		}
	}
}
