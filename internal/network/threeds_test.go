package network

import (
	"context"
	"regexp"
	"testing"

	"github.com/JonahR/simple-network/internal/iso8583"
)

func (c *bankClient) Authenticate3DS(_ context.Context, r iso8583.ThreeDSRequest) (iso8583.ThreeDSResult, error) {
	return c.bank.Authenticate3DS(r), nil
}

func (c *bankClient) Challenge3DS(_ context.Context, ch iso8583.ThreeDSChallenge) (iso8583.ThreeDSResult, error) {
	return c.bank.Challenge3DS(ch), nil
}

func ecom(pan, expiry string, amount int64, res iso8583.ThreeDSResult) iso8583.AuthRequest {
	r := auth(pan, expiry, amount)
	r.EntryMode = iso8583.EntryEcommerce
	r.ECI, r.CAVV = res.ECI, res.CAVV
	return r
}

func TestThreeDSFrictionless(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	res := f.sw.Authenticate3DS(ctx, iso8583.ThreeDSRequest{PAN: "4242424242424242", Expiry: "3312", Amount: 2500})
	if res.Status != "Y" || res.ECI != "05" || res.CAVV == "" || res.ID == "" {
		t.Fatalf("frictionless: %+v", res)
	}
	if resp := f.sw.Authorize(ctx, ecom("4242424242424242", "3312", 2500, res)); resp.ResponseCode != "00" {
		t.Fatalf("authorize with CAVV: %+v", resp)
	}
	// A CAVV is good for one authorization.
	if resp := f.sw.Authorize(ctx, ecom("4242424242424242", "3312", 2500, res)); resp.ResponseCode != "05" {
		t.Errorf("reused CAVV: %+v", resp)
	}
}

func TestThreeDSChallenge(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	res := f.sw.Authenticate3DS(ctx, iso8583.ThreeDSRequest{PAN: "4242424242424242", Expiry: "3312", Amount: 50000})
	if res.Status != "C" || res.Challenge == "" {
		t.Fatalf("expected a challenge: %+v", res)
	}
	otp := regexp.MustCompile(`code is (\d{6})`).FindStringSubmatch(res.Challenge)
	if otp == nil {
		t.Fatalf("no code in %q", res.Challenge)
	}
	wrong := f.sw.Challenge3DS(ctx, iso8583.ThreeDSChallenge{ID: res.ID, OTP: "000000x"})
	if wrong.Status != "C" {
		t.Fatalf("wrong code: %+v", wrong)
	}
	ok := f.sw.Challenge3DS(ctx, iso8583.ThreeDSChallenge{ID: res.ID, OTP: otp[1]})
	if ok.Status != "Y" || ok.CAVV == "" {
		t.Fatalf("right code: %+v", ok)
	}
	// The CAVV is bound to the authenticated amount.
	if resp := f.sw.Authorize(ctx, ecom("4242424242424242", "3312", 60000, ok)); resp.ResponseCode != "05" {
		t.Errorf("CAVV for another amount: %+v", resp)
	}
	if resp := f.sw.Authorize(ctx, ecom("4242424242424242", "3312", 50000, ok)); resp.ResponseCode != "00" {
		t.Errorf("authorize after challenge: %+v", resp)
	}
	// The challenge is finished; the ID is no longer routable.
	if again := f.sw.Challenge3DS(ctx, iso8583.ThreeDSChallenge{ID: res.ID, OTP: otp[1]}); again.Status != "N" {
		t.Errorf("finished challenge: %+v", again)
	}
}

func TestThreeDSUnknownCard(t *testing.T) {
	f := newFixture(t)
	res := f.sw.Authenticate3DS(context.Background(), iso8583.ThreeDSRequest{PAN: "9999999999999999", Expiry: "3312", Amount: 100})
	if res.Status != "U" {
		t.Errorf("%+v", res)
	}
}
