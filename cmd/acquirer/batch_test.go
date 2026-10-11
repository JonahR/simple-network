package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/JonahR/simple-network/internal/clearing"
	"github.com/JonahR/simple-network/internal/iso8583"
)

func closeReq(proc, batch string, totals []iso8583.BatchTotals) iso8583.Reconciliation {
	return iso8583.Reconciliation{MTI: "0500", ProcessingCode: proc, STAN: "000050", TerminalID: "T1", MerchantID: "M1",
		BatchNumber: batch, Totals: totals}
}

func TestBatchCloseReconcilesAndClears(t *testing.T) {
	var files []clearing.File
	net := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var f clearing.File
		json.NewDecoder(r.Body).Decode(&f)
		files = append(files, f)
		json.NewEncoder(w).Encode(clearing.Ack{FileID: f.Header.FileID, Status: clearing.FileAccepted, Accepted: len(f.Records), Rejected: []clearing.Rejection{}})
	}))
	defer net.Close()

	acq := newAcquirer("100001", "Bank", []Merchant{{ID: "M1", Name: "Shop", Terminals: []string{"T1", "T2"}}}, nil)
	sale := onTerminal(rec("sale", "APPROVED", 1000), "000001")
	sale.Captured = 1200 // A tip
	refund := onTerminal(rec("refund", "APPROVED", 300), "000002")
	refund.Request.ProcessingCode = "200000"
	pre := onTerminal(rec("pre", "APPROVED", 5000), "000003") // Open: held out
	pre.Request.AuthType = iso8583.AuthEstimated
	declined := onTerminal(rec("declined", "DECLINED", 700), "000004")
	voided := onTerminal(rec("voided", "APPROVED", 900), "000005")
	voided.Reversal = ReversalAcknowledged
	other := rec("other", "APPROVED", 4000) // Another terminal's batch
	other.TerminalID, other.TerminalSTAN = "T2", "000001"
	acq.records = []*Record{sale, refund, pre, declined, voided, other}
	c := newClearer(acq, net.URL)
	ctx := context.Background()

	want := []iso8583.BatchTotals{{Currency: "840", SalesCount: 1, SalesAmount: 1200, RefundsCount: 1, RefundsAmount: 300, Net: 900}}

	// Totals that differ: 95, with the acquirer's totals and items, nothing closed.
	wrong := []iso8583.BatchTotals{{Currency: "840", SalesCount: 1, SalesAmount: 1000, Net: 1000}}
	r, err := c.CloseBatch(ctx, closeReq(iso8583.ProcBatchClose, "001", wrong))
	if err != nil || r.ResponseCode != "95" || !iso8583.SameTotals(r.HostTotals, want) || len(r.Items) != 2 || r.Held != 1 {
		t.Fatalf("mismatch: %+v %v", r, err)
	}
	if sale.Batch != "" || len(files) != 0 {
		t.Fatalf("a 95 closed the batch or sent a file")
	}

	// Matching totals: 00, sealed, and only this batch goes to clearing.
	r, err = c.CloseBatch(ctx, closeReq(iso8583.ProcBatchClose, "001", want))
	if err != nil || r.ResponseCode != "00" || r.ClearingFileID == "" || r.Held != 1 {
		t.Fatalf("close: %+v %v", r, err)
	}
	if sale.Batch != "001" || refund.Batch != "001" || pre.Batch != "" || other.Batch != "" {
		t.Errorf("batches: sale %q refund %q pre %q other %q", sale.Batch, refund.Batch, pre.Batch, other.Batch)
	}
	if len(files) != 1 || len(files[0].Records) != 2 || files[0].Records[0].Amount != 1200 {
		t.Fatalf("clearing files %+v", files)
	}

	// The same close again (a lost 0510) gets the same answer and sends nothing new.
	if again, _ := c.CloseBatch(ctx, closeReq(iso8583.ProcBatchClose, "001", want)); again.ResponseCode != "00" || len(files) != 1 {
		t.Errorf("repeat close: %+v, %d files", again, len(files))
	}

	// A closed sale can't take a tip.
	if r, _ := c.Capture(completion("000001", 1100)); r.ResponseCode != "12" {
		t.Errorf("tip after close: %+v", r)
	}

	// The pre-auth, once captured, is in the next batch.
	if r, _ := c.Capture(completion("000003", 4800)); r.ResponseCode != "00" {
		t.Fatalf("capture: %+v", r)
	}
	next := []iso8583.BatchTotals{{Currency: "840", SalesCount: 1, SalesAmount: 4800, Net: 4800}}
	if r, _ := c.CloseBatch(ctx, closeReq(iso8583.ProcBatchClose, "002", next)); r.ResponseCode != "00" || r.Held != 0 || pre.Batch != "002" {
		t.Errorf("second batch: %+v", r)
	}
}

func TestBatchCloseForcedTakesTheAcquirersTotals(t *testing.T) {
	acq := newAcquirer("100001", "Bank", []Merchant{{ID: "M1", Name: "Shop", Terminals: []string{"T1"}}}, nil)
	sale := onTerminal(rec("sale", "APPROVED", 1000), "000001")
	acq.records = []*Record{sale}
	r, ids, err := acq.CloseBatch(closeReq(iso8583.ProcBatchCloseForced, "007", nil))
	if err != nil || r.ResponseCode != "00" || len(ids) != 1 || sale.Batch != "007" {
		t.Fatalf("forced close: %+v %v %v", r, ids, err)
	}
	for _, bad := range []iso8583.Reconciliation{
		closeReq("000000", "001", nil),
		closeReq(iso8583.ProcBatchClose, "1", nil),
		closeReq(iso8583.ProcBatchClose, "000", nil),
	} {
		if _, _, err := acq.CloseBatch(bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	if b := acq.Batches(); len(b) != 1 || b[0].Count != 1 {
		t.Errorf("batches %+v", b)
	}
}
