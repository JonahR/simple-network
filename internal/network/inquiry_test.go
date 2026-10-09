package network

import (
	"context"
	"testing"
)

func TestBalanceInquiryValidation(t *testing.T) {
	f := newFixture(t)
	r := auth("4242424242424242", "3312", 0)
	r.ProcessingCode = "303000"
	resp := f.sw.Authorize(context.Background(), r)
	if resp.ResponseCode != "00" || len(resp.AdditionalAmounts) != 1 {
		t.Errorf("inquiry: %+v", resp)
	}
	withAmount := auth("4242424242424242", "3312", 100)
	withAmount.ProcessingCode = "303000"
	if resp := f.sw.Authorize(context.Background(), withAmount); resp.ResponseCode != "30" {
		t.Errorf("inquiry with an amount: %+v", resp)
	}
}
