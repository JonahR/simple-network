package pin

import (
	"encoding/hex"
	"testing"
)

func TestFormat0(t *testing.T) {
	// PIN field 041234FFFFFFFFFF XOR PAN field 0000111111111111.
	got, err := Format0("1234", "4111111111111111")
	if err != nil {
		t.Fatal(err)
	}
	if got != "041225EEEEEEEEEE" {
		t.Errorf("Format0 = %s", got)
	}
}

func TestFormat0ShortPAN(t *testing.T) {
	// A 12-digit PAN leaves 11 digits without the check digit, so the PAN
	// field is left-padded to 0000012345678901.
	got, err := Format0("1234", "123456789012")
	if err != nil {
		t.Fatal(err)
	}
	if got != "041235DCBA9876FE" {
		t.Errorf("Format0 = %s", got)
	}
}

func TestEncryptRoundTrip(t *testing.T) {
	key, _ := hex.DecodeString("0123456789ABCDEFFEDCBA9876543210")
	enc, err := Encrypt("1234", "4111111111111111", key)
	if err != nil {
		t.Fatal(err)
	}
	if len(enc) != 16 || enc == "041225EEEEEEEEEE" {
		t.Fatalf("Encrypt = %s", enc)
	}
	dec, err := Decrypt(enc, key)
	if err != nil {
		t.Fatal(err)
	}
	if dec != "041225EEEEEEEEEE" {
		t.Errorf("Decrypt = %s", dec)
	}
}

func TestValidate(t *testing.T) {
	for _, bad := range []string{"123", "1234567890123", "12a4", ""} {
		if Validate(bad) == nil {
			t.Errorf("Validate(%q) = nil", bad)
		}
	}
	if err := Validate("0000"); err != nil {
		t.Error(err)
	}
}

func TestTranslateAndExtract(t *testing.T) {
	acq, _ := hex.DecodeString("0123456789ABCDEFFEDCBA9876543210")
	iss, _ := hex.DecodeString("A1B2C3D4E5F60718293A4B5C6D7E8F90")
	pan := "4000056655665556"

	enc, err := Encrypt("2468", pan, acq)
	if err != nil {
		t.Fatal(err)
	}
	translated, err := Translate(enc, acq, iss)
	if err != nil {
		t.Fatal(err)
	}
	if translated == enc {
		t.Fatal("translation did not change the block")
	}
	got, err := Extract(translated, pan, iss)
	if err != nil || got != "2468" {
		t.Fatalf("Extract = %q, %v", got, err)
	}
	// The wrong key yields garbage, not the PIN.
	if got, err := Extract(translated, pan, acq); err == nil && got == "2468" {
		t.Fatal("extracted PIN with the wrong key")
	}
}
