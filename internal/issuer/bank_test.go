package issuer

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/JonahR/simple-network/internal/demokeys"
	"github.com/JonahR/simple-network/internal/iso8583"
	"github.com/JonahR/simple-network/internal/pin"
)

func testBanks(t *testing.T) (*Bank, *Bank, []byte) {
	t.Helper()
	key, _ := hex.DecodeString(demokeys.IssuerFSB)
	fa, fe := FirstSimpleBankAccounts()
	ua, ue := UnionCardBankAccounts()
	return NewBank("FSB", "First Simple Bank", key, fa, fe), NewBank("UCB", "Union Card Bank", key, ua, ue), key
}

var txnSeq int

func req(pan, expiry string, amount int64) iso8583.AuthRequest {
	txnSeq++
	return iso8583.AuthRequest{
		MTI: "0100", PAN: pan, Expiry: expiry, Amount: amount, ProcessingCode: "000000",
		STAN: "000001", RRN: "628214000001", Currency: "840",
		NetworkTxnID: fmt.Sprintf("txn-%d", txnSeq),
	}
}

func TestAuthorizeAndHold(t *testing.T) {
	fsb, _, _ := testBanks(t)
	r := req("4242424242424242", "3312", 12_50)
	r.CVV2 = "123"
	resp := fsb.Authorize(r)
	if resp.ResponseCode != "00" || resp.Amount != 1250 || len(resp.AuthCode) != 6 || resp.MTI != "0110" {
		t.Fatalf("%+v", resp)
	}
	a, _ := fsb.Snapshot("4242424242424242")
	if a.Available != 500_000-1250 {
		t.Errorf("available = %d", a.Available)
	}

	// Reversal releases the hold, once.
	if rr := fsb.Reverse(iso8583.ReversalAdvice{NetworkTxnID: r.NetworkTxnID}); !rr.Matched {
		t.Error("reversal not matched")
	}
	if a, _ := fsb.Snapshot("4242424242424242"); a.Available != 500_000 {
		t.Errorf("available after reversal = %d", a.Available)
	}
	if rr := fsb.Reverse(iso8583.ReversalAdvice{NetworkTxnID: r.NetworkTxnID}); rr.Matched {
		t.Error("second reversal matched")
	}
}

func TestReversalBeforeAuthorization(t *testing.T) {
	fsb, _, _ := testBanks(t)
	r := req("4242424242424242", "3312", 1000)
	fsb.Reverse(iso8583.ReversalAdvice{NetworkTxnID: r.NetworkTxnID})
	if resp := fsb.Authorize(r); resp.ResponseCode == "00" {
		t.Fatal("late authorization approved after its reversal")
	}
	if a, _ := fsb.Snapshot("4242424242424242"); a.Available != 500_000 {
		t.Errorf("hold placed: available = %d", a.Available)
	}
}

func TestDeclines(t *testing.T) {
	fsb, ucb, key := testBanks(t)
	goodPIN, _ := pin.Encrypt("1234", "4000056655665556", key)
	badPIN, _ := pin.Encrypt("9999", "4000056655665556", key)

	tests := []struct {
		name string
		bank *Bank
		req  func() iso8583.AuthRequest
		want string
	}{
		{"unknown card", fsb, func() iso8583.AuthRequest { return req("4242424242424241", "3312", 100) }, "14"},
		{"wrong expiry", fsb, func() iso8583.AuthRequest { return req("4242424242424242", "3001", 100) }, "54"},
		{"wrong CVV", fsb, func() iso8583.AuthRequest {
			r := req("4242424242424242", "3312", 100)
			r.CVV2 = "999"
			return r
		}, "N7"},
		{"correct PIN", fsb, func() iso8583.AuthRequest {
			r := req("4000056655665556", "3305", 100)
			r.PINData = goodPIN
			return r
		}, "00"},
		{"wrong PIN", fsb, func() iso8583.AuthRequest {
			r := req("4000056655665556", "3305", 100)
			r.PINData = badPIN
			return r
		}, "55"},
		{"over credit limit", ucb, func() iso8583.AuthRequest { return req("5555555555554444", "3208", 30_001) }, "51"},
		{"cash back on credit", ucb, func() iso8583.AuthRequest {
			r := req("5555555555554444", "3208", 2000)
			r.AdditionalAmounts = []iso8583.AdditionalAmount{{Type: "40", Amount: 1000}}
			return r
		}, "57"},
		{"HSA without eligible amount", fsb, func() iso8583.AuthRequest { return req("4716006861111015", "3211", 100) }, "57"},
		{"auto-enrolled test card", fsb, func() iso8583.AuthRequest { return req("4000001234567899", "3010", 100) }, "00"},
		{"auto-enrolled pay later", ucb, func() iso8583.AuthRequest { return req("4859321234567890", "2710", 100) }, "00"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := tt.req()
			got := tt.bank.Authorize(r)
			if got.ResponseCode != tt.want {
				t.Errorf("code = %s (%s), want %s", got.ResponseCode, got.ResponseText, tt.want)
			}
			// DE4 echoes the requested amount on approvals and declines alike.
			if got.Amount != r.Amount {
				t.Errorf("DE4 = %d, want %d", got.Amount, r.Amount)
			}
		})
	}
}

func TestPartialApprovals(t *testing.T) {
	fsb, ucb, _ := testBanks(t)

	// Prepaid: $100 balance, $120 purchase → approve the $100 left.
	resp := ucb.Authorize(req("4358805984634941", "3207", 12_000))
	if resp.ResponseCode != "10" || resp.Amount != 10_000 {
		t.Fatalf("prepaid partial: %+v", resp)
	}
	if resp := ucb.Authorize(req("4358805984634941", "3207", 100)); resp.ResponseCode != "51" {
		t.Errorf("empty prepaid card: %s", resp.ResponseCode)
	}

	// HSA: only the eligible $30 of a $50 purchase is approved.
	r := req("4716006861111015", "3211", 5000)
	r.AdditionalAmounts = []iso8583.AdditionalAmount{{Type: "4S", Amount: 3000}}
	if resp := fsb.Authorize(r); resp.ResponseCode != "10" || resp.Amount != 3000 {
		t.Errorf("HSA partial: %+v", resp)
	}
	r = req("4716006861111015", "3211", 3000)
	r.AdditionalAmounts = []iso8583.AdditionalAmount{{Type: "4S", Amount: 3000}}
	if resp := fsb.Authorize(r); resp.ResponseCode != "00" || resp.Amount != 3000 {
		t.Errorf("HSA full: %+v", resp)
	}
}

func checkNames(d Decision) string {
	var parts []string
	for _, c := range d.Checks {
		parts = append(parts, c.Name+":"+c.Result)
	}
	return strings.Join(parts, " ")
}

func TestDecisionLog(t *testing.T) {
	fsb, _, key := testBanks(t)

	// An approval with a PIN runs every check and records the hold.
	r := req("4000056655665556", "3305", 2000)
	r.PINData, _ = pin.Encrypt("1234", r.PAN, key)
	r.AdditionalAmounts = []iso8583.AdditionalAmount{{Type: "40", Amount: 500}}
	r.MerchantNameLoc = "Simple Coffee Co         San Francisco US"
	fsb.Authorize(r)

	// A decline stops at the failing check.
	bad := req("4242424242424242", "3312", 100)
	bad.CVV2 = "000"
	fsb.Authorize(bad)

	st := fsb.State()
	if len(st.Decisions) != 2 {
		t.Fatalf("decisions = %d", len(st.Decisions))
	}
	declined, approved := st.Decisions[0], st.Decisions[1] // Newest first
	if got := checkNames(approved); got != "Account:pass Card status:pass Expiry:pass CVV2:skip PIN:pass Product rules:pass Funds:pass" {
		t.Errorf("approved checks: %s", got)
	}
	if approved.AvailableBefore != 250_000 || approved.AvailableAfter != 248_000 || approved.Hold != "active" ||
		approved.Merchant != "Simple Coffee Co" || approved.PAN != "400005******5556" || approved.AccountID != "fsb-002" {
		t.Errorf("approved decision: %+v", approved)
	}
	if got := checkNames(declined); got != "Account:pass Card status:pass Expiry:pass CVV2:fail" {
		t.Errorf("declined checks: %s", got)
	}
	if declined.Hold != "none" || declined.AvailableAfter != declined.AvailableBefore {
		t.Errorf("declined decision: %+v", declined)
	}

	// The account view shows the hold, and a reversal releases it.
	acct := st.Accounts[1]
	if acct.Held != 2000 || acct.Holds != 1 || acct.Available != 248_000 || !acct.HasPIN || acct.PAN != "400005******5556" {
		t.Errorf("account view: %+v", acct)
	}
	fsb.Reverse(iso8583.ReversalAdvice{NetworkTxnID: r.NetworkTxnID, ResponseCode: iso8583.RCLateResponse})
	st = fsb.State()
	if st.Decisions[1].Hold != "released" || st.Accounts[1].Held != 0 || len(st.Reversals) != 1 || st.Reversals[0].Released != 2000 {
		t.Errorf("after reversal: decision hold %q, held %d, reversals %+v", st.Decisions[1].Hold, st.Accounts[1].Held, st.Reversals)
	}
}

func TestFrozenCardDeclines(t *testing.T) {
	fsb, _, _ := testBanks(t)
	if !fsb.SetBlocked("fsb-001", true) {
		t.Fatal("account fsb-001 not found")
	}
	if resp := fsb.Authorize(req("4242424242424242", "3312", 100)); resp.ResponseCode != "62" {
		t.Errorf("frozen card: %s", resp.ResponseCode)
	}
	fsb.SetBlocked("fsb-001", false)
	if resp := fsb.Authorize(req("4242424242424242", "3312", 100)); resp.ResponseCode != "00" {
		t.Errorf("unfrozen card: %s", resp.ResponseCode)
	}
}

func TestAutoEnrolledAccountsAppearInState(t *testing.T) {
	_, ucb, _ := testBanks(t)
	ucb.Authorize(req("4859321234567890", "2710", 100))
	st := ucb.State()
	last := st.Accounts[len(st.Accounts)-1]
	if last.Holder != "Pay Later customer" || last.ID != "ucb-004" || last.Expiry != "10/27" {
		t.Errorf("%+v", last)
	}
	if !strings.Contains(st.Decisions[0].Checks[0].Detail, "opened on first use") {
		t.Errorf("account check: %+v", st.Decisions[0].Checks[0])
	}
}

func TestEmptyStateEncodesArrays(t *testing.T) {
	fsb, _, _ := testBanks(t)
	body, err := json.Marshal(fsb.State())
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"decisions":[]`, `"reversals":[]`} {
		if !strings.Contains(string(body), field) {
			t.Errorf("empty state should encode %s, got %s", field, body)
		}
	}
}
