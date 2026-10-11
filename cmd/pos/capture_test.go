package main

import (
	"net/http"
	"testing"

	"github.com/JonahR/simple-network/internal/iso8583"
)

func TestPreAuthIncrementAndCapture(t *testing.T) {
	s, acq := newBankServer(t)
	in := sale(creditPAN, iso8583.EntryManual)
	in.Expiry, in.CVV, in.Amount, in.PreAuth = "12/33", "123", "100.00", true
	_, pre := post(t, s.handleSale, in)
	if pre.Status != "APPROVED" || pre.Request.AuthType != iso8583.AuthEstimated || pre.Authorized != 10000 {
		t.Fatalf("pre-auth: %+v", pre)
	}

	_, inc := post(t, s.handleIncrement, map[string]string{"stan": pre.Request.STAN, "amount": "40.00"})
	if inc.Status != "APPROVED" || inc.IncrementOf != pre.Request.STAN {
		t.Fatalf("increment: %+v", inc)
	}
	got := acq.sent[1]
	if got.AuthType != iso8583.AuthIncremental || got.OriginalTxnID != "NTX-"+pre.Request.STAN || got.CVV2 != "" || got.Amount != 4000 {
		t.Errorf("incremental 0100 = %+v", got)
	}

	// An increment is adjusted through its pre-auth.
	if code, _ := post(t, s.handleCapture, map[string]string{"stan": inc.Request.STAN, "amount": "1.00"}); code != http.StatusConflict {
		t.Errorf("capturing an increment: HTTP %d", code)
	}
	code, done := post(t, s.handleCapture, map[string]string{"stan": pre.Request.STAN, "amount": "132.50"})
	if code != 200 || done.Captured != 13250 || done.Authorized != 14000 {
		t.Fatalf("capture: %d %+v", code, done)
	}
	c := acq.captures[0]
	if c.MTI != "0220" || c.Amount != 13250 || c.OriginalData.STAN != pre.Request.STAN || c.OriginalData.TransmissionTime != pre.Request.TransmissionTime {
		t.Errorf("0220 = %+v", c)
	}
	if code, _ := post(t, s.handleCapture, map[string]string{"stan": pre.Request.STAN, "amount": "1.00"}); code != http.StatusConflict {
		t.Errorf("second capture: HTTP %d", code)
	}
}

func TestIncrementAfterRestartIsRefusedButCaptureWorks(t *testing.T) {
	s, _ := newBankServer(t)
	in := sale(creditPAN, iso8583.EntryManual)
	in.Expiry, in.CVV, in.Amount, in.PreAuth = "12/33", "123", "100.00", true
	_, pre := post(t, s.handleSale, in)

	restarted, _ := newBankServer(t)
	restarted.acquirer = s.acquirer
	restarted.restoreState(s.saveState())
	code, _ := post(t, restarted.handleIncrement, map[string]string{"stan": pre.Request.STAN, "amount": "10.00"})
	if code != http.StatusConflict {
		t.Errorf("increment with a masked card number: HTTP %d, want 409", code)
	}
	if code, tx := post(t, restarted.handleCapture, map[string]string{"stan": pre.Request.STAN, "amount": "90.00"}); code != 200 || tx.Captured != 9000 {
		t.Errorf("capture after restart: %d %+v", code, tx)
	}
}

func TestTipAdjust(t *testing.T) {
	s, acq := newBankServer(t)
	in := sale(creditPAN, iso8583.EntryManual)
	in.Expiry, in.CVV, in.Amount = "12/33", "123", "20.00"
	_, tx := post(t, s.handleSale, in)
	if code, _ := post(t, s.handleIncrement, map[string]string{"stan": tx.Request.STAN, "amount": "1.00"}); code != http.StatusConflict {
		t.Errorf("increment on a final sale: HTTP %d", code)
	}
	code, done := post(t, s.handleCapture, map[string]string{"stan": tx.Request.STAN, "tip": "3.00"})
	if code != 200 || done.Captured != 2300 || acq.captures[0].Amount != 2300 {
		t.Fatalf("tip: %d %+v", code, done)
	}
}

func TestFinalAmount(t *testing.T) {
	for _, c := range []struct {
		amount, tip string
		want        int64
		err         bool
	}{
		{"", "2.50", 1250, false},
		{"9.00", "", 900, false},
		{"9.00", "1.00", 0, true},
		{"", "", 0, true},
		{"", "abc", 0, true},
	} {
		got, err := finalAmount(c.amount, c.tip, 1000)
		if got != c.want || (err != nil) != c.err {
			t.Errorf("finalAmount(%q, %q) = %d, %v", c.amount, c.tip, got, err)
		}
	}
}
