package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/JonahR/simple-network/internal/demokeys"
	"github.com/JonahR/simple-network/internal/iso8583"
	"github.com/JonahR/simple-network/internal/issuer"
)

// bankAcquirer stands in for the acquirer, network, and issuer: it answers
// 3-D Secure and authorizations with a real issuer bank.
type bankAcquirer struct {
	bank *issuer.Bank
	sent []iso8583.AuthRequest
}

func (a *bankAcquirer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/3ds/authenticate":
		var req iso8583.ThreeDSRequest
		json.NewDecoder(r.Body).Decode(&req)
		req.ID = "3ds-" + req.Expiry + "-" + string(rune('a'+len(a.sent)))
		json.NewEncoder(w).Encode(a.bank.Authenticate3DS(req))
	case "/3ds/challenge":
		var c iso8583.ThreeDSChallenge
		json.NewDecoder(r.Body).Decode(&c)
		json.NewEncoder(w).Encode(a.bank.Challenge3DS(c))
	case "/authorize":
		var req iso8583.AuthRequest
		json.NewDecoder(r.Body).Decode(&req)
		req.NetworkTxnID = "NTX-" + req.STAN
		a.sent = append(a.sent, req)
		json.NewEncoder(w).Encode(a.bank.Authorize(req))
	}
}

func newBankServer(t *testing.T) (*server, *bankAcquirer) {
	t.Helper()
	key, err := demokeys.Load("UNSET_TEST_KEY", demokeys.IssuerFSB)
	if err != nil {
		t.Fatal(err)
	}
	accts, extra := issuer.FirstSimpleBankAccounts()
	acq := &bankAcquirer{bank: issuer.NewBank("FSB", "First Simple Bank", key, accts, extra)}
	srv := httptest.NewServer(acq)
	t.Cleanup(srv.Close)
	s := newTestServer()
	s.acquirer = newAcquirerClient(srv.URL)
	return s, acq
}

func onlineSale(amount string) SaleInput {
	in := sale(creditPAN, iso8583.EntryEcommerce)
	in.Expiry, in.CVV, in.Amount = "12/33", "123", amount
	in.BillingZip, in.BillingStreet = "94105", "123 Market St"
	return in
}

func TestOnlineSaleSendsAVSAndShowsResults(t *testing.T) {
	s, acq := newBankServer(t)
	_, tx := post(t, s.handleSale, onlineSale("10.00"))
	if tx.Status != "APPROVED" || tx.Response.AVSResult != "Y" || tx.Response.CVV2Result != "M" {
		t.Fatalf("%+v %+v", tx, tx.Response)
	}
	if got := acq.sent[0]; got.ECI != "07" || got.AVS == nil || got.AVS.Zip != "94105" {
		t.Errorf("0100 = %+v", got)
	}
}

func TestThreeDSFrictionlessSale(t *testing.T) {
	s, acq := newBankServer(t)
	in := onlineSale("25.00")
	in.ThreeDS = true
	_, tx := post(t, s.handleSale, in)
	if tx.Status != "APPROVED" || tx.ThreeDS == nil || tx.ThreeDS.Status != "Y" {
		t.Fatalf("%+v", tx)
	}
	if got := acq.sent[0]; got.ECI != "05" || got.CAVV == "" {
		t.Errorf("0100 = %+v", got)
	}
}

func TestThreeDSChallengeSale(t *testing.T) {
	s, acq := newBankServer(t)
	in := onlineSale("250.00")
	in.ThreeDS = true
	_, tx := post(t, s.handleSale, in)
	if tx.Status != "CHALLENGE" || len(acq.sent) != 0 {
		t.Fatalf("expected a challenge before any 0100: %+v", tx)
	}
	otp := regexp.MustCompile(`code is (\d{6})`).FindStringSubmatch(tx.Message)[1]
	_, done := post(t, s.handleChallenge, iso8583.ThreeDSChallenge{ID: tx.ThreeDS.ID, OTP: otp})
	if done.Status != "APPROVED" || acq.sent[0].ECI != "05" || acq.sent[0].Amount != 25000 {
		t.Fatalf("%+v", done)
	}
	// The held sale is gone once used.
	if code, _ := post(t, s.handleChallenge, iso8583.ThreeDSChallenge{ID: tx.ThreeDS.ID, OTP: otp}); code != http.StatusUnprocessableEntity {
		t.Errorf("reused challenge: HTTP %d", code)
	}
}

func TestThreeDSFailureIsNotSent(t *testing.T) {
	s, acq := newBankServer(t)
	in := onlineSale("250.00")
	in.ThreeDS = true
	_, tx := post(t, s.handleSale, in)
	for range 3 {
		_, tx = post(t, s.handleChallenge, iso8583.ThreeDSChallenge{ID: tx.ThreeDS.ID, OTP: "000000x"})
	}
	if tx.Status != "NOT_AUTHENTICATED" || len(acq.sent) != 0 {
		t.Fatalf("%+v, sent %d", tx, len(acq.sent))
	}
}

func TestCardNotPresentChecksNeedCardNotPresent(t *testing.T) {
	s := newTestServer()
	in := sale(creditPAN, iso8583.EntryManual)
	in.CVV, in.ThreeDS = "123", true
	if _, errs := s.buildAuthRequest(in, testNow); errs["three_ds"] == "" {
		t.Error("3-D Secure on a keyed sale should be refused")
	}
	in = onlineSale("1.00")
	in.EntryMode, in.CVV, in.Track2 = iso8583.EntryMagstripe, "", "x"
	if _, errs := s.buildAuthRequest(in, testNow); errs["billing_zip"] == "" {
		t.Error("AVS on a swipe should be refused")
	}
	in = onlineSale("1.00")
	in.BillingZip = "941"
	if _, errs := s.buildAuthRequest(in, testNow); errs["billing_zip"] == "" {
		t.Error("short ZIP accepted")
	}
}
