package issuer

import (
	"testing"

	"github.com/JonahR/simple-network/internal/iso8583"
)

func TestBalanceInquiryReportsWithoutHolding(t *testing.T) {
	fsb, _, _ := testBanks(t)
	r := req("4242424242424242", "3312", 0)
	r.ProcessingCode = "303000"
	resp := fsb.Authorize(r)
	if resp.ResponseCode != "00" || resp.Amount != 0 || resp.AuthCode != "" {
		t.Fatalf("0110 = %+v", resp)
	}
	if len(resp.AdditionalAmounts) != 1 || resp.AdditionalAmounts[0].Type != iso8583.AmountAvailable || resp.AdditionalAmounts[0].Amount != 500_000 {
		t.Errorf("DE54 = %+v, want type 02 with 500000", resp.AdditionalAmounts)
	}
	if a, _ := fsb.Snapshot("4242424242424242"); a.Available != 500_000 {
		t.Errorf("an inquiry changed the balance to %d", a.Available)
	}
	if d := fsb.State().Decisions; d[0].Hold != "none" { // Newest first
		t.Errorf("decision hold = %q", d[0].Hold)
	}

	// The card checks still apply: a wrong expiry is declined.
	bad := req("4242424242424242", "2001", 0)
	bad.ProcessingCode = "303000"
	if resp := fsb.Authorize(bad); resp.ResponseCode == "00" || len(resp.AdditionalAmounts) != 0 {
		t.Errorf("inquiry with a wrong expiry: %+v", resp)
	}
}
