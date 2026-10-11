package network

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/JonahR/simple-network/internal/iso8583"
)

// A remembered authorization is saved with the card masked and without the
// cardholder's name, address or CAVV: only what a later reversal needs.
func TestSavedAttemptHasNoCardData(t *testing.T) {
	f := newFixture(t)
	r := auth("4242424242424242", "3312", 2000)
	r.CardholderName = "JANE DOE"
	r.AVS = &iso8583.AVSData{Zip: "94105", Street: "1 Market St"}
	if resp := f.sw.Authorize(context.Background(), r); resp.ResponseCode != "00" {
		t.Fatalf("not approved: %+v", resp)
	}
	data, _ := json.Marshal(f.sw.SaveState())
	for _, s := range []string{"4242424242424242", "JANE DOE", "1 Market St"} {
		if strings.Contains(string(data), s) {
			t.Errorf("saved switch state contains %q", s)
		}
	}
	if !strings.Contains(string(data), "fwd") {
		t.Error("the forwarded request needed for reversals wasn't saved")
	}
}
