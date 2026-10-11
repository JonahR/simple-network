package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/JonahR/simple-network/internal/iso8583"
)

// posTimeout is how long the terminal waits for an answer (D8: POS → acquirer 20s).
const posTimeout = 20 * time.Second

// acquirerClient sends authorization requests to the merchant's acquirer,
// which forwards them to the card network.
type acquirerClient struct {
	url  string
	http *http.Client
}

func newAcquirerClient(url string) *acquirerClient {
	return &acquirerClient{url: url, http: &http.Client{Timeout: posTimeout}}
}

func (c *acquirerClient) authorize(ctx context.Context, req iso8583.AuthRequest) (iso8583.AuthResponse, error) {
	var resp iso8583.AuthResponse
	return resp, c.post(ctx, "/authorize", req, &resp)
}

// reverse sends an 0420 reversal advice and returns the 0430.
func (c *acquirerClient) reverse(ctx context.Context, adv iso8583.ReversalAdvice) (iso8583.ReversalResponse, error) {
	var resp iso8583.ReversalResponse
	return resp, c.post(ctx, "/reverse", adv, &resp)
}

// authenticate3DS asks the acquirer's 3-D Secure server to authenticate an
// online shopper with the card's issuer.
func (c *acquirerClient) authenticate3DS(ctx context.Context, r iso8583.ThreeDSRequest) (iso8583.ThreeDSResult, error) {
	var res iso8583.ThreeDSResult
	return res, c.post(ctx, "/3ds/authenticate", r, &res)
}

// challenge3DS sends the shopper's one-time code.
func (c *acquirerClient) challenge3DS(ctx context.Context, ch iso8583.ThreeDSChallenge) (iso8583.ThreeDSResult, error) {
	var res iso8583.ThreeDSResult
	return res, c.post(ctx, "/3ds/challenge", ch, &res)
}

// closeBatch sends an 0500 batch close and returns the 0510.
func (c *acquirerClient) closeBatch(ctx context.Context, rq iso8583.Reconciliation) (iso8583.ReconciliationResponse, error) {
	var resp iso8583.ReconciliationResponse
	return resp, c.post(ctx, "/batch/close", rq, &resp)
}

// httpError is a non-200 answer from the acquirer.
type httpError struct {
	code int
	body string
}

func (e *httpError) Error() string {
	return fmt.Sprintf("acquirer returned HTTP %d %s", e.code, e.body)
}

func (c *acquirerClient) post(ctx context.Context, path string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(httpReq)
	if err != nil {
		return fmt.Errorf("acquirer unreachable at %s", c.url)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 200))
		return &httpError{code: res.StatusCode, body: strings.TrimSpace(string(msg))}
	}
	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		return fmt.Errorf("unreadable acquirer response: %v", err)
	}
	return nil
}

// outcome turns an 0110 into the status and message the cashier sees.
func outcome(req iso8583.AuthRequest, resp iso8583.AuthResponse) (string, string) {
	switch {
	case iso8583.IsBalanceInquiry(req.ProcessingCode) && iso8583.IsApproved(resp.ResponseCode):
		for _, a := range resp.AdditionalAmounts {
			if a.Type == iso8583.AmountAvailable {
				return "BALANCE", fmt.Sprintf("Available balance: %s. Nothing was charged or held.", cents(a.Amount))
			}
		}
		return "BALANCE", "The issuer answered but sent no balance."
	case iso8583.IsRefund(req.ProcessingCode) && iso8583.IsApproved(resp.ResponseCode):
		return "REFUND", fmt.Sprintf("Refund approved: %s goes back to the card when the refund clears. Auth code %s.", cents(resp.Amount), resp.AuthCode)
	case resp.ResponseCode == iso8583.RCPartialApproval:
		rest := float64(req.Amount-resp.Amount) / 100
		return "PARTIAL", fmt.Sprintf("Partially approved: %.2f approved, collect %.2f another way. Auth code %s.",
			float64(resp.Amount)/100, rest, resp.AuthCode)
	case req.AuthType == iso8583.AuthEstimated && iso8583.IsApproved(resp.ResponseCode):
		return "APPROVED", fmt.Sprintf("Pre-authorized: %s is held. Add to it if the bill grows, then capture the final amount. Auth code %s.",
			cents(resp.Amount), resp.AuthCode)
	case iso8583.IsApproved(resp.ResponseCode):
		return "APPROVED", "Approved. Auth code " + resp.AuthCode + "."
	default:
		return "DECLINED", fmt.Sprintf("Declined: %s %s.", resp.ResponseCode, resp.ResponseText)
	}
}
