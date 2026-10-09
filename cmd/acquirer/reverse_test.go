package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JonahR/simple-network/internal/iso8583"
)

// terminalAdvice is the 0420 a terminal sends to undo req.
func terminalAdvice(req iso8583.AuthRequest, reason string) iso8583.ReversalAdvice {
	return iso8583.ReversalAdvice{
		MTI: iso8583.MTIReversalAdvice, PAN: req.PAN, Amount: req.Amount, TransmissionTime: "1009143500",
		STAN: "000043", RRN: req.RRN, ResponseCode: reason, TerminalID: req.TerminalID, MerchantID: req.MerchantID,
		OriginalData: &iso8583.OriginalData{MTI: iso8583.MTIAuthRequest, STAN: req.STAN, TransmissionTime: req.TransmissionTime},
	}
}

func TestVoidTranslatesDE90ToTheNetworkLeg(t *testing.T) {
	net := &fakeNetwork{code: iso8583.RCApproved}
	a := newTestAcquirer(net)
	auth := request()
	a.Authorize(context.Background(), auth) // Network leg: STAN 000001

	ack, err := a.TerminalReverse(terminalAdvice(auth, iso8583.RCCustomerCancel))
	if err != nil || ack.MTI != "0430" || ack.ResponseCode != "00" || !ack.Matched || ack.STAN != "000043" || ack.NetworkTxnID != "NTX1" {
		t.Fatalf("0430 to terminal: %+v, %v", ack, err)
	}
	waitFor(t, "the 0420 to reach the network", func() bool { return len(net.sentReversals()) == 1 })
	sent := net.sentReversals()[0]
	if sent.OriginalData.STAN != "000001" || sent.OriginalData.AcquirerID != "100001" || sent.ResponseCode != "17" {
		t.Errorf("0420 to network: DE90 %+v, DE39 %s; want the network leg's STAN 000001 and reason 17", *sent.OriginalData, sent.ResponseCode)
	}
	waitFor(t, "the reversal to be acknowledged", func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.records[0].Reversal == ReversalAcknowledged
	})
	if r := a.Records()[0]; r.ReversalReason != "17" {
		t.Errorf("record reason %q", r.ReversalReason)
	}

	// The terminal retries: acknowledged again, but no second 0420.
	if ack, _ := a.TerminalReverse(terminalAdvice(auth, iso8583.RCCustomerCancel)); !ack.Matched {
		t.Errorf("repeat: %+v", ack)
	}
	if n := len(net.sentReversals()); n != 1 {
		t.Errorf("network got %d 0420s, want 1", n)
	}
}

func TestTerminalReversalOfUnknownSaleIsNotForwarded(t *testing.T) {
	net := &fakeNetwork{code: iso8583.RCApproved}
	a := newTestAcquirer(net)
	ack, err := a.TerminalReverse(terminalAdvice(request(), iso8583.RCLateResponse))
	if err != nil || ack.ResponseCode != "00" || ack.Matched || len(net.sentReversals()) != 0 {
		t.Errorf("ack %+v, err %v, forwarded %d", ack, err, len(net.sentReversals()))
	}
}

func TestTerminalReversalWhileTheNetworkIsAnswering(t *testing.T) {
	gate := make(chan struct{})
	net := &slowNetwork{fakeNetwork: &fakeNetwork{code: iso8583.RCApproved}, gate: gate}
	a := newTestAcquirer(net)
	auth := request()
	done := make(chan iso8583.AuthResponse)
	go func() { done <- a.Authorize(context.Background(), auth) }()
	waitFor(t, "the 0100 to reach the network", func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return len(a.legs) == 1
	})

	ack, err := a.TerminalReverse(terminalAdvice(auth, iso8583.RCLateResponse))
	if err != nil || !ack.Matched {
		t.Fatalf("ack %+v, err %v", ack, err)
	}
	close(gate) // The network finally approves
	<-done
	waitFor(t, "the late approval to be reversed", func() bool { return len(net.sentReversals()) == 1 })
	if r := a.Records()[0]; r.ReversalReason != "68" || r.Status != "APPROVED" {
		t.Errorf("record: status %s, reason %s", r.Status, r.ReversalReason)
	}
}

func TestTerminalReversalValidation(t *testing.T) {
	a := newTestAcquirer(&fakeNetwork{code: iso8583.RCApproved})
	bad := terminalAdvice(request(), iso8583.RCCustomerCancel)
	bad.OriginalData = nil
	if _, err := a.TerminalReverse(bad); err == nil {
		t.Error("missing DE90 accepted")
	}
	stranger := terminalAdvice(request(), iso8583.RCCustomerCancel)
	stranger.TerminalID = "T9"
	if _, err := a.TerminalReverse(stranger); err == nil {
		t.Error("unknown terminal accepted")
	}

	s := &server{acq: a}
	rec := httptest.NewRecorder()
	s.handleReverse(rec, httptest.NewRequest(http.MethodPost, "/reverse", strings.NewReader(`{"mti":"0100"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("HTTP %d for a non-0420", rec.Code)
	}
}

func TestVoidedSalesAreNotPaidOut(t *testing.T) {
	net := &fakeNetwork{code: iso8583.RCApproved}
	s := &server{acq: newTestAcquirer(net)}
	auth := request()
	s.acq.Authorize(context.Background(), auth)
	s.acq.TerminalReverse(terminalAdvice(auth, iso8583.RCCustomerCancel))

	rec := httptest.NewRecorder()
	s.handleOverview(rec, httptest.NewRequest(http.MethodGet, "/api/overview", nil))
	var body struct {
		Merchants []merchantView `json:"merchants"`
	}
	json.NewDecoder(rec.Body).Decode(&body)
	if tot := body.Merchants[0].Totals[0]; tot.Approved != 1 || tot.Sales != 0 || tot.Net != 0 {
		t.Errorf("a voided sale must not be paid: %+v", tot)
	}
}

// slowNetwork holds every authorization until gate closes.
type slowNetwork struct {
	*fakeNetwork
	gate chan struct{}
}

func (n *slowNetwork) Authorize(ctx context.Context, req iso8583.AuthRequest) (iso8583.AuthResponse, error) {
	<-n.gate
	return n.fakeNetwork.Authorize(ctx, req)
}
