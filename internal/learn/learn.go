// Package learn serves the learning layer shared by every simple-network UI:
// the glossary, the definition popovers (learn.js), the /learn/ hub and doc
// viewer, and the Network KT docs and visuals.
//
// Any page can load /learn/learn.css and /learn/learn.js and mark labels with
// data-term="<id>" to make them explain themselves.
package learn

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"
	"sync"

	simplenetwork "github.com/JonahR/simple-network"
)

//go:embed web
var webFS embed.FS

// Links are the other UIs the learning pages link to.
type Links struct {
	POS       string `json:"pos_url"`
	Dashboard string `json:"dashboard_url"`
}

// Handler serves the learning layer. Mount it at /learn/:
//
//	mux.Handle("GET /learn/", learn.Handler(learn.Links{...}))
func Handler(links Links) http.Handler {
	web, err := fs.Sub(webFS, "web")
	if err != nil {
		panic(err) // embedded at build time
	}
	mux := http.NewServeMux()
	mux.Handle("GET /learn/kt/", http.StripPrefix("/learn/kt/", http.FileServerFS(simplenetwork.Knowledge())))
	mux.HandleFunc("GET /learn/config.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(links)
	})
	mux.Handle("GET /learn/", http.StripPrefix("/learn/", http.FileServerFS(web)))
	return mux
}

// Term is one glossary entry.
type Term struct {
	Name    string      `json:"name"`
	Def     string      `json:"def"`
	KT      [][2]string `json:"kt"`  // [label, "file.md#heading"]
	Ext     [][2]string `json:"ext"` // [label, "https://..."]
	Aliases []string    `json:"aliases"`
}

var (
	loadOnce sync.Once
	terms    map[string]Term
	aliases  map[string]string
	loadErr  error
)

func load() {
	b, err := fs.ReadFile(webFS, "web/glossary.json")
	if err != nil {
		loadErr = err
		return
	}
	var g struct {
		Terms map[string]Term `json:"terms"`
	}
	if loadErr = json.Unmarshal(b, &g); loadErr != nil {
		return
	}
	terms = g.Terms
	aliases = map[string]string{}
	for id, t := range terms {
		for _, a := range t.Aliases {
			aliases[Normalize(a)] = id
		}
	}
}

// Terms returns the glossary.
func Terms() (map[string]Term, error) {
	loadOnce.Do(load)
	return terms, loadErr
}

// Normalize matches norm() in learn.js: collapse spaces, drop trailing
// punctuation like "·" or ":", and lowercase.
func Normalize(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	s = strings.TrimRight(s, "·:…")
	return strings.ToLower(strings.TrimSpace(s))
}

var firstPart = regexp.MustCompile(`\s*[(:]\s*`)

// Lookup returns the term for a label's text the way learn.js does: an exact
// alias, or the part before a "(" or ":".
func Lookup(label string) (string, bool) {
	loadOnce.Do(load)
	if id, ok := aliases[Normalize(label)]; ok {
		return id, true
	}
	id, ok := aliases[Normalize(firstPart.Split(label, 2)[0])]
	return id, ok
}

var dataTermRE = regexp.MustCompile(`data-term="([^"]+)"`)

// UnknownTerms lists every data-term in fsys's HTML, SVG and JS files under
// root that isn't in the glossary, as "file: id". Services use it in tests.
func UnknownTerms(fsys fs.FS, root string) ([]string, error) {
	all, err := Terms()
	if err != nil {
		return nil, err
	}
	var bad []string
	err = fs.WalkDir(fsys, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		switch path.Ext(p) {
		case ".html", ".svg", ".js":
		default:
			return nil
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		for _, m := range dataTermRE.FindAllStringSubmatch(string(b), -1) {
			if strings.ContainsAny(m[1], "${<") {
				continue // built at runtime in JS, or an example in a comment
			}
			if _, ok := all[m[1]]; !ok {
				bad = append(bad, p+": "+m[1])
			}
		}
		return nil
	})
	return bad, err
}

// Web returns the embedded learning assets (for tests in other packages).
func Web() fs.FS {
	web, _ := fs.Sub(webFS, "web")
	return web
}
