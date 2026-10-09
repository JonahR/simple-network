package main

import (
	"io/fs"
	"regexp"
	"testing"

	"github.com/JonahR/simple-network/internal/learn"
	"github.com/JonahR/simple-network/internal/ui"
)

// Every label on the acquirer page explains itself in Help mode. These tests
// fail when a label points at a term the glossary doesn't have.

func TestAcquirerLabelsUseKnownTerms(t *testing.T) {
	bad, err := learn.UnknownTerms(webFS, "web")
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range bad {
		t.Errorf("unknown data-term: %s", b)
	}
}

func TestAcquirerRuntimeTermsExist(t *testing.T) {
	app, err := fs.ReadFile(webFS, "web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	terms, err := learn.Terms()
	if err != nil {
		t.Fatal(err)
	}
	// Term ids app.js puts into data-term at runtime: lane and KPI terms,
	// the MTI and field maps, and their fallbacks.
	var ids []string
	for _, re := range []string{
		`term: "([a-z0-9-]+)"`,
		`\["[A-Z][A-Za-z ]+", "([a-z0-9-]+)", `,
		`(?:MTI_TERMS|FIELD_TERMS) = \{([^}]*)\}`,
		`\|\| "([a-z0-9-]+)"\}"`,
		`data-term="([a-z0-9-]+)"`,
		`dataset\.term = "([a-z0-9-]+)"`,
	} {
		for _, m := range regexp.MustCompile(re).FindAllStringSubmatch(string(app), -1) {
			if len(m[1]) > 0 && m[1][0] == ' ' { // A map literal: take its values
				for _, v := range regexp.MustCompile(`: "([a-z0-9-]+)"`).FindAllStringSubmatch(m[1], -1) {
					ids = append(ids, v[1])
				}
				continue
			}
			ids = append(ids, m[1])
		}
	}
	if len(ids) < 15 {
		t.Fatalf("found only %d runtime terms; did app.js change shape?", len(ids))
	}
	for _, id := range ids {
		if _, ok := terms[id]; !ok {
			t.Errorf("app.js uses unknown term %q", id)
		}
	}
}

func TestNavTermsExist(t *testing.T) {
	terms, err := learn.Terms()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ui.PagesFromEnv() {
		if _, ok := terms[p.Term]; !ok {
			t.Errorf("nav page %s uses unknown term %q", p.ID, p.Term)
		}
	}
}
