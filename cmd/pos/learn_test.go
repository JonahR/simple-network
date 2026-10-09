package main

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"

	"github.com/JonahR/simple-network/internal/iso8583"
	"github.com/JonahR/simple-network/internal/learn"
)

// Every label on the POS teaches what it means: static ones carry data-term in
// index.html, and the ones app.js renders are tagged by pos-terms.js through
// glossary aliases. These tests fail when a new field or label has no definition.

func TestPOSLabelsUseKnownTerms(t *testing.T) {
	bad, err := learn.UnknownTerms(webFS, "web")
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range bad {
		t.Errorf("unknown data-term: %s", b)
	}
}

func TestPOSFieldsHaveTerms(t *testing.T) {
	app, err := fs.ReadFile(webFS, "web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(app)

	// Request fields (FIELDS) and response rows (responseRows) both render as
	// ["DEnn", "Name", value] triples; every name needs a glossary alias.
	block := regexp.MustCompile(`(?s)const FIELDS = \[(.*?)\n\];`).FindStringSubmatch(src)
	if block == nil {
		t.Fatal("couldn't find FIELDS in app.js")
	}
	names := regexp.MustCompile(`\["[^"]*", "[^"]*", "([^"]+)"\]`).FindAllStringSubmatch(block[1], -1)
	if len(names) < 10 {
		t.Fatalf("parsed only %d FIELDS rows", len(names))
	}
	resp := regexp.MustCompile(`\["(?:MTI|DE\d+|)", "([^"]+)", (?:resp|r)\.`).FindAllStringSubmatch(src, -1)
	for _, m := range append(names, resp...) {
		if _, ok := learn.Lookup(m[1]); !ok {
			t.Errorf("POS field %q has no glossary alias", m[1])
		}
	}
	for _, label := range []string{"Device account number (token)", "Network transaction ID", "Response from network", "Request sent (0100)"} {
		if strings.Contains(src, label) || strings.HasPrefix(label, "Device") {
			if _, ok := learn.Lookup(label); !ok {
				t.Errorf("POS label %q has no glossary alias", label)
			}
		}
	}

	posTerms, err := fs.ReadFile(webFS, "web/pos-terms.js")
	if err != nil {
		t.Fatal(err)
	}
	for code, name := range iso8583.EntryModes {
		if !strings.Contains(string(posTerms), `"`+code+`"`) {
			t.Errorf("entry mode %s (%s) isn't mapped in pos-terms.js", code, name)
		}
		if _, ok := learn.Lookup(name); !ok {
			t.Errorf("entry mode name %q has no glossary alias", name)
		}
	}
	for _, name := range iso8583.Wallets {
		if _, ok := learn.Lookup(name); !ok {
			t.Errorf("wallet %q has no glossary alias", name)
		}
	}
}
