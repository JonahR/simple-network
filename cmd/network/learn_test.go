package main

import (
	"io/fs"
	"regexp"
	"testing"

	"github.com/JonahR/simple-network/internal/learn"
)

// Every label on the network dashboard teaches what it means: static ones carry
// data-term in index.html, and the ones app.js renders are tagged by
// dashboard-terms.js through glossary aliases. These tests fail when a new
// label has no definition.

func TestDashboardLabelsUseKnownTerms(t *testing.T) {
	bad, err := learn.UnknownTerms(webFS, "web")
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range bad {
		t.Errorf("unknown data-term: %s", b)
	}
}

func TestDashboardRuntimeLabelsHaveTerms(t *testing.T) {
	app, err := fs.ReadFile(webFS, "web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(app)
	var labels []string
	add := func(re string) {
		for _, m := range regexp.MustCompile(re).FindAllStringSubmatch(src, -1) {
			labels = append(labels, m[1])
		}
	}
	// Trace step names (STEP_NAMES values) and topology stages (STAGES).
	add(`\n  [a-z_]+: "([^"]+)",`)
	add(`\["[a-z_]+", "([A-Z][A-Za-z ]+)"\]`)
	// KPI tiles: ["Approval rate", value, sub].
	add(`\n    \["([A-Z][A-Za-z0-9 ]+)", `)
	// Messages table rows: ["DE2 Card / token", ...].
	add(`\["(DE\d+ [^"]+|Token|Network txn ID)", `)
	// Issuer health: { cls: ..., text: "Breaker open" }.
	add(`text: "([A-Z][a-z ]+)" \}`)
	if len(labels) < 20 {
		t.Fatalf("parsed only %d labels from app.js; update the patterns", len(labels))
	}
	for _, l := range labels {
		if l == "" {
			continue
		}
		if _, ok := learn.Lookup(l); !ok {
			t.Errorf("dashboard label %q has no glossary alias", l)
		}
	}
}
