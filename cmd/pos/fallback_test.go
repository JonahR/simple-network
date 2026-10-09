package main

import (
	"testing"

	"github.com/JonahR/simple-network/internal/iso8583"
)

func swipe(pan, serviceCode, entry string) SaleInput {
	in := sale(pan, entry)
	in.Track2 = pan + "=2812" + serviceCode + "123456"
	return in
}

func TestFallbackSwipe(t *testing.T) {
	s := newTestServer()

	// A plain swipe of a chip card is refused: the terminal must try the chip.
	if _, errs := s.buildAuthRequest(swipe(creditPAN, "201", iso8583.EntryMagstripe), testNow); errs["entry_mode"] == "" {
		t.Error("plain swipe of a chip card accepted")
	}
	// A fallback without a failed chip read first is refused too.
	if _, errs := s.buildAuthRequest(swipe(creditPAN, "201", iso8583.EntryFallback), testNow); errs["entry_mode"] == "" {
		t.Error("fallback accepted without a failed chip read")
	}

	s.recordChipFailure(creditPAN, testNow)
	req, errs := s.buildAuthRequest(swipe(creditPAN, "201", iso8583.EntryFallback), testNow.Add(30_000_000_000))
	if len(errs) > 0 {
		t.Fatalf("fallback after a chip failure: %v", errs)
	}
	if req.EntryMode != "801" || req.Track2 == "" || req.ARQC != "" {
		t.Errorf("0100 entry mode %s, track2 %q, ARQC %q", req.EntryMode, req.Track2, req.ARQC)
	}

	// Only the card whose chip failed, and only for a short while.
	if _, errs := s.buildAuthRequest(swipe(creditPAN, "201", iso8583.EntryFallback), testNow.Add(chipFailureWindow)); errs["entry_mode"] == "" {
		t.Error("fallback still allowed after the window")
	}
	if _, errs := s.buildAuthRequest(swipe(prepaidPAN, "101", iso8583.EntryFallback), testNow); errs["entry_mode"] == "" {
		t.Error("fallback accepted for a card without a chip")
	}
}

func TestFallbackDebitNeedsPIN(t *testing.T) {
	s := newTestServer()
	s.recordChipFailure(debitPAN, testNow)
	if _, errs := s.buildAuthRequest(swipe(debitPAN, "201", iso8583.EntryFallback), testNow); errs["pin"] == "" {
		t.Error("debit fallback swipe without a PIN accepted")
	}
}
