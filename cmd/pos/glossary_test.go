package main

import (
	"encoding/json"
	"encoding/xml"
	"io/fs"
	"path"
	"regexp"
	"strings"
	"testing"
	"unicode"

	simplenetwork "github.com/JonahR/simple-network"
	"github.com/JonahR/simple-network/internal/iso8583"
)

// These tests keep the learning layer honest: every label someone can click
// must have a definition, a link into the Network KT docs that lands on a real
// heading, and a link to an outside source.

type term struct {
	Name    string      `json:"name"`
	Def     string      `json:"def"`
	KT      [][2]string `json:"kt"`
	Ext     [][2]string `json:"ext"`
	See     []string    `json:"see"`
	Aliases []string    `json:"aliases"`
}

func loadGlossary(t *testing.T) map[string]term {
	t.Helper()
	b, err := fs.ReadFile(webFS, "web/learn/glossary.json")
	if err != nil {
		t.Fatal(err)
	}
	var g struct {
		Terms map[string]term `json:"terms"`
	}
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatalf("glossary.json: %v", err)
	}
	return g.Terms
}

// normLabel mirrors norm() in learn.js.
func normLabel(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	s = strings.TrimRight(s, "·:…")
	return strings.ToLower(strings.TrimSpace(s))
}

// githubSlug mirrors how GitHub (and doc.js) turn a heading into an anchor.
func githubSlug(h string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(h)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsNumber(r) || r == '-' || r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('-')
		}
	}
	return b.String()
}

var headingRE = regexp.MustCompile(`(?m)^#{1,6} +(.+?) *$`)
var inlineMD = regexp.MustCompile("[`*]|\\[([^\\]]*)\\]\\([^)]*\\)")

func anchors(t *testing.T, kt fs.FS, file string) map[string]bool {
	t.Helper()
	b, err := fs.ReadFile(kt, file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	// Ignore headings inside fenced code blocks.
	var text strings.Builder
	inFence := false
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "```") {
			inFence = !inFence
			continue
		}
		if !inFence {
			text.WriteString(line + "\n")
		}
	}
	out := map[string]bool{}
	seen := map[string]int{}
	for _, m := range headingRE.FindAllStringSubmatch(text.String(), -1) {
		s := githubSlug(inlineMD.ReplaceAllString(m[1], "$1"))
		if n := seen[s]; n > 0 {
			out[s+"-"+string(rune('0'+n))] = true
		} else {
			out[s] = true
		}
		seen[s]++
	}
	return out
}

func TestGlossaryEntriesAreComplete(t *testing.T) {
	terms := loadGlossary(t)
	kt := simplenetwork.Knowledge()
	anchorCache := map[string]map[string]bool{}
	aliasOwner := map[string]string{}

	for id, tm := range terms {
		if tm.Name == "" || len(tm.Def) < 40 {
			t.Errorf("%s: needs a name and a real definition", id)
		}
		if len(tm.KT) == 0 {
			t.Errorf("%s: needs at least one Network KT link", id)
		}
		if len(tm.Ext) == 0 {
			t.Errorf("%s: needs at least one outside link", id)
		}
		for _, l := range tm.Ext {
			if !strings.HasPrefix(l[1], "https://") || l[0] == "" {
				t.Errorf("%s: outside link %q must be https with a label", id, l[1])
			}
		}
		for _, l := range tm.KT {
			file, anchor, _ := strings.Cut(l[1], "#")
			if _, err := fs.Stat(kt, file); err != nil {
				t.Errorf("%s: KT link %q: no such file", id, l[1])
				continue
			}
			if anchor == "" || !strings.HasSuffix(file, ".md") {
				continue
			}
			if anchorCache[file] == nil {
				anchorCache[file] = anchors(t, kt, file)
			}
			if !anchorCache[file][anchor] {
				t.Errorf("%s: KT link %q: no heading with that anchor", id, l[1])
			}
		}
		for _, s := range tm.See {
			if _, ok := terms[s]; !ok {
				t.Errorf("%s: related term %q doesn't exist", id, s)
			}
		}
		for _, a := range tm.Aliases {
			n := normLabel(a)
			if other, ok := aliasOwner[n]; ok && other != id {
				t.Errorf("alias %q belongs to both %s and %s", a, other, id)
			}
			aliasOwner[n] = id
		}
	}
}

var dataTermRE = regexp.MustCompile(`data-term="([^"]+)"`)

// Every data-term in the POS pages and the KT visuals names a real term.
func TestEveryLabelHasADefinition(t *testing.T) {
	terms := loadGlossary(t)
	check := func(fsys fs.FS, root string) {
		fs.WalkDir(fsys, root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			if ext := path.Ext(p); ext != ".html" && ext != ".svg" && ext != ".js" {
				return nil
			}
			b, _ := fs.ReadFile(fsys, p)
			for _, m := range dataTermRE.FindAllStringSubmatch(string(b), -1) {
				if strings.ContainsAny(m[1], "${<") {
					continue // built at runtime in JS, or an example in a comment
				}
				if _, ok := terms[m[1]]; !ok {
					t.Errorf("%s: data-term %q isn't in glossary.json", p, m[1])
				}
			}
			return nil
		})
	}
	check(webFS, "web")
	check(simplenetwork.Knowledge(), "visuals")
}

// Every <text> label in a KT visual is clickable: the <text> itself or its
// <tspan> parts carry data-term. A <g data-learn="skip"> exempts its contents
// (for example, chart axis values).
func TestVisualLabelsAreTagged(t *testing.T) {
	kt := simplenetwork.Knowledge()
	files, _ := fs.Glob(kt, "visuals/*.svg")
	if len(files) == 0 {
		t.Fatal("no visuals found")
	}
	skipRE := regexp.MustCompile(`(?s)<g[^>]*data-learn="skip"[^>]*>.*?</g>`)
	textRE := regexp.MustCompile(`(?s)<text\b([^>]*)>(.*?)</text>`)
	tagRE := regexp.MustCompile(`<[^>]+>`)
	for _, f := range files {
		b, _ := fs.ReadFile(kt, f)
		if err := xml.Unmarshal(b, new(struct{})); err != nil {
			t.Errorf("%s: not well-formed XML: %v", f, err)
		}
		src := skipRE.ReplaceAllString(string(b), "")
		for _, m := range textRE.FindAllStringSubmatch(src, -1) {
			attrs, body := m[1], m[2]
			visible := strings.TrimSpace(tagRE.ReplaceAllString(body, ""))
			if visible == "" || strings.Contains(attrs, "data-term=") || strings.Contains(body, "<tspan data-term=") {
				continue
			}
			t.Errorf("%s: label %q has no data-term", f, visible)
		}
	}
}

// Every field the POS can show in its 0100 table, and every entry mode, has a term.
func TestPOSFieldsHaveTerms(t *testing.T) {
	terms := loadGlossary(t)
	alias := map[string]string{}
	for id, tm := range terms {
		for _, a := range tm.Aliases {
			alias[normLabel(a)] = id
		}
	}
	lookup := func(s string) bool {
		if _, ok := alias[normLabel(s)]; ok {
			return true
		}
		first := regexp.MustCompile(`\s*[(:]\s*`).Split(s, 2)[0]
		_, ok := alias[normLabel(first)]
		return ok
	}

	app, err := fs.ReadFile(webFS, "web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile(`(?s)const FIELDS = \[(.*?)\n\];`).FindStringSubmatch(string(app))
	if block == nil {
		t.Fatal("couldn't find FIELDS in app.js")
	}
	rows := regexp.MustCompile(`\["[^"]*", "[^"]*", "([^"]+)"\]`).FindAllStringSubmatch(block[1], -1)
	if len(rows) < 10 {
		t.Fatalf("parsed only %d FIELDS rows", len(rows))
	}
	for _, r := range rows {
		if !lookup(r[1]) {
			t.Errorf("POS field %q has no glossary alias", r[1])
		}
	}
	if !lookup("Device account number (token)") {
		t.Error("token PAN label has no glossary alias")
	}

	posTerms, err := fs.ReadFile(webFS, "web/learn/pos-terms.js")
	if err != nil {
		t.Fatal(err)
	}
	for code, name := range iso8583.EntryModes {
		if !strings.Contains(string(posTerms), `"`+code+`"`) {
			t.Errorf("entry mode %s (%s) isn't mapped in pos-terms.js", code, name)
		}
		if !lookup(name) {
			t.Errorf("entry mode name %q has no glossary alias", name)
		}
	}
	for _, name := range iso8583.Wallets {
		if !lookup(name) {
			t.Errorf("wallet %q has no glossary alias", name)
		}
	}
}
