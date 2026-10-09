package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JonahR/simple-network/internal/clearing"
	"github.com/JonahR/simple-network/internal/iso8583"
)

// onTerminal places a record as terminal T1's STAN.
func onTerminal(r *Record, stan string) *Record {
	r.TerminalID, r.TerminalSTAN = "T1", stan
	r.Request.TransmissionTime = "1009150000"
	return r
}

func completion(stan string, amount int64) iso8583.Completion {
	return iso8583.Completion{MTI: "0220", STAN: "000099", Amount: amount, Currency: "840", TerminalID: "T1", MerchantID: "M1",
		OriginalData: &iso8583.OriginalData{MTI: "0100", STAN: stan, TransmissionTime: "1009150000"}}
}

func TestCaptureTipsPreAuthsAndIncrements(t *testing.T) {
	var files []clearing.File
	net := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var f clearing.File
		json.NewDecoder(r.Body).Decode(&f)
		files = append(files, f)
		json.NewEncoder(w).Encode(clearing.Ack{FileID: f.Header.FileID, Status: clearing.FileAccepted, Accepted: len(f.Records), Rejected: []clearing.Rejection{}})
	}))
	defer net.Close()

	acq := newAcquirer("100001", "Bank", []Merchant{{ID: "M1", Name: "Shop", Terminals: []string{"T1"}}}, nil)
	sale := onTerminal(rec("sale", "APPROVED", 1000), "000001") // MCC 5814: tips up to 20%
	pre := onTerminal(rec("pre", "APPROVED", 10_000), "000002")
	pre.Request.AuthType = iso8583.AuthEstimated
	inc := onTerminal(rec("inc", "APPROVED", 5000), "000003")
	inc.Request.AuthType, inc.Request.OriginalTxnID = iso8583.AuthIncremental, "pre"
	open := onTerminal(rec("open", "APPROVED", 3000), "000004")
	open.Request.AuthType = iso8583.AuthEstimated
	acq.records = []*Record{sale, pre, inc, open}
	c := newClearer(acq, net.URL)

	// A tip within the tolerance is captured; one beyond it isn't.
	if r, _ := c.Capture(completion("000001", 1201)); r.ResponseCode != "12" || !strings.Contains(r.Message, "incremental") {
		t.Errorf("tip over 20%%: %+v", r)
	}
	if r, _ := c.Capture(completion("000001", 1200)); r.ResponseCode != "00" || r.Captured != 1200 {
		t.Errorf("tip: %+v", r)
	}
	// The pre-auth's limit counts the increment: 15,000 authorized, +20%.
	if r, _ := c.Capture(completion("000002", 14_000)); r.ResponseCode != "00" || r.Authorized != 15_000 {
		t.Errorf("pre-auth final bill: %+v", r)
	}
	if r, _ := c.Capture(completion("000003", 100)); r.ResponseCode != "12" {
		t.Errorf("capturing an increment: %+v", r)
	}

	if _, err := c.Submit(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, p := range files[0].Records {
		got[p.NetworkTxnID] = p.Amount
	}
	// The uncaptured pre-auth waits; the increment rides with its original.
	if len(got) != 2 || got["sale"] != 1200 || got["pre"] != 14_000 {
		t.Errorf("presented %v", got)
	}
	// Once presented, the amount can't change.
	if r, _ := c.Capture(completion("000001", 1100)); r.ResponseCode != "12" || !strings.Contains(r.Message, "clearing file") {
		t.Errorf("after presentment: %+v", r)
	}
}
