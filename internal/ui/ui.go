// Package ui holds what every service's web page shares: the navigation bar
// that links the participants' pages in the order a payment flows through
// them (POS → Acquirer → Network → Issuer), then on to clearing and
// settlement at the end of the day, and the Risk lab beside them.
//
// A page opts in with:
//
//	<link rel="stylesheet" href="/ui/nav.css">
//	<nav class="sn-nav" data-page="acquirer"></nav>
//	<script src="/ui/nav.js"></script>
package ui

import (
	"embed"
	"encoding/json"
	"net/http"
	"os"
)

//go:embed nav.css nav.js
var assets embed.FS

// Page is one participant's web page.
type Page struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Role  string `json:"role"`
	Term  string `json:"term"` // Glossary term that explains the participant in Help mode
	URL   string `json:"url"`
	// Aside marks a page that isn't a stop in the payment flow, such as the
	// Risk lab. The nav shows it after a divider instead of an arrow.
	Aside bool `json:"aside,omitempty"`
}

// PagesFromEnv lists the pages in payment-flow order, with the URLs the
// browser should use. They differ from the service-to-service URLs in Docker.
func PagesFromEnv() []Page {
	return []Page{
		// The cardholder starts every payment; their online banking is served
		// by the issuer that holds their card.
		{"cardholder", "Cardholder", "Customer's online banking", "cardholder", env("CARDHOLDER_PUBLIC_URL", "http://localhost:8091/banking/"), false},
		{"pos", "POS", "Merchant terminal", "pos-terminal", env("POS_PUBLIC_URL", "http://localhost:8080"), false},
		{"acquirer", "Acquirer", "Merchant's bank", "acquirer", env("ACQUIRER_PUBLIC_URL", "http://localhost:8081"), false},
		{"network", "Network", "Card network switch", "network", env("NETWORK_DASHBOARD_URL", "http://localhost:8090"), false},
		{"issuer", "Issuer", "Cardholder's bank", "issuer", env("ISSUER_PUBLIC_URL", "http://localhost:8091"), false},
		{"settlement", "Settlement", "Clearing and settlement", "settlement", env("SETTLEMENT_PUBLIC_URL", "http://localhost:8090/settlement/"), false},
		{"risklab", "Risk lab", "Fraud and failure drills", "risk-lab", env("RISKLAB_PUBLIC_URL", "http://localhost:8093"), true},
	}
}

// Register mounts the navigation assets under /ui/.
func Register(mux *http.ServeMux, pages []Page) {
	files := http.StripPrefix("/ui/", http.FileServerFS(assets))
	mux.Handle("GET /ui/nav.css", files)
	mux.Handle("GET /ui/nav.js", files)
	mux.HandleFunc("GET /ui/pages", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(pages)
	})
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
