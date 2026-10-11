package network

import (
	"context"
	"testing"

	"github.com/JonahR/simple-network/internal/iso8583"
)

func linkedRefund(pan, expiry string, amount int64, original string) iso8583.AuthRequest {
	r := auth(pan, expiry, amount)
	r.ProcessingCode, r.OriginalTxnID = "200000", original
	return r
}

func TestLinkedRefundsAreCheckedAndCapped(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	sale := f.sw.Authorize(ctx, auth("4242424242424242", "3312", 1000))

	first := f.sw.Authorize(ctx, linkedRefund("4242424242424242", "3312", 400, sale.NetworkTxnID))
	if first.ResponseCode != "00" {
		t.Fatalf("refund: %+v", first)
	}
	if r, _ := f.sw.Recorder.Get(sale.NetworkTxnID); r.Refunded != 400 {
		t.Errorf("sale refunded %d, want 400", r.Refunded)
	}
	if resp := f.sw.Authorize(ctx, linkedRefund("4242424242424242", "3312", 700, sale.NetworkTxnID)); resp.ResponseCode != iso8583.RCInvalidTransaction {
		t.Errorf("400 + 700 refunded on a 1000 sale: %+v", resp)
	}
	if resp := f.sw.Authorize(ctx, linkedRefund("5555555555554444", "3208", 1, sale.NetworkTxnID)); resp.ResponseCode != iso8583.RCInvalidTransaction {
		t.Errorf("refund to another card: %+v", resp)
	}
	if resp := f.sw.Authorize(ctx, linkedRefund("4242424242424242", "3312", 1, first.NetworkTxnID)); resp.ResponseCode != iso8583.RCInvalidTransaction {
		t.Errorf("refund of a refund: %+v", resp)
	}
	// An unlinked refund is still allowed (the card is presented again).
	unlinked := auth("4242424242424242", "3312", 100)
	unlinked.ProcessingCode = "200000"
	if resp := f.sw.Authorize(ctx, unlinked); resp.ResponseCode != "00" {
		t.Errorf("unlinked refund: %+v", resp)
	}
	other := auth("4242424242424242", "3312", 100)
	other.OriginalTxnID = sale.NetworkTxnID
	if resp := f.sw.Authorize(ctx, other); resp.ResponseCode != iso8583.RCFormatError {
		t.Errorf("a purchase naming an original: %+v", resp)
	}
}
