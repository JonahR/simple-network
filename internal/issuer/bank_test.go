package issuer

import (
	"encoding/hex"
	"fmt"
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
	r := req("4242424242424242", "2912", 12_50)
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
	r := req("4242424242424242", "2912", 1000)
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
		{"unknown card", fsb, func() iso8583.AuthRequest { return req("4242424242424241", "2912", 100) }, "14"},
		{"wrong expiry", fsb, func() iso8583.AuthRequest { return req("4242424242424242", "3001", 100) }, "54"},
		{"wrong CVV", fsb, func() iso8583.AuthRequest {
			r := req("4242424242424242", "2912", 100)
			r.CVV2 = "999"
			return r
		}, "N7"},
		{"correct PIN", fsb, func() iso8583.AuthRequest {
			r := req("4000056655665556", "2905", 100)
			r.PINData = goodPIN
			return r
		}, "00"},
		{"wrong PIN", fsb, func() iso8583.AuthRequest {
			r := req("4000056655665556", "2905", 100)
			r.PINData = badPIN
			return r
		}, "55"},
		{"over credit limit", ucb, func() iso8583.AuthRequest { return req("5555555555554444", "2808", 30_001) }, "51"},
		{"cash back on credit", ucb, func() iso8583.AuthRequest {
			r := req("5555555555554444", "2808", 2000)
			r.AdditionalAmounts = []iso8583.AdditionalAmount{{Type: "40", Amount: 1000}}
			return r
		}, "57"},
		{"HSA without eligible amount", fsb, func() iso8583.AuthRequest { return req("4716006861111015", "2811", 100) }, "57"},
		{"auto-enrolled test card", fsb, func() iso8583.AuthRequest { return req("4000001234567899", "3010", 100) }, "00"},
		{"auto-enrolled pay later", ucb, func() iso8583.AuthRequest { return req("4859321234567890", "2710", 100) }, "00"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.bank.Authorize(tt.req()); got.ResponseCode != tt.want {
				t.Errorf("code = %s (%s), want %s", got.ResponseCode, got.ResponseText, tt.want)
			}
		})
	}
}

func TestPartialApprovals(t *testing.T) {
	fsb, ucb, _ := testBanks(t)

	// Prepaid: $100 balance, $120 purchase → approve the $100 left.
	resp := ucb.Authorize(req("4358805984634941", "2807", 12_000))
	if resp.ResponseCode != "10" || resp.Amount != 10_000 {
		t.Fatalf("prepaid partial: %+v", resp)
	}
	if resp := ucb.Authorize(req("4358805984634941", "2807", 100)); resp.ResponseCode != "51" {
		t.Errorf("empty prepaid card: %s", resp.ResponseCode)
	}

	// HSA: only the eligible $30 of a $50 purchase is approved.
	r := req("4716006861111015", "2811", 5000)
	r.AdditionalAmounts = []iso8583.AdditionalAmount{{Type: "4S", Amount: 3000}}
	if resp := fsb.Authorize(r); resp.ResponseCode != "10" || resp.Amount != 3000 {
		t.Errorf("HSA partial: %+v", resp)
	}
	r = req("4716006861111015", "2811", 3000)
	r.AdditionalAmounts = []iso8583.AdditionalAmount{{Type: "4S", Amount: 3000}}
	if resp := fsb.Authorize(r); resp.ResponseCode != "00" || resp.Amount != 3000 {
		t.Errorf("HSA full: %+v", resp)
	}
}
