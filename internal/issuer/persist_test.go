package issuer

import (
	"encoding/json"
	"strings"
	"testing"
)

// The saved state never holds a CVV or PIN hash; a restored bank takes them
// from its seed, so CVV2 checks still work after a restart.
func TestSaveStateOmitsCVVAndPINHash(t *testing.T) {
	fsb, _, _ := testBanks(t)
	data, err := json.Marshal(fsb.SaveState())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{`"CVV"`, `"PINHash"`, HashPIN("4000056655665556", "1234")} {
		if strings.Contains(string(data), s) {
			t.Errorf("saved state contains %s", s)
		}
	}
	var st BankState
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatal(err)
	}
	again, _, _ := testBanks(t)
	again.RestoreState(st)
	ok := req("4242424242424242", "3312", 100)
	ok.CVV2 = "123"
	if resp := again.Authorize(ok); resp.ResponseCode != "00" {
		t.Errorf("right CVV2 after restore: %s", resp.ResponseCode)
	}
	bad := req("4242424242424242", "3312", 100)
	bad.CVV2 = "000"
	if resp := again.Authorize(bad); resp.ResponseCode == "00" {
		t.Error("wrong CVV2 approved after restore")
	}
}

// A dispute notice can't credit more than the sale, or a negative amount.
func TestDisputeCreditIsCappedAtTheSale(t *testing.T) {
	fsb, _, _ := testBanks(t)
	r := req("4242424242424242", "3312", 5000)
	fsb.Authorize(r)
	for _, amt := range []int64{5001, 99_999_999, -1} {
		if err := fsb.ApplyDispute(DisputeNotice{DisputeID: "D1", NetworkTxnID: r.NetworkTxnID, Amount: amt, Credit: true}); err == nil {
			t.Errorf("credit of %d accepted on a 5000 sale", amt)
		}
	}
	if err := fsb.ApplyDispute(DisputeNotice{DisputeID: "D1", NetworkTxnID: r.NetworkTxnID, Amount: 5000, Credit: true}); err != nil {
		t.Errorf("full-amount credit refused: %v", err)
	}
}
