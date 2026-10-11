package issuer

import (
	"testing"

	"github.com/JonahR/simple-network/internal/clearing"
)

func TestRefundIsApprovedWithoutFundsOrHold(t *testing.T) {
	fsb, _, _ := testBanks(t)
	// Use up the whole credit line, so a charge would be declined.
	sale := req("4242424242424242", "3312", 500_000)
	if resp := fsb.Authorize(sale); resp.ResponseCode != "00" {
		t.Fatalf("sale: %+v", resp)
	}
	refund := req("4242424242424242", "3312", 12_00)
	refund.ProcessingCode = "200000"
	resp := fsb.Authorize(refund)
	if resp.ResponseCode != "00" || resp.Amount != 12_00 || len(resp.AuthCode) != 6 {
		t.Fatalf("refund with no available credit: %+v", resp)
	}
	acct, _ := fsb.Snapshot("4242424242424242")
	if acct.Available != 0 {
		t.Errorf("available = %d; a refund must not change it before it clears", acct.Available)
	}
	d := fsb.State().Decisions
	if last := d[0]; last.Hold != "credit pending" || last.Approved != 12_00 {
		t.Errorf("refund decision hold %q approved %d", last.Hold, last.Approved)
	}
}

func TestRefundStillChecksTheCard(t *testing.T) {
	fsb, _, _ := testBanks(t)
	refund := req("4242424242424242", "2001", 1_00) // Wrong expiry
	refund.ProcessingCode = "200000"
	if resp := fsb.Authorize(refund); resp.ResponseCode != "54" {
		t.Errorf("refund to a card with the wrong expiry: %s, want 54", resp.ResponseCode)
	}
}

func TestClearedRefundCreditsTheAccountOnce(t *testing.T) {
	fsb, _, _ := testBanks(t)
	sale := req("4242424242424242", "3312", 80_00)
	fsb.Authorize(sale)
	refund := req("4242424242424242", "3312", 30_00)
	refund.ProcessingCode = "200000"
	fsb.Authorize(refund)

	file := clearing.IssuerFile{FileID: "c1-FSB", Records: []clearing.IssuerRecord{
		{NetworkTxnID: sale.NetworkTxnID, Amount: 80_00},
		{NetworkTxnID: refund.NetworkTxnID, Amount: 30_00, Credit: true, OriginalTxnID: sale.NetworkTxnID},
	}}
	ack := fsb.Post(file)
	if ack.Posted != 2 || len(ack.Unmatched) != 0 {
		t.Fatalf("ack %+v", ack)
	}
	acct, _ := fsb.Snapshot("4242424242424242")
	if acct.Posted != 50_00 || acct.Available != 500_000-50_00 {
		t.Errorf("posted %d, available %d; want the 30.00 refund to offset the 80.00 sale", acct.Posted, acct.Available)
	}
	d := fsb.State().Decisions
	if d[0].Hold != "credited" { // Newest first
		t.Errorf("refund decision hold %q, want credited", d[0].Hold)
	}
	// The sale's decision shows the credit, for a dispute on that sale.
	if d[0].OriginalTxnID != sale.NetworkTxnID || d[1].Refunded != 30_00 {
		t.Errorf("refund names %q; sale shows %d refunded, want 3000", d[0].OriginalTxnID, d[1].Refunded)
	}
	fsb.Post(file) // A retried file changes nothing
	if again, _ := fsb.Snapshot("4242424242424242"); again.Posted != 50_00 || fsb.State().Decisions[1].Refunded != 30_00 {
		t.Errorf("refund credited twice: posted %d", again.Posted)
	}
}
