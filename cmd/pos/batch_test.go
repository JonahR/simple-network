package main

import (
	"context"
	"net/http"
	"testing"

	"github.com/JonahR/simple-network/internal/iso8583"
)

func TestBatchCloseSendsTotalsAndSeals(t *testing.T) {
	s, acq := newBankServer(t)
	keyed := func(amount string) SaleInput {
		in := sale(creditPAN, iso8583.EntryManual)
		in.Expiry, in.CVV, in.Amount = "12/33", "123", amount
		return in
	}
	_, a := post(t, s.handleSale, keyed("20.00"))
	if code, _ := post(t, s.handleCapture, map[string]string{"stan": a.Request.STAN, "amount": "23.00"}); code != 200 {
		t.Fatalf("tip: HTTP %d", code)
	}
	_, b := post(t, s.handleSale, keyed("10.00"))
	refund := keyed("4.00")
	refund.Refund, refund.RefundOf = true, b.Request.STAN
	_, r := post(t, s.handleSale, refund)
	pre := keyed("50.00")
	pre.PreAuth = true
	_, p := post(t, s.handleSale, pre)
	_, v := post(t, s.handleSale, keyed("7.00"))
	post(t, s.handleVoid, map[string]string{"stan": v.Request.STAN})
	if r.Status != "REFUND" || p.Status != "APPROVED" {
		t.Fatalf("setup: refund %s, pre-auth %s", r.Status, p.Status)
	}

	l := s.lanes[0]
	report := s.closeBatch(context.Background(), l, false)
	rq := acq.closes[0]
	want := []iso8583.BatchTotals{{Currency: "840", SalesCount: 2, SalesAmount: 3300, RefundsCount: 1, RefundsAmount: 400, Net: 2900}}
	if rq.MTI != "0500" || rq.ProcessingCode != "920000" || rq.BatchNumber != "001" || !iso8583.SameTotals(rq.Totals, want) {
		t.Fatalf("0500 = %+v", rq)
	}
	if report.Response == nil || report.Response.ResponseCode != "00" {
		t.Fatalf("report %+v", report)
	}
	s.mu.Lock()
	v1 := s.batchView(l.TerminalID)
	sealed := s.findTx(a.Request.STAN).Batch
	s.mu.Unlock()
	if v1.BatchNumber != "002" || v1.Count != 0 || v1.Held != 1 || sealed != "001" {
		t.Errorf("after close: %+v, sale batch %q", v1, sealed)
	}
	// A closed sale can't be voided or tipped.
	if code, _ := post(t, s.handleVoid, map[string]string{"stan": b.Request.STAN}); code != http.StatusConflict {
		t.Errorf("void after close: HTTP %d", code)
	}
	if code, _ := post(t, s.handleCapture, map[string]string{"stan": b.Request.STAN, "amount": "11.00"}); code != http.StatusConflict {
		t.Errorf("tip after close: HTTP %d", code)
	}

	// The batch number survives a restart.
	restarted, _ := newBankServer(t)
	restarted.restoreState(s.saveState())
	restarted.mu.Lock()
	defer restarted.mu.Unlock()
	if got := restarted.batchView(l.TerminalID); got.BatchNumber != "002" || len(got.Reports) != 1 {
		t.Errorf("restored batch %+v", got)
	}
}

func TestBatchCloseMismatchShowsDifferences(t *testing.T) {
	s, acq := newBankServer(t)
	in := sale(creditPAN, iso8583.EntryManual)
	in.Expiry, in.CVV, in.Amount = "12/33", "123", "20.00"
	_, a := post(t, s.handleSale, in)
	// The acquirer has a different amount for the sale and one the terminal doesn't.
	acq.hostItems = []iso8583.BatchItem{
		{STAN: a.Request.STAN, Amount: 2400, Currency: "840"},
		{STAN: "999999", Amount: 500, Currency: "840"},
	}
	l := s.lanes[0]
	report := s.closeBatch(context.Background(), l, false)
	if report.Response.ResponseCode != "95" || len(report.Differences) != 2 {
		t.Fatalf("report %+v", report)
	}
	d := report.Differences
	if d[0].STAN != a.Request.STAN || d[0].TerminalAmount != 2000 || d[0].HostAmount != 2400 || !d[1].OnHost || d[1].OnTerminal {
		t.Errorf("differences %+v", d)
	}
	s.mu.Lock()
	open := s.batchView(l.TerminalID)
	s.mu.Unlock()
	if open.BatchNumber != "001" || open.Count != 1 {
		t.Fatalf("a 95 closed the batch: %+v", open)
	}

	report = s.closeBatch(context.Background(), l, true)
	if acq.closes[1].ProcessingCode != "960000" || report.Response.ResponseCode != "00" {
		t.Fatalf("forced close %+v", report)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if v := s.batchView(l.TerminalID); v.BatchNumber != "002" || v.Count != 0 {
		t.Errorf("after forced close %+v", v)
	}
}

func TestBatchCloseWithoutAnAnswerStaysOpen(t *testing.T) {
	s := newTestServer()
	s.acquirer = newAcquirerClient("http://127.0.0.1:1")
	report := s.closeBatch(context.Background(), s.lanes[0], false)
	if report.Error == "" || report.Response != nil {
		t.Fatalf("report %+v", report)
	}
	if s.batchNumber(s.lanes[0].TerminalID) != "001" {
		t.Error("batch number moved without an answer")
	}
}
