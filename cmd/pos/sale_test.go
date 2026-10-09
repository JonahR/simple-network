package main

import (
	"encoding/hex"
	"testing"
	"time"

	"github.com/JonahR/simple-network/internal/demokeys"
	"github.com/JonahR/simple-network/internal/iso8583"
	"github.com/JonahR/simple-network/internal/pin"
)

var testNow = time.Date(2026, 10, 9, 14, 30, 0, 0, time.UTC)

func newTestServer() *server {
	key, _ := hex.DecodeString(demokeys.AcquirerPIN)
	loc, _ := time.LoadLocation("America/Los_Angeles")
	return &server{
		terminal:   Terminal{MerchantID: "M1", TerminalID: "T1", MerchantName: "Shop", City: "SF", Country: "US", MCC: "5814"},
		location:   loc,
		acquirerID: "100001",
		pinKey:     key,
	}
}

// Test cards, matching the browser wallet.
const (
	creditPAN     = "4242424242424242"
	debitPAN      = "4000056655665556"
	prepaidPAN    = "4358805984634941"
	fleetPAN      = "5568007143162129"
	healthPAN     = "4716006861111015"
	applePayToken = "4895372051310681"
	arqc          = "A1B2C3D4E5F60718"
	tavv          = "AJkBBkhgQQAAAE4gSEJydAAAAAA=" // 20-byte in-app token cryptogram
)

func sale(pan string, entry string) SaleInput {
	return SaleInput{CardNumber: pan, Expiry: "12/28", Amount: "10.00", Currency: "840", EntryMode: entry}
}

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

func TestKeyedSale(t *testing.T) {
	in := sale("4242 4242 4242 4242", iso8583.EntryManual)
	in.CVV = "123"
	in.Amount = "4.75"
	req, errs := newTestServer().buildAuthRequest(in, testNow)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if req.MTI != "0100" || req.PAN != creditPAN || req.Amount != 475 || req.ProcessingCode != "003000" ||
		req.Expiry != "2812" || req.STAN != "000001" || req.RRN != "628214000001" || req.EntryMode != "011" {
		t.Errorf("unexpected request: %+v", req)
	}
	// DE7 is UTC; DE12/DE13 are the merchant's local time (PDT, UTC-7).
	if req.TransmissionTime != "1009143000" || req.LocalTime != "073000" || req.LocalDate != "1009" {
		t.Errorf("DE7 %s, DE12 %s, DE13 %s", req.TransmissionTime, req.LocalTime, req.LocalDate)
	}
	if len(req.MerchantNameLoc) != 40 {
		t.Errorf("DE43 length = %d, want 40", len(req.MerchantNameLoc))
	}
}

// Each case is a valid sale; check runs against the built request.
func TestPaymentMethods(t *testing.T) {
	tests := []struct {
		name  string
		in    func() SaleInput
		check func(t *testing.T, r iso8583.AuthRequest)
	}{
		{"chip credit", func() SaleInput {
			in := sale(creditPAN, iso8583.EntryChip)
			in.Cryptogram = arqc
			return in
		}, func(t *testing.T, r iso8583.AuthRequest) {
			if r.ARQC != arqc || r.CVV2 != "" || r.PINData != "" {
				t.Errorf("%+v", r)
			}
		}},
		{"contactless card", func() SaleInput {
			in := sale(creditPAN, iso8583.EntryContactless)
			in.Cryptogram = arqc
			return in
		}, func(t *testing.T, r iso8583.AuthRequest) {
			if r.ARQC != arqc || r.WalletProvider != "" {
				t.Errorf("%+v", r)
			}
		}},
		{"swipe chipless gift card", func() SaleInput {
			in := sale(prepaidPAN, iso8583.EntryMagstripe)
			in.Track2 = prepaidPAN + "=2812101123456"
			return in
		}, func(t *testing.T, r iso8583.AuthRequest) {
			if r.Track2 == "" || r.ARQC != "" {
				t.Errorf("%+v", r)
			}
			if red := r.Redacted(func(string) string { return "MASKED" }); red.Track2 != "MASKED=****" {
				t.Errorf("redacted track2 = %q", red.Track2)
			}
		}},
		{"debit chip with PIN", func() SaleInput {
			in := sale(debitPAN, iso8583.EntryChip)
			in.Cryptogram = arqc
			in.PIN = "1234"
			return in
		}, func(t *testing.T, r iso8583.AuthRequest) {
			if r.ProcessingCode != "002000" || len(r.PINData) != 16 {
				t.Errorf("%+v", r)
			}
			// The issuer can recover the PIN block with the same key.
			got, _ := pin.Decrypt(r.PINData, newTestServer().pinKey)
			want, _ := pin.Format0("1234", debitPAN)
			if got != want {
				t.Errorf("decrypted PIN block %s, want %s", got, want)
			}
			if red := r.Redacted(func(s string) string { return s }); red.PINData != "ENCRYPTED" {
				t.Errorf("redacted PIN data = %q", red.PINData)
			}
		}},
		{"debit contactless without PIN", func() SaleInput {
			in := sale(debitPAN, iso8583.EntryContactless)
			in.Cryptogram = arqc
			return in
		}, func(t *testing.T, r iso8583.AuthRequest) {
			if r.PINData != "" || r.ProcessingCode != "002000" {
				t.Errorf("%+v", r)
			}
		}},
		{"debit with cash back", func() SaleInput {
			in := sale(debitPAN, iso8583.EntryChip)
			in.Cryptogram = arqc
			in.PIN = "1234"
			in.Cashback = "20"
			return in
		}, func(t *testing.T, r iso8583.AuthRequest) {
			if r.ProcessingCode != "092000" || r.Amount != 3000 ||
				len(r.AdditionalAmounts) != 1 || r.AdditionalAmounts[0] != (iso8583.AdditionalAmount{Type: "40", Amount: 2000}) {
				t.Errorf("%+v", r)
			}
		}},
		{"prepaid", func() SaleInput {
			in := sale(prepaidPAN, iso8583.EntryManual)
			in.CVV = "321"
			return in
		}, func(t *testing.T, r iso8583.AuthRequest) {
			if r.ProcessingCode != "000000" {
				t.Errorf("%+v", r)
			}
		}},
		{"fleet", func() SaleInput {
			in := sale(fleetPAN, iso8583.EntryChip)
			in.Cryptogram = arqc
			in.Odometer, in.VehicleID, in.DriverID = "48213", "truck12", "4821"
			return in
		}, func(t *testing.T, r iso8583.AuthRequest) {
			if r.Fleet == nil || *r.Fleet != (iso8583.FleetData{Odometer: "48213", VehicleID: "TRUCK12", DriverID: "4821"}) {
				t.Errorf("fleet = %+v", r.Fleet)
			}
		}},
		{"HSA/FSA", func() SaleInput {
			in := sale(healthPAN, iso8583.EntryChip)
			in.Cryptogram = arqc
			in.HealthcareAmount = "7.50"
			return in
		}, func(t *testing.T, r iso8583.AuthRequest) {
			if len(r.AdditionalAmounts) != 1 || r.AdditionalAmounts[0] != (iso8583.AdditionalAmount{Type: "4S", Amount: 750}) {
				t.Errorf("%+v", r.AdditionalAmounts)
			}
		}},
		{"Apple Pay in store", func() SaleInput {
			in := sale(applePayToken, iso8583.EntryContactless)
			in.Wallet, in.Cryptogram = iso8583.WalletApplePay, "a1b2c3d4e5f60718"
			return in
		}, func(t *testing.T, r iso8583.AuthRequest) {
			if r.WalletProvider != "apple_pay" || r.ARQC != arqc || r.CVV2 != "" {
				t.Errorf("%+v", r)
			}
		}},
		{"Google Pay in app", func() SaleInput {
			in := sale(applePayToken, iso8583.EntryEcommerce)
			in.Wallet, in.Cryptogram = iso8583.WalletGooglePay, tavv
			return in
		}, func(t *testing.T, r iso8583.AuthRequest) {
			// Base64 is case-sensitive, so the cryptogram must not be upper-cased.
			if r.ARQC != tavv || r.EntryMode != "812" {
				t.Errorf("%+v", r)
			}
		}},
		{"card on file, recurring", func() SaleInput {
			in := SaleInput{Amount: "29.99", Currency: "840", EntryMode: iso8583.EntryCredentialOnFile,
				CardOnFileID: "cof_acme", COFIndicator: iso8583.COFMerchantRecurring}
			return in
		}, func(t *testing.T, r iso8583.AuthRequest) {
			stored, _ := cardOnFile("cof_acme")
			if r.PAN != "5555555555554444" || r.Expiry != stored.Expiry[3:]+stored.Expiry[:2] || r.COFIndicator != "mit_recurring" || r.CVV2 != "" {
				t.Errorf("%+v", r)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, errs := newTestServer().buildAuthRequest(tt.in(), testNow)
			if len(errs) > 0 {
				t.Fatalf("unexpected errors: %v", errs)
			}
			if tt.check != nil {
				tt.check(t, req)
			}
		})
	}
}

// Each case is an invalid sale that must be rejected with an error on field.
func TestRejectedSales(t *testing.T) {
	tests := []struct {
		name  string
		field string
		in    func() SaleInput
	}{
		{"keyed without CVV", "cvv", func() SaleInput { return sale(creditPAN, iso8583.EntryManual) }},
		{"chip without cryptogram", "entry_mode", func() SaleInput { return sale(creditPAN, iso8583.EntryChip) }},
		{"swipe without track 2", "entry_mode", func() SaleInput { return sale(creditPAN, iso8583.EntryMagstripe) }},
		{"track 2 for another card", "form", func() SaleInput {
			in := sale(creditPAN, iso8583.EntryMagstripe)
			in.Track2 = debitPAN + "=2812201000000"
			return in
		}},
		{"swipe chip card", "entry_mode", func() SaleInput {
			in := sale(creditPAN, iso8583.EntryMagstripe)
			in.Track2 = creditPAN + "=2812201123456"
			return in
		}},
		{"in-app wallet with EMV cryptogram", "form", func() SaleInput {
			in := sale(applePayToken, iso8583.EntryEcommerce)
			in.Wallet, in.Cryptogram = iso8583.WalletGooglePay, arqc
			return in
		}},
		{"in-store wallet with in-app cryptogram", "form", func() SaleInput {
			in := sale(applePayToken, iso8583.EntryContactless)
			in.Wallet, in.Cryptogram = iso8583.WalletApplePay, tavv
			return in
		}},
		{"cryptogram on keyed sale", "form", func() SaleInput {
			in := sale(creditPAN, iso8583.EntryManual)
			in.CVV, in.Cryptogram = "123", arqc
			return in
		}},
		{"wallet without cryptogram", "form", func() SaleInput {
			in := sale(applePayToken, iso8583.EntryContactless)
			in.Wallet = iso8583.WalletApplePay
			return in
		}},
		{"unknown wallet", "form", func() SaleInput {
			in := sale(applePayToken, iso8583.EntryContactless)
			in.Wallet, in.Cryptogram = "foo_pay", arqc
			return in
		}},
		{"keyed wallet", "entry_mode", func() SaleInput {
			in := sale(applePayToken, iso8583.EntryManual)
			in.Wallet, in.Cryptogram = iso8583.WalletApplePay, arqc
			return in
		}},
		{"debit chip without PIN", "pin", func() SaleInput {
			in := sale(debitPAN, iso8583.EntryChip)
			in.Cryptogram = arqc
			return in
		}},
		{"PIN on keyed sale", "pin", func() SaleInput {
			in := sale(debitPAN, iso8583.EntryManual)
			in.CVV, in.PIN = "789", "1234"
			return in
		}},
		{"PIN with wallet", "pin", func() SaleInput {
			in := sale(applePayToken, iso8583.EntryContactless)
			in.Wallet, in.Cryptogram, in.PIN = iso8583.WalletApplePay, arqc, "1234"
			return in
		}},
		{"cash back on credit", "cashback", func() SaleInput {
			in := sale(creditPAN, iso8583.EntryChip)
			in.Cryptogram, in.PIN, in.Cashback = arqc, "1234", "20"
			return in
		}},
		{"cash back without PIN", "cashback", func() SaleInput {
			in := sale(debitPAN, iso8583.EntryContactless)
			in.Cryptogram, in.Cashback = arqc, "20"
			return in
		}},
		{"cash back over limit", "cashback", func() SaleInput {
			in := sale(debitPAN, iso8583.EntryChip)
			in.Cryptogram, in.PIN, in.Cashback = arqc, "1234", "200.01"
			return in
		}},
		{"fleet without odometer", "odometer", func() SaleInput {
			in := sale(fleetPAN, iso8583.EntryChip)
			in.Cryptogram, in.VehicleID, in.DriverID = arqc, "V1", "4821"
			return in
		}},
		{"fleet data on credit card", "form", func() SaleInput {
			in := sale(creditPAN, iso8583.EntryChip)
			in.Cryptogram, in.Odometer = arqc, "100"
			return in
		}},
		{"HSA without eligible amount", "healthcare_amount", func() SaleInput {
			in := sale(healthPAN, iso8583.EntryChip)
			in.Cryptogram = arqc
			return in
		}},
		{"HSA amount over sale", "healthcare_amount", func() SaleInput {
			in := sale(healthPAN, iso8583.EntryChip)
			in.Cryptogram, in.HealthcareAmount = arqc, "10.01"
			return in
		}},
		{"card on file without saved card", "card_on_file", func() SaleInput {
			in := sale("", iso8583.EntryCredentialOnFile)
			in.COFIndicator = iso8583.COFCustomerInitiated
			return in
		}},
		{"card on file without initiator", "cof_indicator", func() SaleInput {
			in := sale("", iso8583.EntryCredentialOnFile)
			in.CardOnFileID = "cof_jane"
			return in
		}},
		{"card on file with CVV", "cvv", func() SaleInput {
			in := sale("", iso8583.EntryCredentialOnFile)
			in.CardOnFileID, in.COFIndicator, in.CVV = "cof_jane", iso8583.COFCustomerInitiated, "123"
			return in
		}},
		{"saved card without card-on-file entry", "form", func() SaleInput {
			in := sale(creditPAN, iso8583.EntryManual)
			in.CVV, in.CardOnFileID = "123", "cof_jane"
			return in
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, errs := newTestServer().buildAuthRequest(tt.in(), testNow)
			if errs[tt.field] == "" {
				t.Errorf("want error on %q, got %v", tt.field, errs)
			}
		})
	}
}
