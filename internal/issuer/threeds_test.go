package issuer

import (
	"testing"

	"github.com/JonahR/simple-network/internal/iso8583"
)

func TestAVSResults(t *testing.T) {
	fsb, _, _ := testBanks(t)
	cases := []struct {
		zip, street, want string
	}{
		{"94105", "123 Market St", "Y"},
		{"94105-1234", "123 Market Street", "Y"},
		{"94105", "999 Elm", "Z"},
		{"10001", "123 Main", "A"},
		{"10001", "", "N"},
	}
	for _, c := range cases {
		r := req("4242424242424242", "3312", 100)
		r.AVS = &iso8583.AVSData{Zip: c.zip, Street: c.street}
		resp := fsb.Authorize(r)
		if resp.ResponseCode != "00" || resp.AVSResult != c.want {
			t.Errorf("AVS %q %q: got %s (rc %s), want %s", c.zip, c.street, resp.AVSResult, resp.ResponseCode, c.want)
		}
	}
}

func TestCVV2Results(t *testing.T) {
	fsb, _, _ := testBanks(t)
	r := req("4242424242424242", "3312", 100)
	r.CVV2 = "123"
	if resp := fsb.Authorize(r); resp.CVV2Result != "M" || resp.ResponseCode != "00" {
		t.Errorf("match: %+v", resp)
	}
	r = req("4242424242424242", "3312", 100)
	r.CVV2 = "999"
	if resp := fsb.Authorize(r); resp.CVV2Result != "N" || resp.ResponseCode != iso8583.RCCVVMismatch {
		t.Errorf("mismatch: %+v", resp)
	}
}

func TestThreeDSDeclinesFrozenAndWrongExpiry(t *testing.T) {
	fsb, _, _ := testBanks(t)
	if res := fsb.Authenticate3DS(iso8583.ThreeDSRequest{ID: "a", PAN: "4242424242424242", Expiry: "3001", Amount: 100}); res.Status != "N" {
		t.Errorf("wrong expiry: %+v", res)
	}
	res := fsb.Authenticate3DS(iso8583.ThreeDSRequest{ID: "b", PAN: "4242424242424242", Expiry: "3312", Amount: 50000})
	for range maxOTPTries {
		res = fsb.Challenge3DS(iso8583.ThreeDSChallenge{ID: "b", OTP: "x"})
	}
	if res.Status != "N" {
		t.Errorf("after %d wrong codes: %+v", maxOTPTries, res)
	}
}
