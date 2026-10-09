package main

import (
	"testing"

	"github.com/JonahR/simple-network/internal/iso8583"
)

func TestBalanceInquiryRequest(t *testing.T) {
	s := newTestServer()
	in := sale(prepaidPAN, iso8583.EntryManual)
	in.CVV, in.Amount, in.BalanceInquiry = "123", "", true
	req, errs := s.buildAuthRequest(in, testNow)
	if len(errs) > 0 {
		t.Fatalf("inquiry: %v", errs)
	}
	if req.ProcessingCode[:2] != "30" || req.Amount != 0 {
		t.Errorf("0100 DE3 %s, DE4 %d; want 30xxxx and 0", req.ProcessingCode, req.Amount)
	}

	in.Cashback = "5.00"
	if _, errs := s.buildAuthRequest(in, testNow); errs["form"] == "" {
		t.Error("inquiry with cash back accepted")
	}
}

func TestBalanceInquiryOutcome(t *testing.T) {
	req := iso8583.AuthRequest{ProcessingCode: "300000"}
	resp := iso8583.AuthResponse{ResponseCode: "00", AdditionalAmounts: []iso8583.AdditionalAmount{{Type: "02", Amount: 4250}}}
	status, msg := outcome(req, resp)
	if status != "BALANCE" || msg != "Available balance: $42.50. Nothing was charged or held." {
		t.Errorf("outcome = %s %q", status, msg)
	}
}
