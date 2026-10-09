package main

import (
	"io/fs"
	"regexp"
	"testing"

	"github.com/JonahR/simple-network/internal/learn"
)

// Every label the settlement page renders at runtime explains itself in
// Help mode: literal data-term attributes, KPI terms, and the pipeline's
// STAGE_TERMS map.
func TestSettlementRuntimeTermsExist(t *testing.T) {
	app, err := fs.ReadFile(webFS, "web/settlement/app.js")
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
		`\["[A-Z][A-Za-z ]+", "([a-z0-9-]+)", `,
		`\|\| "([a-z0-9-]+)"\}"`,
	} {
		for _, m := range regexp.MustCompile(re).FindAllStringSubmatch(string(app), -1) {
			ids = append(ids, m[1])
		}
	}
	stages := regexp.MustCompile(`(?s)STAGE_TERMS = \{(.*?)\};`).FindStringSubmatch(string(app))
	if stages == nil {
		t.Fatal("STAGE_TERMS not found")
	}
	for _, v := range regexp.MustCompile(`: "([a-z0-9-]+)"`).FindAllStringSubmatch(stages[1], -1) {
		ids = append(ids, v[1])
	}
	if len(ids) < 30 {
		t.Fatalf("found only %d runtime terms; did app.js change shape?", len(ids))
	}
	for _, id := range ids {
		if _, ok := terms[id]; !ok {
			t.Errorf("settlement app.js uses unknown term %q", id)
		}
	}
}
