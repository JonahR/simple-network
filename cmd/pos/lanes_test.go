package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/JonahR/simple-network/internal/iso8583"
	"github.com/JonahR/simple-network/internal/merchant"
)

func newLanesServer(t *testing.T) *server {
	t.Helper()
	lanes, err := newLanes(merchant.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	s := newTestServer()
	s.lanes = lanes
	return s
}

func TestSaleUsesThePickedMerchant(t *testing.T) {
	s := newLanesServer(t)
	in := sale(creditPAN, iso8583.EntryManual)
	in.CVV = "123"
	in.TerminalID = "BOOK0001" // Northern Lights Books, Anchorage

	req, errs := s.buildAuthRequest(in, testNow) // 14:30 UTC
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if req.TerminalID != "BOOK0001" || req.MerchantID != "000000000134567" || req.MCC != "5942" {
		t.Errorf("DE41/DE42/DE18 = %s/%s/%s, want the bookshop's", req.TerminalID, req.MerchantID, req.MCC)
	}
	if req.LocalTime != "063000" { // Anchorage is UTC-8 in October
		t.Errorf("DE12 = %s, want Anchorage local time 063000", req.LocalTime)
	}
	if got := req.MerchantNameLoc[:21]; got != "Northern Lights Books" {
		t.Errorf("DE43 starts %q", got)
	}
}

func TestSaleDefaultsToTheFirstMerchant(t *testing.T) {
	s := newLanesServer(t)
	in := sale(creditPAN, iso8583.EntryManual)
	in.CVV = "123"
	req, errs := s.buildAuthRequest(in, testNow)
	if len(errs) > 0 || req.TerminalID != merchant.Catalog[0].Terminals[0] {
		t.Errorf("terminal = %s, errs = %v; want the default lane", req.TerminalID, errs)
	}
}

func TestSaleRejectsUnknownTerminal(t *testing.T) {
	s := newLanesServer(t)
	in := sale(creditPAN, iso8583.EntryManual)
	in.TerminalID = "NOPE0001"
	if _, errs := s.buildAuthRequest(in, testNow); errs["form"] == "" {
		t.Error("an unknown terminal must be rejected")
	}
}

func TestTerminalListsEveryLane(t *testing.T) {
	s := newLanesServer(t)
	rec := httptest.NewRecorder()
	s.handleTerminal(rec, httptest.NewRequest(http.MethodGet, "/api/terminal", nil))
	var out struct {
		Terminal  Terminal   `json:"terminal"`
		Terminals []Terminal `json:"terminals"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Terminals) != 13 || out.Terminal.TerminalID != out.Terminals[0].TerminalID {
		t.Errorf("got %d terminals, default %s", len(out.Terminals), out.Terminal.TerminalID)
	}
}

func TestTransactionsFilterByTerminal(t *testing.T) {
	s := newLanesServer(t)
	for _, id := range []string{"TERM0001", "SURF0001", "SURF0001"} {
		s.history = append(s.history, &Transaction{Request: iso8583.AuthRequest{TerminalID: id}})
	}
	count := func(url string) int {
		rec := httptest.NewRecorder()
		s.handleTransactions(rec, httptest.NewRequest(http.MethodGet, url, nil))
		var out []Transaction
		json.NewDecoder(rec.Body).Decode(&out)
		return len(out)
	}
	if n := count("/api/transactions?terminal=SURF0001"); n != 2 {
		t.Errorf("surf shop has %d sales, want 2", n)
	}
	if n := count("/api/transactions"); n != 3 {
		t.Errorf("unfiltered list has %d sales, want 3", n)
	}
}
