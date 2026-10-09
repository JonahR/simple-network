package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JonahR/simple-network/internal/iso8583"
)

// fakeNetwork records what it was sent and answers with a fixed code.
type fakeNetwork struct {
	code string
	err  error
	got  []iso8583.AuthRequest
}

func (f *fakeNetwork) Authorize(_ context.Context, req iso8583.AuthRequest) (iso8583.AuthResponse, error) {
	f.got = append(f.got, req)
	if f.err != nil {
		return iso8583.AuthResponse{}, f.err
	}
	resp := reply(req, f.code)
	resp.Amount = req.Amount
	resp.NetworkTxnID = "NTX1"
	if iso8583.IsApproved(f.code) {
		resp.AuthCode = "123456"
	}
	return resp, nil
}

var shop = Merchant{ID: "M1", Name: "Shop", MCC: "5814", Terminals: []string{"T1"}, DiscountBPS: 250}

func newTestAcquirer(net Network) *acquirer {
	a := newAcquirer("100001", "Test Bank", []Merchant{shop}, net)
	a.stan.Store(0) // Network-leg STANs start at 000001
	return a
}

func request() iso8583.AuthRequest {
	return iso8583.AuthRequest{
		MTI: iso8583.MTIAuthRequest, PAN: "4242424242424242", ProcessingCode: "003000", Amount: 1000,
		TransmissionTime: "1009143000", STAN: "000042", Expiry: "2912", MCC: "5999", RRN: "628014000042",
		TerminalID: "T1", MerchantID: "M1", Currency: "840", PINData: "0123456789ABCDEF", CVV2: "123",
	}
}

func TestAuthorizeForwardsAndRestoresSTAN(t *testing.T) {
	net := &fakeNetwork{code: iso8583.RCApproved}
	a := newTestAcquirer(net)

	resp := a.Authorize(context.Background(), request())

	if len(net.got) != 1 {
		t.Fatalf("network called %d times, want 1", len(net.got))
	}
	sent := net.got[0]
	if sent.AcquirerID != "100001" {
		t.Errorf("DE32 = %q, want 100001", sent.AcquirerID)
	}
	if sent.STAN == "000042" || sent.STAN != "000001" {
		t.Errorf("network-leg STAN = %q, want the acquirer's own 000001", sent.STAN)
	}
	if sent.MCC != "5814" {
		t.Errorf("DE18 = %q, want the merchant agreement's 5814", sent.MCC)
	}
	if sent.PINData != "0123456789ABCDEF" || sent.PAN != "4242424242424242" {
		t.Error("PAN and PIN block must reach the network unchanged")
	}
	if resp.STAN != "000042" || resp.ResponseCode != iso8583.RCApproved || resp.AuthCode != "123456" {
		t.Errorf("response = %+v, want approved with the terminal's STAN 000042", resp)
	}

	rec := a.Records()[0]
	if rec.Status != "APPROVED" || rec.MerchantName != "Shop" {
		t.Errorf("record = %+v", rec)
	}
	if rec.Request.PAN != "424242******4242" || rec.Request.CVV2 != "" || rec.Request.PINData != "ENCRYPTED" {
		t.Errorf("stored request is not redacted: %+v", rec.Request)
	}
	if rec.Response.PAN == "4242424242424242" {
		t.Error("stored response holds the full PAN")
	}
	var fields []string
	for _, c := range rec.Changes {
		fields = append(fields, c.Field)
	}
	if got := strings.Join(fields, ","); got != "DE11,DE32,DE18,DE11" {
		t.Errorf("changes = %s", got)
	}
	if n := len(rec.Steps); n != 4 {
		t.Errorf("got %d steps, want POS→acq, acq→net, net→acq, acq→POS", n)
	}
}

func TestAuthorizeRejectsUnknownTerminal(t *testing.T) {
	for name, mutate := range map[string]func(*iso8583.AuthRequest){
		"unknown merchant":           func(r *iso8583.AuthRequest) { r.MerchantID = "M9" },
		"terminal of other merchant": func(r *iso8583.AuthRequest) { r.TerminalID = "T9" },
	} {
		t.Run(name, func(t *testing.T) {
			net := &fakeNetwork{code: iso8583.RCApproved}
			req := request()
			mutate(&req)
			resp := newTestAcquirer(net).Authorize(context.Background(), req)
			if resp.ResponseCode != iso8583.RCInvalidMerchant {
				t.Errorf("code = %s, want 03", resp.ResponseCode)
			}
			if len(net.got) != 0 {
				t.Error("request reached the network")
			}
		})
	}
}

func TestAuthorizeRetryGetsSameAnswer(t *testing.T) {
	net := &fakeNetwork{code: iso8583.RCApproved}
	a := newTestAcquirer(net)
	first := a.Authorize(context.Background(), request())
	second := a.Authorize(context.Background(), request())
	if len(net.got) != 1 {
		t.Errorf("network called %d times for a retry, want 1", len(net.got))
	}
	if first != second {
		t.Errorf("retry got %+v, want %+v", second, first)
	}
}

func TestAuthorizeNetworkDown(t *testing.T) {
	a := newTestAcquirer(&fakeNetwork{err: errors.New("connection refused")})
	resp := a.Authorize(context.Background(), request())
	if resp.ResponseCode != iso8583.RCIssuerUnavailable || resp.STAN != "000042" {
		t.Errorf("response = %+v, want 91 with the terminal's STAN", resp)
	}
	if a.Records()[0].Status != "DECLINED" {
		t.Error("record should be declined")
	}
}

func TestOverviewTotals(t *testing.T) {
	net := &fakeNetwork{code: iso8583.RCApproved}
	s := &server{acq: newTestAcquirer(net)}
	sale := request()
	sale.Amount = 10_000
	s.acq.Authorize(context.Background(), sale)
	refund := request()
	refund.STAN, refund.ProcessingCode, refund.Amount = "000043", "203000", 2_000
	s.acq.Authorize(context.Background(), refund)
	net.code = iso8583.RCInsufficientFunds
	declined := request()
	declined.STAN = "000044"
	s.acq.Authorize(context.Background(), declined)

	rec := httptest.NewRecorder()
	s.handleOverview(rec, httptest.NewRequest(http.MethodGet, "/api/overview", nil))
	var out struct {
		Stats     stats          `json:"stats"`
		Merchants []merchantView `json:"merchants"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Stats.Count != 3 || out.Stats.Approved != 2 {
		t.Errorf("stats = %+v", out.Stats)
	}
	got := out.Merchants[0].Totals
	want := Totals{Currency: "840", Approved: 2, Declined: 1, Sales: 10_000, Refunds: 2_000, Fees: 250, Net: 7_750}
	if len(got) != 1 || got[0] != want {
		t.Errorf("totals = %+v, want %+v", got, want)
	}
}

func TestHandleAuthorizeRejectsWrongMTI(t *testing.T) {
	s := &server{acq: newTestAcquirer(&fakeNetwork{code: iso8583.RCApproved})}
	rec := httptest.NewRecorder()
	s.handleAuthorize(rec, httptest.NewRequest(http.MethodPost, "/authorize", strings.NewReader(`{"mti":"0420"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}
