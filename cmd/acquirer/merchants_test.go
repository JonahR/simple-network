package main

import (
	"context"
	"testing"

	"github.com/JonahR/simple-network/internal/iso8583"
	"github.com/JonahR/simple-network/internal/merchant"
)

// Every lane the POS can switch to must be a terminal the acquirer has on
// file, or its sales would be declined 03 Invalid merchant.
func TestEveryPOSLaneIsSignedUp(t *testing.T) {
	net := &fakeNetwork{code: iso8583.RCApproved}
	all := append(merchant.FromEnv(func(string) string { return "" }), acquirerOnlyMerchants...)
	a := newAcquirer("100001", "Test Bank", all, net)
	for i, m := range merchant.Catalog {
		req := request()
		req.MerchantID, req.TerminalID, req.MCC = m.ID, m.Terminals[0], m.MCC
		req.STAN = request().STAN[:4] + string(rune('a'+i)) // A distinct sale per lane
		if resp := a.Authorize(context.Background(), req); resp.ResponseCode != iso8583.RCApproved {
			t.Errorf("%s (%s): got %s, want approved", m.Name, m.Terminals[0], resp.ResponseCode)
		}
	}
	if len(net.got) != len(merchant.Catalog) {
		t.Errorf("network saw %d sales, want one per lane (%d)", len(net.got), len(merchant.Catalog))
	}
}
