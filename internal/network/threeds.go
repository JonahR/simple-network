package network

import (
	"context"
	"fmt"

	"github.com/JonahR/simple-network/internal/iso8583"
)

// ACS is an issuer's access control server for 3-D Secure. Issuer clients
// that support it implement it alongside IssuerClient.
type ACS interface {
	Authenticate3DS(ctx context.Context, r iso8583.ThreeDSRequest) (iso8583.ThreeDSResult, error)
	Challenge3DS(ctx context.Context, c iso8583.ThreeDSChallenge) (iso8583.ThreeDSResult, error)
}

func (h *HTTPIssuer) Authenticate3DS(ctx context.Context, r iso8583.ThreeDSRequest) (iso8583.ThreeDSResult, error) {
	var res iso8583.ThreeDSResult
	return res, h.post(ctx, "/3ds/authenticate", r, &res)
}

func (h *HTTPIssuer) Challenge3DS(ctx context.Context, c iso8583.ThreeDSChallenge) (iso8583.ThreeDSResult, error) {
	var res iso8583.ThreeDSResult
	return res, h.post(ctx, "/3ds/challenge", c, &res)
}

// maxThreeDS bounds the authentications the directory server remembers.
const maxThreeDS = 10_000

// Authenticate3DS is the network's directory server: it routes a 3-D Secure
// request to the card issuer's ACS by BIN, as it routes authorizations, and
// remembers which issuer it went to so a challenge answer can follow.
func (s *Switch) Authenticate3DS(ctx context.Context, r iso8583.ThreeDSRequest) iso8583.ThreeDSResult {
	r.ID = NewTxnID(s.Now())
	res := iso8583.ThreeDSResult{ID: r.ID}
	entry, ok := s.BINs.Lookup(r.PAN)
	switch {
	case !ok:
		res.Status, res.Reason = "U", "The directory server has no issuer for this card"
		return res
	case entry.TokenRange:
		res.Status, res.Reason = "U", "Wallet tokens carry their own cryptogram; 3-D Secure is for card numbers"
		return res
	}
	iss, ok := s.Issuers[entry.IssuerID]
	if !ok {
		res.Status, res.Reason = "U", "The card's issuer isn't connected"
		return res
	}
	acs, ok := iss.Client.(ACS)
	if !ok {
		res.Status, res.Reason = "U", iss.Name+" has no 3-D Secure ACS"
		return res
	}
	s.mu.Lock()
	if s.threeDS == nil || len(s.threeDS) >= maxThreeDS {
		s.threeDS = map[string]*Issuer{}
	}
	s.threeDS[r.ID] = iss
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, s.IssuerTimeout)
	defer cancel()
	out, err := acs.Authenticate3DS(ctx, r)
	if err != nil {
		res.Status, res.Reason = "U", fmt.Sprintf("%s's ACS didn't answer: %v", iss.Name, err)
		return res
	}
	out.ID = r.ID
	if out.IssuerName == "" {
		out.IssuerName = iss.Name
	}
	return out
}

// Challenge3DS passes the cardholder's answer to the ACS that issued the challenge.
func (s *Switch) Challenge3DS(ctx context.Context, c iso8583.ThreeDSChallenge) iso8583.ThreeDSResult {
	res := iso8583.ThreeDSResult{ID: c.ID}
	s.mu.Lock()
	iss := s.threeDS[c.ID]
	s.mu.Unlock()
	if iss == nil {
		res.Status, res.Reason = "N", "No 3-D Secure authentication in progress with that ID"
		return res
	}
	acs := iss.Client.(ACS)
	ctx, cancel := context.WithTimeout(ctx, s.IssuerTimeout)
	defer cancel()
	out, err := acs.Challenge3DS(ctx, c)
	if err != nil {
		res.Status, res.Reason = "U", fmt.Sprintf("%s's ACS didn't answer: %v", iss.Name, err)
		return res
	}
	if out.Status != "C" {
		s.mu.Lock()
		delete(s.threeDS, c.ID)
		s.mu.Unlock()
	}
	out.ID = c.ID
	return out
}
