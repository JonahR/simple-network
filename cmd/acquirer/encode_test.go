package main

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JonahR/simple-network/internal/card"
	"github.com/JonahR/simple-network/internal/iso8583"
)

func TestHandleEncodeShowsMaskedBytes(t *testing.T) {
	body, _ := json.Marshal(request().Redacted(card.Mask))
	w := httptest.NewRecorder()
	handleEncode(w, httptest.NewRequest(http.MethodPost, "/api/iso8583", strings.NewReader(string(body))))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var wire iso8583.Wire
	if err := json.Unmarshal(w.Body.Bytes(), &wire); err != nil {
		t.Fatal(err)
	}
	b, _ := hex.DecodeString(wire.Hex)
	if !strings.HasPrefix(string(b), "0100") || strings.Contains(string(b), "4242424242424242") || strings.Contains(string(b), "0123456789ABCDEF") {
		t.Errorf("bytes %q: want an 0100 with no card number or PIN block", b)
	}
}

func TestHandleEncodeRejectsBadMessages(t *testing.T) {
	for _, body := range []string{"not json", `{"mti":"01"}`} {
		w := httptest.NewRecorder()
		handleEncode(w, httptest.NewRequest(http.MethodPost, "/api/iso8583", strings.NewReader(body)))
		if w.Code == http.StatusOK {
			t.Errorf("%s: status 200, want an error", body)
		}
	}
}
