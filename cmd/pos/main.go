// Command pos runs a browser-based point-of-sale terminal. It collects card
// and amount details, validates them, and builds an ISO 8583-style 0100
// authorization request. Sending the request to the network comes later.
package main

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/JonahR/simple-network/internal/card"
	"github.com/JonahR/simple-network/internal/iso8583"
)

//go:embed web
var webFS embed.FS

// maxAmount is the largest amount the terminal accepts, in cents.
const maxAmount = 99_999_999

// Terminal holds the merchant configuration provisioned onto this terminal.
type Terminal struct {
	MerchantID   string `json:"merchant_id"`
	TerminalID   string `json:"terminal_id"`
	MerchantName string `json:"merchant_name"`
	City         string `json:"city"`
	Country      string `json:"country"`
	MCC          string `json:"mcc"`
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
}

// Transaction is a sale as recorded by the terminal.
type Transaction struct {
	Status  string              `json:"status"`
	Message string              `json:"message"`
	Request iso8583.AuthRequest `json:"request"` // redacted
	Created time.Time           `json:"created"`
}

type server struct {
	terminal Terminal
	stan     atomic.Uint32

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
	}
	addr := env("POS_ADDR", ":8080")

	s := &server{terminal: t}
	static, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(static))
	mux.HandleFunc("GET /api/terminal", s.handleTerminal)
	mux.HandleFunc("GET /api/test-card", s.handleTestCard)
	mux.HandleFunc("POST /api/sale", s.handleSale)
	mux.HandleFunc("GET /api/transactions", s.handleTransactions)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })

	log.Printf("POS terminal %s (merchant %s) listening on %s", t.TerminalID, t.MerchantID, addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func (s *server) handleTerminal(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"terminal":    s.terminal,
		"entry_modes": iso8583.EntryModes,
		"currencies":  iso8583.Currencies,
	})
}

func (s *server) handleTestCard(w http.ResponseWriter, _ *http.Request) {
	pan, err := card.Generate("400000", 16)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	expiry := time.Now().AddDate(3, 0, 0).Format("01/06")
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

	// The network does not exist yet, so the request is built but not sent.
	tx := Transaction{
		Status:  "NOT_SENT",
		Message: "Authorization request built. Network not connected yet.",
		Request: req.Redacted(card.Mask),
		Created: time.Now(),
	}
	s.mu.Lock()
	s.history = append(s.history, tx)
	s.mu.Unlock()

	log.Printf("sale stan=%s rrn=%s pan=%s amount=%d currency=%s status=%s",
		tx.Request.STAN, tx.Request.RRN, tx.Request.PAN, tx.Request.Amount, tx.Request.Currency, tx.Status)
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

// buildAuthRequest validates the cashier's input and builds an 0100 message.
// It returns a map of field name to error message when input is invalid.
func (s *server) buildAuthRequest(in SaleInput, now time.Time) (iso8583.AuthRequest, map[string]string) {
	errs := map[string]string{}

	pan := card.Normalize(in.CardNumber)
	if err := card.ValidatePAN(pan); err != nil {
		errs["card_number"] = err.Error()
	}
	expiry, err := card.ParseExpiry(in.Expiry, now)
	if err != nil {
		errs["expiry"] = err.Error()
	}
	// CVV is required for keyed and e-commerce sales, where the card is not read.
	cvv := strings.TrimSpace(in.CVV)
	if cvv != "" || in.EntryMode == iso8583.EntryManual || in.EntryMode == iso8583.EntryEcommerce {
		if err := card.ValidateCVV(cvv); err != nil {
			errs["cvv"] = err.Error()
		}
	}
	amount, err := parseAmount(in.Amount)
	if err != nil {
		errs["amount"] = err.Error()
	}
	if _, ok := iso8583.Currencies[in.Currency]; !ok {
		errs["currency"] = "unsupported currency"
	}
	if _, ok := iso8583.EntryModes[in.EntryMode]; !ok {
		errs["entry_mode"] = "unsupported entry mode"
	}
	if len(errs) > 0 {
		return iso8583.AuthRequest{}, errs
	}

	stan := s.nextSTAN()
	utc := now.UTC()
	t := s.terminal
	return iso8583.AuthRequest{
		MTI:              iso8583.MTIAuthRequest,
		PAN:              pan,
		ProcessingCode:   iso8583.ProcPurchase,
		Amount:           amount,
		TransmissionTime: utc.Format("0102150405"),
		STAN:             stan,
		LocalTime:        now.Format("150405"),
		LocalDate:        now.Format("0102"),
		Expiry:           expiry,
		MCC:              t.MCC,
		EntryMode:        in.EntryMode,
		// RRN: last digit of year, day of year, hour, then the STAN (12 chars).
		RRN:             fmt.Sprintf("%s%03d%02d%s", utc.Format("2006")[3:], utc.YearDay(), utc.Hour(), stan),
		TerminalID:      t.TerminalID,
		MerchantID:      t.MerchantID,
		MerchantNameLoc: fmt.Sprintf("%-25.25s%-13.13s%2.2s", t.MerchantName, t.City, t.Country),
		Currency:        in.Currency,
		CVV2:            cvv,
		CardholderName:  strings.TrimSpace(in.CardholderName),
	}, nil
}

// nextSTAN returns the next 6-digit system trace audit number, 000001-999999.
func (s *server) nextSTAN() string {
	n := s.stan.Add(1)
	return fmt.Sprintf("%06d", (n-1)%999_999+1)
}

// parseAmount converts a decimal string like "12.50" into cents without
// going through floating point.
func parseAmount(s string) (int64, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "$")
	whole, frac, hasFrac := strings.Cut(s, ".")
	if whole == "" {
		whole = "0"
	}
	if hasFrac && (len(frac) == 0 || len(frac) > 2) {
		return 0, errors.New("amount must have at most 2 decimal places")
	}
	frac += strings.Repeat("0", 2-len(frac))
	w, err1 := strconv.ParseUint(whole, 10, 63)
	f, err2 := strconv.ParseUint(frac, 10, 63)
	if err1 != nil || err2 != nil {
		return 0, errors.New("amount must be a number like 12.50")
	}
	cents := int64(w)*100 + int64(f)
	if cents <= 0 {
		return 0, errors.New("amount must be greater than zero")
	}
	if cents > maxAmount || w > maxAmount {
		return 0, errors.New("amount exceeds terminal limit")
	}
	return cents, nil
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
