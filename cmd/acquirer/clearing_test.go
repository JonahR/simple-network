package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/JonahR/simple-network/internal/card"
	"github.com/JonahR/simple-network/internal/clearing"
	"github.com/JonahR/simple-network/internal/iso8583"
)

func record(id, status string, amount int64) Record {
	return Record{
		Received: time.Date(2026, 10, 9, 15, 0, 0, 0, time.UTC), Status: status, MerchantID: "M1", MerchantName: "Shop",
		Request:  iso8583.AuthRequest{PAN: "424242******4242", Currency: "840", MCC: "5814", EntryMode: "051", RRN: "628215000001", ProcessingCode: "003000"},
		Response: iso8583.AuthResponse{NetworkTxnID: id, Amount: amount, ResponseCode: map[string]string{"APPROVED": "00", "DECLINED": "51"}[status], AuthCode: "ABC123"},
	}
}

func TestSubmitAndFund(t *testing.T) {
	var got []clearing.File
	reply := func(f clearing.File) clearing.Ack {
		ack := clearing.Ack{FileID: f.Header.FileID, Status: clearing.FileAccepted, Rejected: []clearing.Rejection{}}
		for _, p := range f.Records {
			if p.NetworkTxnID == "t2" {
				ack.Rejected = append(ack.Rejected, clearing.Rejection{NetworkTxnID: "t2", Reason: "the authorization was reversed"})
			} else {
				ack.Accepted++
			}
		}
		return ack
	}
	down := true
	net := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		var f clearing.File
		json.NewDecoder(r.Body).Decode(&f)
		got = append(got, f)
		json.NewEncoder(w).Encode(reply(f))
	}))
	defer net.Close()

	acq := newAcquirer("100001", "Bank", []Merchant{{ID: "M1", Name: "Shop", DiscountBPS: 250}}, nil)
	acq.records = []Record{record("t1", "APPROVED", 10_000), record("t2", "APPROVED", 500), record("t3", "DECLINED", 0)}
	c := newClearer(acq, net.URL)

	// The network is down: nothing is presented, and the file is kept.
	if _, err := c.Submit(context.Background()); err == nil {
		t.Fatal("want an error while the network is down")
	}
	down = false
	sent, err := c.Submit(context.Background())
	if err != nil || sent.Ack == nil || sent.Ack.Accepted != 1 {
		t.Fatalf("submit: %v, %+v", err, sent)
	}
	f := got[0]
	if f.Header.Sequence != 1 || len(f.Records) != 2 || f.Trailer.HashTotal != 10_500 || f.CheckControls() != nil {
		t.Errorf("file %+v", f)
	}
	if a := f.Records[0].ARN; len(a) != 23 || !card.Luhn(a) {
		t.Errorf("ARN %q is not 23 Luhn-valid digits", a)
	}
	v := c.View()
	if v.Status["t1"] != presented || v.Status["t2"] != rejected || v.Unpresented != 0 {
		t.Errorf("statuses %+v, unpresented %d", v.Status, v.Unpresented)
	}

	// Nothing new: the next file is empty and has the next sequence.
	c.Submit(context.Background())
	if len(got) != 2 || got[1].Header.Sequence != 2 || len(got[1].Records) != 0 {
		t.Errorf("second file: %+v", got[1].Header)
	}

	// Funding: 2.50% discount on $100 = $2.50; interchange $1.60 and network $0.16 leave a $0.74 margin.
	advice := clearing.Advice{CycleID: "c1", AcquirerID: "100001", Currency: "840", Items: []clearing.AdviceItem{
		{NetworkTxnID: "t1", MerchantID: "M1", Amount: 10_000, Interchange: 160, NetworkFee: 16},
	}}
	fs := c.Fund(advice)
	if len(fs) != 1 || fs[0].Paid != 9_750 || fs[0].DiscountFee != 250 || fs[0].Margin != 74 {
		t.Fatalf("funding %+v", fs)
	}
	if c.Fund(advice) != nil || len(c.View().Fundings) != 1 {
		t.Error("the same advice funded twice")
	}
	if c.View().Status["t1"] != funded {
		t.Error("t1 not marked funded")
	}
}
