package main

import (
	"testing"
	"time"

	"github.com/JonahR/simple-network/internal/iso8583"
)

func TestParseAmount(t *testing.T) {
	tests := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"12.50", 1250, false},
		{"$3", 300, false},
		{".5", 50, false},
		{"0.01", 1, false},
		{"999999.99", 99_999_999, false},
		{"1000000", 0, true},
		{"0", 0, true},
		{"1.234", 0, true},
		{"1.", 0, true},
		{"-5", 0, true},
		{"abc", 0, true},
		{"", 0, true},
	}
	for _, tt := range tests {
		got, err := parseAmount(tt.in)
		if got != tt.want || (err != nil) != tt.wantErr {
			t.Errorf("parseAmount(%q) = %d, %v; want %d, err=%v", tt.in, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestBuildAuthRequest(t *testing.T) {
	s := &server{terminal: Terminal{MerchantID: "M1", TerminalID: "T1", MerchantName: "Shop", City: "SF", Country: "US", MCC: "5814"}}
	now := time.Date(2026, 10, 9, 14, 30, 0, 0, time.UTC)

	req, errs := s.buildAuthRequest(SaleInput{
		CardNumber: "4242 4242 4242 4242",
		Expiry:     "12/28",
		CVV:        "123",
		Amount:     "4.75",
		Currency:   "840",
		EntryMode:  iso8583.EntryManual,
	}, now)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if req.MTI != "0100" || req.PAN != "4242424242424242" || req.Amount != 475 ||
		req.Expiry != "2812" || req.STAN != "000001" || req.RRN != "628214000001" {
		t.Errorf("unexpected request: %+v", req)
	}
	if len(req.MerchantNameLoc) != 40 {
		t.Errorf("DE43 length = %d, want 40", len(req.MerchantNameLoc))
	}

	// Keyed entry without a CVV is rejected; chip without one is allowed.
	_, errs = s.buildAuthRequest(SaleInput{CardNumber: "4242424242424242", Expiry: "12/28", Amount: "1", Currency: "840", EntryMode: iso8583.EntryManual}, now)
	if errs["cvv"] == "" {
		t.Error("expected CVV error for manual entry")
	}
	_, errs = s.buildAuthRequest(SaleInput{CardNumber: "4242424242424242", Expiry: "12/28", Amount: "1", Currency: "840", EntryMode: iso8583.EntryChip}, now)
	if len(errs) > 0 {
		t.Errorf("chip without CVV: unexpected errors %v", errs)
	}
}

func TestNextSTANWraps(t *testing.T) {
	s := &server{}
	s.stan.Store(999_998)
	if got := s.nextSTAN(); got != "999999" {
		t.Errorf("got %s", got)
	}
	if got := s.nextSTAN(); got != "000001" {
		t.Errorf("got %s, want wrap to 000001", got)
	}
}

func TestBuildAuthRequestApplePay(t *testing.T) {
	s := &server{terminal: Terminal{MCC: "5814"}}
	now := time.Date(2026, 10, 9, 14, 30, 0, 0, time.UTC)
	base := SaleInput{
		CardNumber: "4895372051310681",
		Expiry:     "09/30",
		Amount:     "5.00",
		Currency:   "840",
		EntryMode:  iso8583.EntryContactless,
		Wallet:     iso8583.WalletApplePay,
		Cryptogram: "a1b2c3d4e5f60718",
	}

	req, errs := s.buildAuthRequest(base, now)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if req.WalletProvider != "apple_pay" || req.Cryptogram != "A1B2C3D4E5F60718" || req.CVV2 != "" {
		t.Errorf("unexpected wallet fields: %+v", req)
	}

	// In-app (e-commerce) wallet payments need no CVV either.
	inApp := base
	inApp.EntryMode = iso8583.EntryEcommerce
	if _, errs := s.buildAuthRequest(inApp, now); len(errs) > 0 {
		t.Errorf("in-app wallet: unexpected errors %v", errs)
	}

	bad := map[string]func(*SaleInput){
		"missing cryptogram":    func(in *SaleInput) { in.Cryptogram = "" },
		"short cryptogram":      func(in *SaleInput) { in.Cryptogram = "ABC" },
		"unknown wallet":        func(in *SaleInput) { in.Wallet = "foo_pay" },
		"keyed wallet":          func(in *SaleInput) { in.EntryMode = iso8583.EntryManual },
		"cryptogram, no wallet": func(in *SaleInput) { in.Wallet = ""; in.EntryMode = iso8583.EntryChip },
	}
	for name, mutate := range bad {
		in := base
		mutate(&in)
		if _, errs := s.buildAuthRequest(in, now); len(errs) == 0 {
			t.Errorf("%s: expected an error", name)
		}
	}
}
