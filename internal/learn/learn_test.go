package learn

import (
	"encoding/xml"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode"

	simplenetwork "github.com/JonahR/simple-network"
)

// These tests keep the learning layer honest: every label someone can click
// must have a definition, a link into the Network KT docs that lands on a real
// heading, and a link to an outside source.

func mustTerms(t *testing.T) map[string]Term {
	t.Helper()
	terms, err := Terms()
	if err != nil {
		t.Fatalf("glossary.json: %v", err)
	}
	return terms
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

var (
	headingRE = regexp.MustCompile(`^#{1,6} +(.+?) *$`)
	inlineMD  = regexp.MustCompile("[`*]|\\[([^\\]]*)\\]\\([^)]*\\)")
)

// anchors lists a markdown file's heading anchors, skipping fenced code.
func anchors(t *testing.T, kt fs.FS, file string) map[string]bool {
	t.Helper()
	b, err := fs.ReadFile(kt, file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	out := map[string]bool{}
	seen := map[string]int{}
	inFence := false
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "```") {
			inFence = !inFence
			continue
		}
		m := headingRE.FindStringSubmatch(line)
		if inFence || m == nil {
			continue
		}
		s := githubSlug(inlineMD.ReplaceAllString(m[1], "$1"))
		if n := seen[s]; n > 0 {
			out[s+"-"+strconv.Itoa(n)] = true
		} else {
			out[s] = true
		}
		seen[s]++
	}
	return out
}

func TestGlossaryEntriesAreComplete(t *testing.T) {
	terms := mustTerms(t)
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
			n := Normalize(a)
			if other, ok := aliasOwner[n]; ok && other != id {
				t.Errorf("alias %q belongs to both %s and %s", a, other, id)
			}
			aliasOwner[n] = id
		}
	}
}

func TestLearningPagesAndVisualsUseKnownTerms(t *testing.T) {
	for _, src := range []struct {
		fsys fs.FS
		root string
	}{{Web(), "."}, {simplenetwork.Knowledge(), "visuals"}} {
		bad, err := UnknownTerms(src.fsys, src.root)
		if err != nil {
			t.Fatal(err)
		}
		for _, b := range bad {
			t.Errorf("unknown data-term: %s", b)
		}
	}
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

func TestHandlerServesHubDocsAndConfig(t *testing.T) {
	srv := httptest.NewServer(Handler(Links{POS: "http://pos", Dashboard: "http://dash"}))
	defer srv.Close()
	for path, want := range map[string]string{
		"/learn/":                                          "How a card network works",
		"/learn/learn.js":                                  "window.Learn",
		"/learn/glossary.json":                             `"terms"`,
		"/learn/config.json":                               `"dashboard_url":"http://dash"`,
		"/learn/kt/07-cards-bins-and-tokens.md":            "Luhn algorithm",
		"/learn/kt/visuals/pan-anatomy.svg":                `data-term="luhn"`,
		"/learn/kt/interactive/authorization-request.html": "Anatomy of an",
	} {
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		b := new(strings.Builder)
		buf := make([]byte, 1<<16)
		for {
			n, err := res.Body.Read(buf)
			b.Write(buf[:n])
			if err != nil {
				break
			}
		}
		res.Body.Close()
		if res.StatusCode != 200 || !strings.Contains(b.String(), want) {
			t.Errorf("GET %s: status %d, want body containing %q", path, res.StatusCode, want)
		}
	}
}

func TestLookupMatchesLearnJS(t *testing.T) {
	for label, want := range map[string]string{
		"DE22":                 "de22",
		"Expiration (YYMM)":    "de14",
		"Amount (minor units)": "de4",
		"Contactless read":     "contactless",
		"Apple Pay":            "apple-pay",
	} {
		if got, ok := Lookup(label); !ok || got != want {
			t.Errorf("Lookup(%q) = %q, want %q", label, got, want)
		}
	}
}
