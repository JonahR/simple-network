package main

import (
	"io/fs"
	"regexp"
	"testing"

	"github.com/JonahR/simple-network/internal/learn"
)

// Every label the disputes page renders at runtime explains itself in Help
// mode: literal data-term attributes, KPI terms, and the STAGES and
// STATE_TERMS tables.
func TestDisputesRuntimeTermsExist(t *testing.T) {
	app, err := fs.ReadFile(webFS, "web/disputes/app.js")
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
		`\["[a-z_]+", "[A-Z][A-Za-z- ]+", "[A-Za-z ]+", "([a-z0-9-]+)"\]`,
	} {
		for _, m := range regexp.MustCompile(re).FindAllStringSubmatch(string(app), -1) {
			ids = append(ids, m[1])
		}
	}
	block := regexp.MustCompile(`STATE_TERMS = \{([^}]*)\}`).FindStringSubmatch(string(app))
	if block == nil {
		t.Fatal("STATE_TERMS not found")
	}
	for _, m := range regexp.MustCompile(`: "([a-z0-9-]+)"`).FindAllStringSubmatch(block[1], -1) {
		ids = append(ids, m[1])
	}
	if len(ids) < 25 {
		t.Fatalf("found only %d runtime terms; did app.js change shape?", len(ids))
	}
	for _, id := range ids {
		if _, ok := terms[id]; !ok {
			t.Errorf("disputes app.js uses unknown term %q", id)
		}
	}
}
