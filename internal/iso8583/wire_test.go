package iso8583

import (
	"encoding/hex"
	"strings"
	"testing"
)

func keyedSale() AuthRequest {
	return AuthRequest{
		MTI: "0100", PAN: "424242******4242", ProcessingCode: "003000", Amount: 1250,
		TransmissionTime: "1009204205", STAN: "406167", LocalTime: "134205", LocalDate: "1009",
		Expiry: "3312", MCC: "5814", EntryMode: "011", AcquirerID: "100001", RRN: "628220406167",
		TerminalID: "TERM0001", MerchantID: "000000000012345",
		MerchantNameLoc: "Simple Coffee Co         San FranciscoUS", Currency: "840",
	}
}

func TestEncodeAuthRequestBytes(t *testing.T) {
	w, err := EncodeAuthRequest(keyedSale())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := hex.DecodeString(w.Hex)
	// Fields 2 3 4 7 11 12 13 14 18 22 32 37 41 42 43 49.
	// Bit n is byte (n-1)/8, counting from the high bit: 2 3 4 7 → 0111 0010 = 72,
	// 11 12 13 14 → 3c, 18 22 → 44, 32 → 01, 37 → 08, 41 42 43 → e0, 49 → 80.
	wantBitmap := "723c440108e08000"
	if got := hex.EncodeToString(b[4:12]); got != wantBitmap {
		t.Errorf("bitmap = %s, want %s", got, wantBitmap)
	}
	want := "0100" + "\x72\x3c\x44\x01\x08\xe0\x80\x00" +
		"16424242******4242" + "003000" + "000000001250" + "1009204205" + "406167" + "134205" + "1009" + "3312" +
		"5814" + "011" + "06100001" + "628220406167" + "TERM0001" + "000000000012345" +
		"Simple Coffee Co         San FranciscoUS" + "840"
	if string(b) != want {
		t.Errorf("bytes =\n%q\nwant\n%q", b, want)
	}

	// The map covers every byte once, in order, and agrees with the bits.
	next, bits := 0, []int{}
	for _, f := range w.Fields {
		if f.Offset != next {
			t.Fatalf("%s starts at %d, want %d", f.ID, f.Offset, next)
		}
		next += f.Length
		if f.Bit > 0 {
			bits = append(bits, f.Bit)
		}
	}
	if next != len(b) {
		t.Errorf("fields cover %d bytes of %d", next, len(b))
	}
	if len(bits) != len(w.Bits) {
		t.Errorf("fields have bits %v, bitmap %v", bits, w.Bits)
	}
	de2 := w.Fields[2]
	if de2.ID != "DE2" || !de2.Masked || de2.Format != "LLVAR n..19" || de2.Parts[0].Label != "Length 16" {
		t.Errorf("DE2 = %+v", de2)
	}
}

func TestEncodeAuthRequestChipPINAndAVS(t *testing.T) {
	r := keyedSale()
	r.EntryMode, r.ARQC, r.PINData = "051", "A1B2C3D4E5F60718", "ENCRYPTED"
	r.AVS = &AVSData{Zip: "94105", Street: "123 Market St"}
	r.ECI = "05"
	w, err := EncodeAuthRequest(r)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := hex.DecodeString(w.Hex)
	byID := map[string]WireField{}
	for _, f := range w.Fields {
		byID[f.ID] = f
	}
	slice := func(id string) []byte { f := byID[id]; return b[f.Offset : f.Offset+f.Length] }

	// DE123 is past 64, so bit 1 announces a secondary bitmap with bit 123 set.
	if w.Bits[0] != 1 || byID["BITMAP"].Length != 8 || slice("BITMAP")[0]&0x80 == 0 {
		t.Errorf("no secondary bitmap flagged: bits %v", w.Bits)
	}
	if got := hex.EncodeToString(slice("BITMAP2")); got != "0000000000000020" {
		t.Errorf("secondary bitmap = %s, want bit 123 set", got)
	}
	if got := hex.EncodeToString(slice("DE55")); got != hex.EncodeToString([]byte("011"))+"9f2608a1b2c3d4e5f60718" {
		t.Errorf("DE55 = %s, want LLL 011 then TLV 9F26 08 <ARQC>", got)
	}
	if got := string(slice("DE52")); got != "********" || !byID["DE52"].Masked {
		t.Errorf("DE52 = %q, want 8 masked bytes", got)
	}
	if got := string(slice("DE48")); got != "006"+"300205" {
		t.Errorf("DE48 = %q, want SE30 ECI", got)
	}
	if got := string(slice("DE123")); got != "022"+"94105    123 Market St" {
		t.Errorf("DE123 = %q", got)
	}
}

func TestEncodeAuthRequestRejectsOversizeField(t *testing.T) {
	r := keyedSale()
	r.TerminalID = "TERMINAL9"
	if _, err := EncodeAuthRequest(r); err == nil || !strings.Contains(err.Error(), "DE41") {
		t.Errorf("err = %v, want DE41 too long", err)
	}
}
