// Command pos runs a browser-based point-of-sale terminal. It reads a card
// (keyed, swiped, inserted, tapped, from a mobile wallet, or from the
// merchant's card-on-file vault), prompts for whatever the card requires, and
// builds an ISO 8583-style 0100 authorization request. It sends the request to
// the merchant's acquirer, which forwards it to the card network.
package main

import (
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"
	_ "time/tzdata" // The distroless image has no zoneinfo for MERCHANT_TZ

	"github.com/JonahR/simple-network/internal/card"
	"github.com/JonahR/simple-network/internal/demokeys"
	"github.com/JonahR/simple-network/internal/iso8583"
	"github.com/JonahR/simple-network/internal/learn"
	"github.com/JonahR/simple-network/internal/ui"
)

//go:embed web
var webFS embed.FS

// Terminal holds the merchant configuration provisioned onto this terminal.
type Terminal struct {
	MerchantID   string `json:"merchant_id"`
	TerminalID   string `json:"terminal_id"`
	MerchantName string `json:"merchant_name"`
	City         string `json:"city"`
	Country      string `json:"country"`
	MCC          string `json:"mcc"`
	TimeZone     string `json:"time_zone"` // IANA zone for DE12/DE13 local time
}

// SaleInput is what the cashier enters for one sale.
type SaleInput struct {
	CardNumber     string `json:"card_number"`
	Expiry         string `json:"expiry"`
	CVV            string `json:"cvv"`
	CardholderName string `json:"cardholder_name"`
	Amount         string `json:"amount"`
	Currency       string `json:"currency"`
	EntryMode      string `json:"entry_mode"`

	// Card reads. The card or phone supplies these, not the cashier.
	Wallet     string `json:"wallet"`     // Mobile wallet that supplied a device token, if any
	Cryptogram string `json:"cryptogram"` // One-time cryptogram from a chip, contactless card, or wallet
	Track2     string `json:"track2"`     // Magnetic stripe data from a swipe

	// Card on file.
	CardOnFileID string `json:"card_on_file"`
	COFIndicator string `json:"cof_indicator"`

	// Prompts the terminal shows only when the card requires them.
	PIN              string `json:"pin"`
	Cashback         string `json:"cashback"`
	Odometer         string `json:"odometer"`
	VehicleID        string `json:"vehicle_id"`
	DriverID         string `json:"driver_id"`
	HealthcareAmount string `json:"healthcare_amount"`
}

// StoredCard is a card the merchant keeps on file for a returning customer.
type StoredCard struct {
	ID       string
	Customer string
	PAN      string
	Expiry   string // MM/YY
}

// cardsOnFile is the merchant's card vault. The browser only ever sees the
// masked number; the full PAN stays on the terminal server. Expiries must
// match the issuer's records (internal/issuer/seed.go); expiry_test.go checks.
var cardsOnFile = []StoredCard{
	{ID: "cof_jane", Customer: "Jane Doe · loyalty account", PAN: "4242424242424242", Expiry: "12/33"},
	{ID: "cof_acme", Customer: "Acme Corp · monthly subscription", PAN: "5555555555554444", Expiry: "08/32"},
}

// expiryFromNow returns an MM/YY expiry the given years and months from now.
func expiryFromNow(years, months int) string {
	return time.Now().AddDate(years, months, 0).Format("01/06")
}

func cardOnFile(id string) (StoredCard, bool) {
	for _, c := range cardsOnFile {
		if c.ID == id {
			return c, true
		}
	}
	return StoredCard{}, false
}

// Transaction is a sale as recorded by the terminal.
type Transaction struct {
	Status   string                `json:"status"` // APPROVED, PARTIAL, DECLINED, or NO_RESPONSE
	Message  string                `json:"message"`
	Request  iso8583.AuthRequest   `json:"request"`            // Redacted
	Response *iso8583.AuthResponse `json:"response,omitempty"` // Redacted; nil if the acquirer never answered
	Created  time.Time             `json:"created"`
}

type server struct {
	terminal     Terminal
	location     *time.Location // Merchant's time zone, for DE12/DE13
	pinKey       []byte         // PIN key shared between the acquirer and the network
	acquirer     *acquirerClient
	dashboardURL string
	acquirerURL  string // For the browser
	stan         atomic.Uint32

	mu      sync.Mutex
	history []Transaction
}

func main() {
	t := Terminal{
		MerchantID:   env("MERCHANT_ID", "000000000012345"),
		TerminalID:   env("TERMINAL_ID", "TERM0001"),
		MerchantName: env("MERCHANT_NAME", "Simple Coffee Co"),
		City:         env("MERCHANT_CITY", "San Francisco"),
		Country:      env("MERCHANT_COUNTRY", "US"),
		MCC:          env("MCC", "5814"),
		TimeZone:     env("MERCHANT_TZ", "America/Los_Angeles"),
	}
	addr := env("POS_ADDR", ":8080")

	pinKey, err := demokeys.Load("ACQUIRER_PIN_KEY", demokeys.AcquirerPIN)
	if err != nil {
		log.Fatal(err)
	}

	loc, err := time.LoadLocation(t.TimeZone)
	if err != nil {
		log.Fatalf("MERCHANT_TZ must be an IANA time zone like America/New_York: %v", err)
	}

	s := &server{
		terminal:     t,
		location:     loc,
		pinKey:       pinKey,
		acquirer:     newAcquirerClient(env("ACQUIRER_URL", "http://localhost:8081")),
		dashboardURL: env("NETWORK_DASHBOARD_URL", "http://localhost:8090"),
		acquirerURL:  env("ACQUIRER_PUBLIC_URL", "http://localhost:8081"),
	}
	static, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(static))
	// Definitions for every label, the learning hub, and the Network KT docs.
	mux.Handle("GET /learn/", learn.Handler(learn.Links{POS: "/", Dashboard: s.dashboardURL}))
	mux.HandleFunc("GET /api/terminal", s.handleTerminal)
	mux.HandleFunc("GET /api/test-card", s.handleTestCard)
	mux.HandleFunc("POST /api/sale", s.handleSale)
	mux.HandleFunc("GET /api/transactions", s.handleTransactions)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	ui.Register(mux, ui.PagesFromEnv())

	log.Printf("POS terminal %s (merchant %s) listening on %s", t.TerminalID, t.MerchantID, addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func (s *server) handleTerminal(w http.ResponseWriter, _ *http.Request) {
	saved := make([]map[string]string, len(cardsOnFile))
	for i, c := range cardsOnFile {
		saved[i] = map[string]string{"id": c.ID, "customer": c.Customer, "masked": card.Mask(c.PAN)}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"terminal":       s.terminal,
		"mtis":           iso8583.MTINames,
		"mccs":           iso8583.MCCs,
		"entry_modes":    iso8583.EntryModes,
		"currencies":     iso8583.Currencies,
		"wallets":        iso8583.Wallets,
		"cof_indicators": iso8583.COFIndicators,
		"bin_products":   card.BINProducts,
		"product_names":  card.ProductNames,
		"cards_on_file":  saved,
		"dashboard_url":  s.dashboardURL,
		"acquirer_url":   s.acquirerURL,
	})
}

func (s *server) handleTestCard(w http.ResponseWriter, _ *http.Request) {
	pan, err := card.Generate("400000", 16)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	expiry := expiryFromNow(3, 0)
	writeJSON(w, http.StatusOK, map[string]string{"card_number": pan, "expiry": expiry, "cvv": "123"})
}

func (s *server) handleSale(w http.ResponseWriter, r *http.Request) {
	var in SaleInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"errors": map[string]string{"form": "invalid request body"}})
		return
	}
	req, fieldErrs := s.buildAuthRequest(in, time.Now())
	if len(fieldErrs) > 0 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"errors": fieldErrs})
		return
	}

	tx := Transaction{Request: req.Redacted(card.Mask), Created: time.Now()}
	resp, err := s.acquirer.authorize(r.Context(), req)
	if err != nil {
		tx.Status, tx.Message = "NO_RESPONSE", "No response from the acquirer: "+err.Error()
	} else {
		redacted := resp.Redacted(card.Mask)
		tx.Response = &redacted
		tx.Status, tx.Message = outcome(req, resp)
	}
	s.mu.Lock()
	s.history = append(s.history, tx)
	s.mu.Unlock()

	log.Printf("sale stan=%s rrn=%s pan=%s proc=%s amount=%d currency=%s entry=%s wallet=%s status=%s",
		tx.Request.STAN, tx.Request.RRN, tx.Request.PAN, tx.Request.ProcessingCode, tx.Request.Amount,
		tx.Request.Currency, tx.Request.EntryMode, tx.Request.WalletProvider, tx.Status)
	writeJSON(w, http.StatusOK, tx)
}

func (s *server) handleTransactions(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	out := make([]Transaction, len(s.history))
	// Newest first.
	for i, tx := range s.history {
		out[len(s.history)-1-i] = tx
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, out)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
