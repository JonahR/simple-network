package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/JonahR/simple-network/internal/iso8583"
)

// posTimeout is how long the terminal waits for an answer (D8: POS → acquirer 20s).
const posTimeout = 20 * time.Second

// networkClient sends authorization requests to the card network.
type networkClient struct {
	url  string
	http *http.Client
}

func newNetworkClient(url string) *networkClient {
	return &networkClient{url: url, http: &http.Client{Timeout: posTimeout}}
}

func (c *networkClient) authorize(ctx context.Context, req iso8583.AuthRequest) (iso8583.AuthResponse, error) {
	var resp iso8583.AuthResponse
	body, err := json.Marshal(req)
	if err != nil {
		return resp, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url+"/authorize", bytes.NewReader(body))
	if err != nil {
		return resp, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(httpReq)
	if err != nil {
		return resp, fmt.Errorf("network unreachable at %s", c.url)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return resp, fmt.Errorf("network returned HTTP %d", res.StatusCode)
	}
	if err := json.NewDecoder(res.Body).Decode(&resp); err != nil {
		return resp, fmt.Errorf("unreadable network response: %v", err)
	}
	return resp, nil
}

// outcome turns an 0110 into the status and message the cashier sees.
func outcome(req iso8583.AuthRequest, resp iso8583.AuthResponse) (string, string) {
	switch {
	case resp.ResponseCode == iso8583.RCPartialApproval:
		rest := float64(req.Amount-resp.Amount) / 100
		return "PARTIAL", fmt.Sprintf("Partially approved: %.2f approved, collect %.2f another way. Auth code %s.",
			float64(resp.Amount)/100, rest, resp.AuthCode)
	case iso8583.IsApproved(resp.ResponseCode):
		return "APPROVED", "Approved. Auth code " + resp.AuthCode + "."
	default:
		return "DECLINED", fmt.Sprintf("Declined: %s %s.", resp.ResponseCode, resp.ResponseText)
	}
}
