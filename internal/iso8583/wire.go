package iso8583

import (
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// The services in simple-network exchange messages as JSON over HTTP, which
// is easy to read. A real network link carries the same message as ISO 8583
// bytes. EncodeAuthRequest builds those bytes for an 0100 and says which
// bytes carry which field, so a page can show the message as it would
// travel.
//
// Encoding, as on many host-to-host links: the MTI and every field in ASCII,
// the bitmaps and the PIN block in binary, and variable-length fields led by
// their length in ASCII digits (LL for up to 99, LLL for up to 999). DE48 is
// private-use data; real networks each define its subelements, and the tags
// here are simple-network's own.

// Wire is a message encoded as ISO 8583 bytes.
type Wire struct {
	Hex    string      `json:"hex"`    // Every byte, two hex digits each
	Bits   []int       `json:"bits"`   // The bitmap's set bits, in order: the fields present
	Fields []WireField `json:"fields"` // In byte order: MTI, bitmaps, then each field
}

// WireField is one part of the message and where its bytes are.
type WireField struct {
	ID     string     `json:"id"`            // "MTI", "BITMAP", "BITMAP2", or "DE2"
	Bit    int        `json:"bit,omitempty"` // The bitmap bit for a data element
	Name   string     `json:"name"`
	Format string     `json:"format"` // ISO 8583 notation, e.g. "n 6" or "LLVAR n..19"
	Offset int        `json:"offset"`
	Length int        `json:"length"`
	Masked bool       `json:"masked,omitempty"` // Some bytes are '*' standing in for hidden card data
	Parts  []WirePart `json:"parts,omitempty"`
}

// WirePart is a piece of a field: its length prefix, a subelement, or the value.
type WirePart struct {
	Label  string `json:"label"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
}

// fieldSpec is how one data element is encoded.
type fieldSpec struct {
	name   string
	kind   string // n numeric, an/ans text, z track data, b binary
	max    int    // Fixed length, or the most a variable field may hold
	prefix int    // Digits in the length prefix: 0 fixed, 2 LLVAR, 3 LLLVAR
}

func (s fieldSpec) format() string {
	switch {
	case s.prefix == 0 && s.kind == "b":
		return fmt.Sprintf("b %d", s.max*8)
	case s.prefix == 0:
		return fmt.Sprintf("%s %d", s.kind, s.max)
	default:
		return fmt.Sprintf("%sVAR %s..%d", strings.Repeat("L", s.prefix), s.kind, s.max)
	}
}

// specs are the ISO 8583:1987 formats of the fields an 0100 can carry.
var specs = map[int]fieldSpec{
	2:   {"Primary account number", "n", 19, 2},
	3:   {"Processing code", "n", 6, 0},
	4:   {"Amount, transaction", "n", 12, 0},
	7:   {"Transmission date and time", "n", 10, 0},
	11:  {"System trace audit number", "n", 6, 0},
	12:  {"Local transaction time", "n", 6, 0},
	13:  {"Local transaction date", "n", 4, 0},
	14:  {"Expiration date", "n", 4, 0},
	18:  {"Merchant category code", "n", 4, 0},
	22:  {"POS entry mode", "n", 3, 0},
	32:  {"Acquiring institution ID", "n", 11, 2},
	35:  {"Track 2 data", "z", 37, 2},
	37:  {"Retrieval reference number", "an", 12, 0},
	41:  {"Card acceptor terminal ID", "ans", 8, 0},
	42:  {"Card acceptor ID", "ans", 15, 0},
	43:  {"Card acceptor name and location", "ans", 40, 0},
	48:  {"Additional data, private use", "ans", 999, 3},
	49:  {"Currency code, transaction", "n", 3, 0},
	52:  {"PIN data", "b", 8, 0},
	54:  {"Additional amounts", "an", 120, 3},
	55:  {"ICC (chip) data", "b", 255, 3},
	62:  {"Custom payment service data", "ans", 255, 3},
	123: {"Address verification data", "ans", 999, 3},
}

// DE48 subelement tags. Real networks each define their own; these are
// simple-network's.
const (
	tagCOF          = "10"
	tagOdometer     = "20"
	tagVehicle      = "21"
	tagDriver       = "22"
	tagECI          = "30"
	tagCAVV         = "31"
	tagName         = "40"
	tagWallet       = "41"
	tagWalletCrypto = "43"
	tagAuthType     = "50"
)

// value is a field's content before its length prefix, and how it splits.
type value struct {
	data   []byte
	parts  []WirePart // Offsets relative to data
	masked bool
}

// add appends a labelled piece to a value.
func (v *value) add(label string, b []byte) {
	v.parts = append(v.parts, WirePart{Label: label, Offset: len(v.data), Length: len(b)})
	v.data = append(v.data, b...)
}

// EncodeAuthRequest encodes r as ISO 8583 bytes. Pass a redacted request:
// a masked PAN encodes with '*' digits and a hidden PIN block as eight '*'
// bytes, both flagged Masked. The CVV2 is never kept, so it isn't encoded.
func EncodeAuthRequest(r AuthRequest) (Wire, error) {
	if len(r.MTI) != 4 {
		return Wire{}, fmt.Errorf("MTI %q is not 4 digits", r.MTI)
	}
	vals := map[int]value{}
	text := func(bit int, s string) {
		if s != "" {
			vals[bit] = value{data: []byte(s), masked: strings.Contains(s, "*")}
		}
	}
	text(2, r.PAN)
	text(3, r.ProcessingCode)
	text(4, strconv.FormatInt(r.Amount, 10))
	text(7, r.TransmissionTime)
	text(11, r.STAN)
	text(12, r.LocalTime)
	text(13, r.LocalDate)
	text(14, r.Expiry)
	text(18, r.MCC)
	text(22, r.EntryMode)
	text(32, r.AcquirerID)
	text(35, r.Track2)
	text(37, r.RRN)
	text(41, r.TerminalID)
	text(42, r.MerchantID)
	text(43, r.MerchantNameLoc)
	text(49, r.Currency)
	text(62, r.OriginalTxnID)
	if r.PINData != "" {
		// The record keeps no PIN block, only that there was one.
		vals[52] = value{data: []byte(strings.Repeat("*", 8)), masked: true}
	}

	// A card's ARQC goes in DE55 as EMV TLV: tag 9F26, length, cryptogram.
	// An in-app wallet's cryptogram isn't EMV data; it rides in DE48.
	walletCrypto := ""
	if b, err := hex.DecodeString(r.ARQC); err == nil && len(b) == 8 {
		var v value
		v.add("Tag 9F26 (application cryptogram)", []byte{0x9F, 0x26})
		v.add("Length 08", []byte{0x08})
		v.add("ARQC", b)
		vals[55] = v
	} else {
		walletCrypto = r.ARQC
	}

	var de48 value
	for _, se := range []struct{ tag, label, val string }{
		{tagCOF, "card-on-file indicator", r.COFIndicator},
		{tagOdometer, "odometer", fleet(r.Fleet, func(f *FleetData) string { return f.Odometer })},
		{tagVehicle, "vehicle ID", fleet(r.Fleet, func(f *FleetData) string { return f.VehicleID })},
		{tagDriver, "driver ID", fleet(r.Fleet, func(f *FleetData) string { return f.DriverID })},
		{tagECI, "e-commerce indicator", r.ECI},
		{tagCAVV, "CAVV", r.CAVV},
		{tagName, "cardholder name", r.CardholderName},
		{tagWallet, "wallet provider", r.WalletProvider},
		{tagWalletCrypto, "wallet cryptogram", walletCrypto},
		{tagAuthType, "authorization type", r.AuthType},
	} {
		if se.val == "" {
			continue
		}
		if len(se.val) > 99 {
			return Wire{}, fmt.Errorf("DE48 subelement %s is %d characters; at most 99 fit", se.tag, len(se.val))
		}
		de48.add(fmt.Sprintf("SE%s %s", se.tag, se.label), fmt.Appendf(nil, "%s%02d%s", se.tag, len(se.val), se.val))
	}
	if len(de48.data) > 0 {
		vals[48] = de48
	}

	// DE54: 20 characters per amount: account type, amount type, currency,
	// sign (D debit, C credit), amount.
	if len(r.AdditionalAmounts) > 0 {
		var v value
		for _, a := range r.AdditionalAmounts {
			v.add("Amount type "+a.Type, fmt.Appendf(nil, "00%-2.2s%3.3sD%012d", a.Type, r.Currency, a.Amount))
		}
		vals[54] = v
	}
	if r.AVS != nil && (r.AVS.Zip != "" || r.AVS.Street != "") {
		var v value
		v.add("ZIP code", fmt.Appendf(nil, "%-9.9s", r.AVS.Zip))
		v.add("Street address", []byte(r.AVS.Street))
		vals[123] = v
	}

	bits := make([]int, 0, len(vals)+1)
	for b := range vals {
		bits = append(bits, b)
	}
	sort.Ints(bits)
	secondary := len(bits) > 0 && bits[len(bits)-1] > 64
	if secondary {
		bits = append([]int{1}, bits...)
	}

	var out []byte
	var fields []WireField
	place := func(f WireField, b []byte) {
		f.Offset, f.Length = len(out), len(b)
		fields = append(fields, f)
		out = append(out, b...)
	}
	place(WireField{ID: "MTI", Name: "Message type indicator", Format: "n 4"}, []byte(r.MTI))
	bitmap := make([]byte, 8)
	if secondary {
		bitmap = make([]byte, 16)
	}
	for _, b := range bits {
		bitmap[(b-1)/8] |= 0x80 >> ((b - 1) % 8)
	}
	place(WireField{ID: "BITMAP", Name: "Primary bitmap", Format: "b 64"}, bitmap[:8])
	if secondary {
		place(WireField{ID: "BITMAP2", Bit: 1, Name: "Secondary bitmap", Format: "b 64"}, bitmap[8:])
	}
	for _, b := range bits {
		if b == 1 {
			continue
		}
		spec, v := specs[b], vals[b]
		enc, err := encodeField(spec, v.data)
		if err != nil {
			return Wire{}, fmt.Errorf("DE%d: %w", b, err)
		}
		f := WireField{ID: fmt.Sprintf("DE%d", b), Bit: b, Name: spec.name, Format: spec.format(), Masked: v.masked}
		start := len(out)
		if spec.prefix > 0 {
			f.Parts = append(f.Parts, WirePart{Label: fmt.Sprintf("Length %s", enc[:spec.prefix]), Offset: start, Length: spec.prefix})
			start += spec.prefix
		}
		for _, p := range v.parts {
			p.Offset += start
			f.Parts = append(f.Parts, p)
		}
		place(f, enc)
	}
	return Wire{Hex: hex.EncodeToString(out), Bits: bits, Fields: fields}, nil
}

func fleet(f *FleetData, get func(*FleetData) string) string {
	if f == nil {
		return ""
	}
	return get(f)
}

// encodeField pads a fixed-length field or prefixes a variable one with its
// length, checking it fits.
func encodeField(s fieldSpec, data []byte) ([]byte, error) {
	if len(data) > s.max {
		return nil, fmt.Errorf("%d bytes, at most %d fit (%s)", len(data), s.max, s.format())
	}
	if s.prefix > 0 {
		return append(fmt.Appendf(nil, "%0*d", s.prefix, len(data)), data...), nil
	}
	pad := s.max - len(data)
	switch s.kind {
	case "n":
		return append([]byte(strings.Repeat("0", pad)), data...), nil
	case "b":
		if pad > 0 {
			return nil, fmt.Errorf("%d bytes, want %d", len(data), s.max)
		}
		return data, nil
	default:
		return append(data, strings.Repeat(" ", pad)...), nil
	}
}
