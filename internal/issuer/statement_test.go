package issuer

import (
	"encoding/json"
	"testing"

	"github.com/JonahR/simple-network/internal/clearing"
)

func janeCredit(t *testing.T, b *Bank) string {
	t.Helper()
	for _, c := range b.Customers() {
		for _, a := range c.Accounts {
			if c.Holder == "Jane Doe" && a.Product == "credit" {
				return a.ID
			}
		}
	}
	t.Fatal("no Jane Doe credit account")
	return ""
}

func TestCustomersGroupTheirCards(t *testing.T) {
	fsb, ucb, _ := testBanks(t)
	cs := fsb.Customers()
	if len(cs) != 1 || cs[0].Holder != "Jane Doe" || len(cs[0].Accounts) != 3 {
		t.Errorf("FSB customers = %+v; want Jane Doe with three cards", cs)
	}
	if cs[0].Accounts[0].PAN != "424242******4242" {
		t.Errorf("card number %q must be masked", cs[0].Accounts[0].PAN)
	}
	if n := len(ucb.Customers()); n != 3 {
		t.Errorf("UCB has %d customers, want 3", n)
	}
}

func TestStatementPendingThenPosted(t *testing.T) {
	fsb, _, _ := testBanks(t)
	id := janeCredit(t, fsb)

	sale := req("4242424242424242", "3312", 40_00)
	sale.MerchantNameLoc = "Simple Coffee Co         San Francisco US"
	fsb.Authorize(sale)
	refund := req("4242424242424242", "3312", 15_00)
	refund.ProcessingCode = "200000"
	fsb.Authorize(refund)

	st, ok := fsb.Statement(id)
	if !ok {
		t.Fatal("no statement")
	}
	if len(st.Pending) != 2 || len(st.Posted) != 0 {
		t.Fatalf("pending %d, posted %d; want 2 pending", len(st.Pending), len(st.Posted))
	}
	var hold, credit Activity
	for _, a := range st.Pending {
		if a.Kind == "refund" {
			credit = a
		} else {
			hold = a
		}
	}
	if hold.Amount != 40_00 || hold.Merchant != "Simple Coffee Co" || hold.AuthCode == "" {
		t.Errorf("pending sale %+v", hold)
	}
	if credit.Amount != -15_00 {
		t.Errorf("pending refund amount %d, want −1500", credit.Amount)
	}
	if st.PendingNet != 25_00 || st.PendingRefs != 15_00 || st.Balance != 0 {
		t.Errorf("pending net %d, refunds %d, balance %d", st.PendingNet, st.PendingRefs, st.Balance)
	}

	// Clearing posts the sale for its final amount (a tip added) and the refund.
	fsb.Post(clearing.IssuerFile{FileID: "c1-FSB", Records: []clearing.IssuerRecord{
		{NetworkTxnID: sale.NetworkTxnID, ARN: "A1", Amount: 46_00, MerchantName: "Simple Coffee Co", Currency: "840"},
		{NetworkTxnID: refund.NetworkTxnID, ARN: "A2", Amount: 15_00, MerchantName: "Simple Coffee Co", Currency: "840", Credit: true},
	}})
	st, _ = fsb.Statement(id)
	if len(st.Pending) != 0 || len(st.Posted) != 2 {
		t.Fatalf("after clearing: pending %d, posted %d", len(st.Pending), len(st.Posted))
	}
	amounts := map[string]int64{}
	for _, p := range st.Posted {
		amounts[p.Kind] = p.Amount
		if p.Posted == nil || p.Time.IsZero() {
			t.Errorf("posted line %+v needs both dates", p)
		}
	}
	if amounts["purchase"] != 46_00 || amounts["refund"] != -15_00 {
		t.Errorf("posted amounts %v; want the final 46.00 and the −15.00 refund", amounts)
	}
	if st.Balance != 31_00 || st.Account.Available != 500_000-31_00 {
		t.Errorf("balance %d, available %d; want 31.00 owed", st.Balance, st.Account.Available)
	}
}

func TestStatementIncludesAddedAuthorizations(t *testing.T) {
	fsb, _, _ := testBanks(t)
	pre := req("4242424242424242", "3312", 100_00)
	pre.AuthType = "estimated"
	fsb.Authorize(pre)
	inc := req("4242424242424242", "3312", 20_00)
	inc.AuthType, inc.OriginalTxnID = "incremental", pre.NetworkTxnID
	if resp := fsb.Authorize(inc); resp.ResponseCode != "00" {
		t.Fatalf("increment: %+v", resp)
	}
	st, _ := fsb.Statement(janeCredit(t, fsb))
	if len(st.Pending) != 1 || st.Pending[0].Amount != 120_00 || st.Pending[0].Note == "" {
		t.Errorf("pending %+v; want one 120.00 hold noting the increment", st.Pending)
	}
}

func TestPostingsSurviveARestart(t *testing.T) {
	fsb, _, key := testBanks(t)
	sale := req("4242424242424242", "3312", 12_00)
	fsb.Authorize(sale)
	fsb.Post(clearing.IssuerFile{FileID: "c1-FSB", Records: []clearing.IssuerRecord{{NetworkTxnID: sale.NetworkTxnID, Amount: 12_00}}})

	b, _ := json.Marshal(fsb.SaveState())
	var st BankState
	json.Unmarshal(b, &st)
	fa, fe := FirstSimpleBankAccounts()
	again := NewBank("FSB", "First Simple Bank", key, fa, fe)
	again.RestoreState(st)
	s, _ := again.Statement(janeCredit(t, again))
	if len(s.Posted) != 1 || s.Posted[0].Amount != 12_00 {
		t.Errorf("posted after restore: %+v", s.Posted)
	}
}

func TestStatementUnknownAccount(t *testing.T) {
	fsb, _, _ := testBanks(t)
	if _, ok := fsb.Statement("nope"); ok {
		t.Error("want no statement for an unknown account")
	}
}

// A chargeback's provisional credit shows as its own line and lowers the
// balance; if the merchant wins, the credit is taken back.
func TestStatementShowsDisputeCredits(t *testing.T) {
	fsb, _, _ := testBanks(t)
	id := janeCredit(t, fsb)
	sale := req("4242424242424242", "3312", 60_00)
	fsb.Authorize(sale)
	fsb.Post(clearing.IssuerFile{FileID: "c1-FSB", Records: []clearing.IssuerRecord{{NetworkTxnID: sale.NetworkTxnID, Amount: 60_00}}})

	if err := fsb.ApplyDispute(DisputeNotice{DisputeID: "D1", NetworkTxnID: sale.NetworkTxnID, Amount: 60_00, Credit: true, Summary: "open: not received"}); err != nil {
		t.Fatal(err)
	}
	st, _ := fsb.Statement(id)
	lines := map[string]Activity{}
	for _, l := range st.Posted { // Newest first: the credit sits above the purchase
		lines[l.Kind] = l
	}
	if len(st.Posted) != 2 || lines["dispute"].Amount != -60_00 {
		t.Fatalf("posted %+v; want the purchase and a −60.00 provisional credit", st.Posted)
	}
	if lines["purchase"].Note == "" || st.Balance != 0 || st.Account.Available != 500_000 {
		t.Errorf("note %q, balance %d, available %d; want the credit to cancel the charge", lines["purchase"].Note, st.Balance, st.Account.Available)
	}

	fsb.ApplyDispute(DisputeNotice{DisputeID: "D1", NetworkTxnID: sale.NetworkTxnID, Amount: 60_00, Credit: false, Summary: "lost: merchant proved delivery"})
	st, _ = fsb.Statement(id)
	if len(st.Posted) != 1 || st.Balance != 60_00 || st.Account.Available != 500_000-60_00 {
		t.Errorf("after losing: posted %d lines, balance %d, available %d", len(st.Posted), st.Balance, st.Account.Available)
	}
}

// A posted purchase can be disputed once; a refund or a pending hold can't.
func TestDisputableCharge(t *testing.T) {
	fsb, _, _ := testBanks(t)
	id := janeCredit(t, fsb)
	sale := req("4242424242424242", "3312", 40_00)
	fsb.Authorize(sale)
	if _, ok := fsb.DisputableCharge(id, sale.NetworkTxnID); ok {
		t.Error("a pending hold is disputable")
	}
	refund := req("4242424242424242", "3312", 15_00)
	refund.ProcessingCode = "200000"
	fsb.Authorize(refund)
	fsb.Post(clearing.IssuerFile{FileID: "c1-FSB", Records: []clearing.IssuerRecord{
		{NetworkTxnID: sale.NetworkTxnID, ARN: "A1", Amount: 40_00, MerchantName: "Shop", Currency: "840"},
		{NetworkTxnID: refund.NetworkTxnID, ARN: "A2", Amount: 15_00, MerchantName: "Shop", Currency: "840", Credit: true},
	}})

	if p, ok := fsb.DisputableCharge(id, sale.NetworkTxnID); !ok || p.Amount != 40_00 {
		t.Errorf("posted sale: %+v %v, want disputable for 4000", p, ok)
	}
	if _, ok := fsb.DisputableCharge(id, refund.NetworkTxnID); ok {
		t.Error("a refund is disputable")
	}
	if _, ok := fsb.DisputableCharge("someone-else", sale.NetworkTxnID); ok {
		t.Error("another account can dispute the sale")
	}
	st, _ := fsb.Statement(id)
	for _, l := range st.Posted {
		if want := l.Kind == "purchase"; l.Disputable != want {
			t.Errorf("%s line disputable = %v", l.Kind, l.Disputable)
		}
	}

	if err := fsb.ApplyDispute(DisputeNotice{DisputeID: "D1", NetworkTxnID: sale.NetworkTxnID, State: "chargeback", Amount: 40_00, Credit: true, Summary: "First chargeback"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := fsb.DisputableCharge(id, sale.NetworkTxnID); ok {
		t.Error("a disputed sale is disputable again")
	}
	st, _ = fsb.Statement(id)
	for _, l := range st.Posted {
		if l.Disputable {
			t.Errorf("%s line still disputable after the dispute", l.Kind)
		}
	}
}
