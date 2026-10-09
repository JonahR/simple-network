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
