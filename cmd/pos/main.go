// Command pos runs a browser-based point-of-sale terminal. It reads a card
// (keyed, swiped, inserted, tapped, from a mobile wallet, or from the
// merchant's card-on-file vault), prompts for whatever the card requires, and
// builds an ISO 8583-style 0100 authorization request. It sends the request to
// the merchant's acquirer, which forwards it to the card network.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/JonahR/simple-network/internal/merchant"
	"github.com/JonahR/simple-network/internal/store"
	"github.com/JonahR/simple-network/internal/ui"
)

//go:embed web
var webFS embed.FS

// Terminal holds the merchant configuration provisioned onto one terminal.
type Terminal struct {
	MerchantID   string `json:"merchant_id"`
	TerminalID   string `json:"terminal_id"`
	MerchantName string `json:"merchant_name"`
	City         string `json:"city"`
	Country      string `json:"country"`
	MCC          string `json:"mcc"`
	TimeZone     string `json:"time_zone"` // IANA zone for DE12/DE13 local time
}

// lane is one merchant's terminal on this POS server. The cashier picks
// which merchant to ring up for; every request names its terminal.
type lane struct {
	Terminal
	loc *time.Location // TimeZone, loaded
}

// lane returns the lane for a terminal ID; "" means the default (first) one.
func (s *server) lane(terminalID string) (lane, bool) {
	if terminalID == "" {
		return s.lanes[0], true
	}
	for _, l := range s.lanes {
		if l.TerminalID == terminalID {
			return l, true
		}
	}
	return lane{}, false
}

// newLanes gives each merchant in the catalog its first terminal.
func newLanes(merchants []merchant.Merchant) ([]lane, error) {
	out := make([]lane, len(merchants))
	for i, m := range merchants {
		loc, err := time.LoadLocation(m.TimeZone)
		if err != nil {
			return nil, fmt.Errorf("%s: MERCHANT_TZ must be an IANA time zone like America/New_York: %v", m.Name, err)
		}
		out[i] = lane{Terminal{
			MerchantID: m.ID, TerminalID: m.Terminals[0], MerchantName: m.Name,
			City: m.City, Country: m.Country, MCC: m.MCC, TimeZone: m.TimeZone,
		}, loc}
	}
	return out, nil
}

// SaleInput is what the cashier enters for one sale.
type SaleInput struct {
	CardNumber     string `json:"card_number"`
	Expiry         string `json:"expiry"`
	CVV            string `json:"cvv"`
	CardholderName string `json:"cardholder_name"`
	TerminalID     string `json:"terminal_id"` // Which merchant's lane; "" for the default
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

	// Simulation: send the 0100 but drop the 0110, as if the line went dead.
	// The terminal then reverses the sale it never heard back about.
	LoseResponse bool `json:"lose_response"`

	// SplitOf collects the balance due on an earlier partially approved sale
	// (its STAN) with this card.
	SplitOf string `json:"split_of"`

	// BalanceInquiry asks the issuer for the card's available balance
	// (processing code 30) instead of charging it.
	BalanceInquiry bool `json:"balance_inquiry"`

	// Card-not-present checks for keyed and online sales. The billing
	// address goes to the issuer for AVS; ThreeDS authenticates the shopper
	// with the issuer before the 0100 is sent.
	BillingZip    string `json:"billing_zip"`
	BillingStreet string `json:"billing_street"`
	ThreeDS       bool   `json:"three_ds"`

	// PreAuth holds an estimate instead of charging a final amount; the
	// cashier captures the final amount later.
	PreAuth bool `json:"pre_auth"`

	// Refund sends money back to the card (processing code 20). The card is
	// presented again, as for a sale. RefundOf links it to an earlier sale
	// (its STAN): the card must be the same and the total refunded can't
	// exceed what that sale charged.
	Refund   bool   `json:"refund"`
	RefundOf string `json:"refund_of"`
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
	Status   string                 `json:"status"` // APPROVED, PARTIAL, DECLINED, NO_RESPONSE, VOIDED, or REFUND (an approved refund)
	Message  string                 `json:"message"`
	Request  iso8583.AuthRequest    `json:"request"`            // Redacted
	Response *iso8583.AuthResponse  `json:"response,omitempty"` // Redacted; nil if the acquirer never answered
	Created  time.Time              `json:"created"`
	Reversal *Reversal              `json:"reversal,omitempty"` // Set once the terminal starts undoing the sale
	ThreeDS  *iso8583.ThreeDSResult `json:"three_ds,omitempty"` // The 3-D Secure outcome, for online sales that used it

	// Split tender. A partial approval leaves a balance due on the first
	// tender; each further tender (card or cash) names it in SplitOf and
	// reduces its BalanceDue.
	Tender     string `json:"tender"`                // "card" or "cash"
	SplitOf    string `json:"split_of,omitempty"`    // STAN of the first tender
	BalanceDue int64  `json:"balance_due,omitempty"` // On the first tender: still to collect

	// Pre-auths and tips. An increment names the pre-auth it adds to; the
	// final amount, once captured, is what clearing presents.
	IncrementOf string                      `json:"increment_of,omitempty"` // STAN of the pre-auth
	Authorized  int64                       `json:"authorized,omitempty"`   // Approved so far, increments included
	Captured    int64                       `json:"captured,omitempty"`     // Final amount sent in the 0220
	Completion  *iso8583.CompletionResponse `json:"completion,omitempty"`   // The acquirer's 0230

	// Refunds. A refund names the sale it gives money back for; the sale
	// keeps the running total refunded, so it can't be refunded twice.
	RefundOf string `json:"refund_of,omitempty"` // On a refund: STAN of the sale
	Refunded int64  `json:"refunded,omitempty"`  // On a sale: approved refunds so far

	// The 0100 as sent, unmasked, so a reversal can identify the sale. Never
	// serialized; the PIN block, CVV, track data and cryptogram are dropped.
	sent iso8583.AuthRequest
}

type server struct {
	lanes        []lane // One per merchant; the first is the default
	pinKey       []byte // PIN key shared between the acquirer and the network
	acquirer     *acquirerClient
	dashboardURL string
	acquirerURL  string // For the browser
	stan         atomic.Uint32

	mu      sync.Mutex
	history []*Transaction

	reversalRetry time.Duration // Wait between attempts to deliver an 0420

	chipFailures map[string]time.Time   // PAN -> last failed chip read, for fallback swipes
	challenges   map[string]*pending3DS // 3-D Secure ID -> sale waiting for the shopper's code
}

func main() {
	// The same catalog and settings the acquirer onboards, so every lane is
	// a terminal it recognizes.
	lanes, err := newLanes(merchant.FromEnv(os.Getenv))
	if err != nil {
		log.Fatal(err)
	}
	addr := env("POS_ADDR", ":8080")

	pinKey, err := demokeys.Load("ACQUIRER_PIN_KEY", demokeys.AcquirerPIN)
	if err != nil {
		log.Fatal(err)
	}

	s := &server{
		lanes:        lanes,
		pinKey:       pinKey,
		acquirer:     newAcquirerClient(env("ACQUIRER_URL", "http://localhost:8081")),
		dashboardURL: env("NETWORK_DASHBOARD_URL", "http://localhost:8090"),
		acquirerURL:  env("ACQUIRER_PUBLIC_URL", "http://localhost:8081"),

		reversalRetry: 3 * time.Second,
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
	mux.HandleFunc("POST /api/sale", s.handleSale)
	mux.HandleFunc("POST /api/3ds/challenge", s.handleChallenge)
	mux.HandleFunc("POST /api/increment", s.handleIncrement)
	mux.HandleFunc("POST /api/capture", s.handleCapture)
	mux.HandleFunc("POST /api/void", s.handleVoid)
	mux.HandleFunc("POST /api/chip-failure", s.handleChipFailure)
	mux.HandleFunc("POST /api/cash", s.handleCash)
	mux.HandleFunc("GET /api/transactions", s.handleTransactions)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	ui.Register(mux, ui.PagesFromEnv())

	log.Printf("POS with %d merchant lanes (default %s, terminal %s) listening on %s", len(lanes), lanes[0].MerchantName, lanes[0].TerminalID, addr)
	// Saved state survives restarts (make serve redeploys on every merge).
	st := store.Open(store.Dir(), "pos")
	var saved posState
	if ok, err := st.Load(&saved); err != nil {
		log.Printf("store: %v", err)
	} else if ok {
		s.restoreState(saved)
		log.Printf("restored %d sales from %s", len(saved.History), st.Path())
	}
	st.Run(func() any { return s.saveState() }, store.DefaultInterval)
	store.SaveOnExit(nil, st)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func (s *server) handleTerminal(w http.ResponseWriter, _ *http.Request) {
	saved := make([]map[string]string, len(cardsOnFile))
	for i, c := range cardsOnFile {
		saved[i] = map[string]string{"id": c.ID, "customer": c.Customer, "masked": card.Mask(c.PAN)}
	}
	terminals := make([]Terminal, len(s.lanes))
	for i, l := range s.lanes {
		terminals[i] = l.Terminal
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"terminal":       s.lanes[0].Terminal, // The default
		"terminals":      terminals,
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

func (s *server) handleSale(w http.ResponseWriter, r *http.Request) {
	var in SaleInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"errors": map[string]string{"form": "invalid request body"}})
		return
	}
	req, fieldErrs := s.buildAuthRequest(in, time.Now())
	if len(fieldErrs) == 0 && in.SplitOf != "" {
		if msg := s.checkSplit(in.SplitOf, req.Amount, req.Currency); msg != "" {
			fieldErrs = fieldErrors{"amount": msg}
		}
	}
	if len(fieldErrs) == 0 && in.RefundOf != "" {
		if field, msg := s.checkRefund(in.RefundOf, req); msg != "" {
			fieldErrs = fieldErrors{field: msg}
		} else {
			// Name the sale in the 0100 too, so the network, clearing and the
			// issuer can tie the credit to it (and a chargeback can find it).
			req.OriginalTxnID = s.networkTxnOf(in.RefundOf)
		}
	}
	if len(fieldErrs) > 0 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"errors": fieldErrs})
		return
	}

	if in.ThreeDS {
		s.startThreeDS(w, r, in, req)
		return
	}
	writeJSON(w, http.StatusOK, s.completeSale(r.Context(), in, req, nil))
}

// completeSale sends the 0100, records the outcome, and reverses the sale if
// no answer came back.
func (s *server) completeSale(ctx context.Context, in SaleInput, req iso8583.AuthRequest, threeDS *iso8583.ThreeDSResult) Transaction {
	tx := &Transaction{Request: req.Redacted(card.Mask), Created: time.Now(), sent: forReversal(req), Tender: "card", SplitOf: in.SplitOf, ThreeDS: threeDS, RefundOf: in.RefundOf}
	resp, err := s.acquirer.authorize(ctx, req)
	if err == nil && in.LoseResponse {
		err = errors.New("the 0110 was lost on the way back (simulated)")
	}
	if err != nil {
		tx.Status, tx.Message = "NO_RESPONSE", "No response from the acquirer: "+err.Error()
	} else {
		redacted := resp.Redacted(card.Mask)
		tx.Response = &redacted
		tx.Status, tx.Message = outcome(req, resp)
	}
	s.mu.Lock()
	s.history = append(s.history, tx)
	s.applyTender(tx, req.Amount)
	if tx.Status == "REFUND" && tx.RefundOf != "" {
		if sale := s.findTx(tx.RefundOf); sale != nil {
			sale.Refunded += tx.Response.Amount
		}
	}
	s.mu.Unlock()

	// D9: a terminal that never hears back can't know whether the issuer
	// approved and placed a hold, so it reverses the sale.
	if tx.Status == "NO_RESPONSE" {
		s.startReversal(ctx, tx, iso8583.RCLateResponse)
	}

	log.Printf("sale stan=%s rrn=%s pan=%s proc=%s amount=%d currency=%s entry=%s wallet=%s eci=%s status=%s",
		tx.Request.STAN, tx.Request.RRN, tx.Request.PAN, tx.Request.ProcessingCode, tx.Request.Amount,
		tx.Request.Currency, tx.Request.EntryMode, tx.Request.WalletProvider, tx.Request.ECI, tx.Status)
	return s.snapshot(tx)
}

// handleTransactions lists sales newest first, for one terminal when
// ?terminal= names it.
func (s *server) handleTransactions(w http.ResponseWriter, r *http.Request) {
	only := r.URL.Query().Get("terminal")
	s.mu.Lock()
	out := []Transaction{}
	for i := len(s.history) - 1; i >= 0; i-- {
		if tx := s.history[i]; only == "" || tx.Request.TerminalID == only {
			out = append(out, s.copyTx(tx))
		}
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, out)
}

// snapshot copies a transaction under the lock, since a reversal may be
// updating it in the background.
func (s *server) snapshot(tx *Transaction) Transaction {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.copyTx(tx)
}

// copyTx copies tx for the browser, with its authorized total worked out
// from any increments. The caller holds s.mu.
func (s *server) copyTx(tx *Transaction) Transaction {
	c := copyTx(tx)
	if tx.Request.AuthType == iso8583.AuthEstimated {
		c.Authorized = s.authorizedTotal(tx)
	}
	if tx.Completion != nil {
		comp := *tx.Completion
		c.Completion = &comp
	}
	return c
}

func copyTx(tx *Transaction) Transaction {
	c := *tx
	if tx.Reversal != nil {
		r := *tx.Reversal
		c.Reversal = &r
	}
	return c
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
