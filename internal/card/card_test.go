package card

import (
	"errors"
	"testing"
	"time"
)

func TestValidatePAN(t *testing.T) {
	tests := []struct {
		pan  string
		want error
	}{
		{"4242424242424242", nil},
		{"5555555555554444", nil},
		{"378282246310005", nil},
		{"4242424242424241", ErrLuhn},
		{"42424242", ErrPANFormat},
		{"4242a24242424242", ErrPANFormat},
		{"", ErrPANFormat},
	}
	for _, tt := range tests {
		if got := ValidatePAN(tt.pan); !errors.Is(got, tt.want) {
			t.Errorf("ValidatePAN(%q) = %v, want %v", tt.pan, got, tt.want)
		}
	}
}

func TestNormalize(t *testing.T) {
	if got := Normalize("4242 4242-4242 4242"); got != "4242424242424242" {
		t.Errorf("Normalize = %q", got)
	}
}

func TestMask(t *testing.T) {
	if got := Mask("4242424242424242"); got != "424242******4242" {
		t.Errorf("Mask = %q", got)
	}
	if got := Mask("1234567890"); got != "**********" {
		t.Errorf("Mask short = %q", got)
	}
}

func TestParseExpiry(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		in      string
		want    string
		wantErr error
	}{
		{"12/28", "2812", nil},
		{"1028", "2810", nil},
		{"10/26", "2610", nil}, // valid through end of its expiry month
		{"09/26", "", ErrExpired},
		{"13/28", "", ErrExpiry},
		{"00/28", "", ErrExpiry},
		{"1/28", "", ErrExpiry},
	}
	for _, tt := range tests {
		got, err := ParseExpiry(tt.in, now)
		if got != tt.want || !errors.Is(err, tt.wantErr) {
			t.Errorf("ParseExpiry(%q) = %q, %v; want %q, %v", tt.in, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestValidateCVV(t *testing.T) {
	for _, ok := range []string{"123", "1234"} {
		if err := ValidateCVV(ok); err != nil {
			t.Errorf("ValidateCVV(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"12", "12345", "12a", ""} {
		if err := ValidateCVV(bad); err == nil {
			t.Errorf("ValidateCVV(%q) = nil, want error", bad)
		}
	}
}

func TestGenerate(t *testing.T) {
	for range 100 {
		pan, err := Generate("400000", 16)
		if err != nil {
			t.Fatal(err)
		}
		if len(pan) != 16 || BIN(pan) != "400000" {
			t.Fatalf("Generate = %q", pan)
		}
		if err := ValidatePAN(pan); err != nil {
			t.Fatalf("generated PAN %q invalid: %v", pan, err)
		}
	}
}
