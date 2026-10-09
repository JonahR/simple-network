package network

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/JonahR/simple-network/internal/iso8583"
)

// HTTPIssuer is an IssuerClient that talks JSON over HTTP to an issuer at BaseURL.
type HTTPIssuer struct {
	BaseURL string
	Client  *http.Client
}

func (h *HTTPIssuer) Authorize(ctx context.Context, req iso8583.AuthRequest) (iso8583.AuthResponse, error) {
	var resp iso8583.AuthResponse
	err := h.post(ctx, "/authorize", req, &resp)
	return resp, err
}

func (h *HTTPIssuer) Reverse(ctx context.Context, adv iso8583.ReversalAdvice) (iso8583.ReversalResponse, error) {
	var resp iso8583.ReversalResponse
	err := h.post(ctx, "/reverse", adv, &resp)
	return resp, err
}

func (h *HTTPIssuer) post(ctx context.Context, path string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := h.Client
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return ErrIssuerTimeout
		}
		// Connection refused or reset: the request never got a response.
		return fmt.Errorf("%w: %v", ErrIssuerUnavailable, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: HTTP %d", ErrIssuerUnavailable, res.StatusCode)
	}
	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return ErrIssuerTimeout
		}
		return fmt.Errorf("decoding issuer response: %w", err)
	}
	return nil
}
