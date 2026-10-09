package network

import (
	"context"
	"testing"

	"github.com/JonahR/simple-network/internal/iso8583"
)

func TestIncrementalAuthorization(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	first := auth("4242424242424242", "3312", 10000)
	first.AuthType = iso8583.AuthEstimated
	orig := f.sw.Authorize(ctx, first)
	if orig.ResponseCode != "00" {
		t.Fatalf("estimate: %+v", orig)
	}

	more := auth("4242424242424242", "3312", 2500)
	more.AuthType, more.OriginalTxnID = iso8583.AuthIncremental, orig.NetworkTxnID
	inc := f.sw.Authorize(ctx, more)
	if inc.ResponseCode != "00" || inc.NetworkTxnID == orig.NetworkTxnID {
		t.Fatalf("increment: %+v", inc)
	}
	if r, _ := f.sw.Recorder.Get(orig.NetworkTxnID); r.Incremented != 2500 {
		t.Errorf("original incremented by %d, want 2500", r.Incremented)
	}

	// An increment must name a live original for the same card.
	bad := auth("5555555555554444", "3208", 100)
	bad.AuthType, bad.OriginalTxnID = iso8583.AuthIncremental, orig.NetworkTxnID
	if resp := f.sw.Authorize(ctx, bad); resp.ResponseCode != iso8583.RCInvalidTransaction {
		t.Errorf("other card: %+v", resp)
	}
	unknown := auth("4242424242424242", "3312", 100)
	unknown.AuthType, unknown.OriginalTxnID = iso8583.AuthIncremental, "nope"
	if resp := f.sw.Authorize(ctx, unknown); resp.ResponseCode != iso8583.RCInvalidTransaction {
		t.Errorf("unknown original: %+v", resp)
	}
	noOrig := auth("4242424242424242", "3312", 100)
	noOrig.AuthType = iso8583.AuthIncremental
	if resp := f.sw.Authorize(ctx, noOrig); resp.ResponseCode != iso8583.RCFormatError {
		t.Errorf("no original named: %+v", resp)
	}
}
