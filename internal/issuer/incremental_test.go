package issuer

import (
	"testing"

	"github.com/JonahR/simple-network/internal/clearing"
	"github.com/JonahR/simple-network/internal/iso8583"
)

func TestIncrementGrowsTheHoldAndClearsOnce(t *testing.T) {
	fsb, _, _ := testBanks(t)
	pan := "4242424242424242"
	start := fsb.accounts[pan].Available

	first := req(pan, "3312", 10000)
	first.AuthType = iso8583.AuthEstimated
	if r := fsb.Authorize(first); r.ResponseCode != "00" {
		t.Fatalf("estimate: %+v", r)
	}
	more := req(pan, "3312", 3000)
	more.AuthType, more.OriginalTxnID = iso8583.AuthIncremental, first.NetworkTxnID
	if r := fsb.Authorize(more); r.ResponseCode != "00" {
		t.Fatalf("increment: %+v", r)
	}
	if h := fsb.holds[first.NetworkTxnID]; h.amount != 13000 {
		t.Fatalf("hold %d, want 13000", h.amount)
	}

	// Voiding the increment shrinks the hold back.
	fsb.Reverse(iso8583.ReversalAdvice{MTI: "0420", NetworkTxnID: more.NetworkTxnID})
	if h := fsb.holds[first.NetworkTxnID]; h.amount != 10000 || fsb.accounts[pan].Available != start-10000 {
		t.Fatalf("after reversing the increment: hold %d, available %d", h.amount, fsb.accounts[pan].Available)
	}

	// Clearing the original for a final amount releases the whole hold.
	fsb.Post(clearing.IssuerFile{FileID: "f1", Records: []clearing.IssuerRecord{{NetworkTxnID: first.NetworkTxnID, Amount: 11000}}})
	if got := fsb.accounts[pan].Available; got != start-11000 {
		t.Errorf("available %d, want %d", got, start-11000)
	}

	// Nothing left to add to.
	late := req(pan, "3312", 100)
	late.AuthType, late.OriginalTxnID = iso8583.AuthIncremental, first.NetworkTxnID
	if r := fsb.Authorize(late); r.ResponseCode != iso8583.RCInvalidTransaction {
		t.Errorf("increment after clearing: %+v", r)
	}
}
