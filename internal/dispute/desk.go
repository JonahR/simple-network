package dispute

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/JonahR/simple-network/internal/jsonapi"
)

// RegisterDesk gives a member's back office its own dispute desk: the
// page's /api/disputes calls go to the network's members' dispute system
// as this member (side is ActorIssuer or ActorAcquirer), so the bank sees
// only its own cases and makes only its own side's moves. The page uses the
// shared widget in internal/ui (disputes.js).
func RegisterDesk(mux *http.ServeMux, networkURL, side, member string) {
	base := MemberURL(networkURL, side, member)
	client := &http.Client{Timeout: 10 * time.Second}
	forward := func(target string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			// Check the caller's own Content-Type before relabelling the
			// body as JSON, or a cross-site form post would pass through.
			if r.Method == http.MethodPost && !jsonapi.Require(w, r) {
				return
			}
			t := target
			for _, k := range []string{"id", "act"} {
				t = strings.ReplaceAll(t, "{"+k+"}", url.PathEscape(r.PathValue(k)))
			}
			if r.URL.RawQuery != "" {
				t += "?" + r.URL.RawQuery
			}
			req, err := http.NewRequestWithContext(r.Context(), r.Method, t, http.MaxBytesReader(w, r.Body, 1<<16))
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			req.Header.Set("Content-Type", "application/json")
			resp, err := client.Do(req)
			if err != nil {
				http.Error(w, "network unreachable: "+err.Error(), http.StatusBadGateway)
				return
			}
			defer resp.Body.Close()
			w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
			w.WriteHeader(resp.StatusCode)
			io.Copy(w, resp.Body)
		}
	}
	mux.HandleFunc("GET /api/disputes", forward(base))
	mux.HandleFunc("POST /api/disputes/{id}/{act}", forward(base+"/{id}/{act}"))
	if side == ActorIssuer {
		mux.HandleFunc("GET /api/disputes/eligible", forward(base+"/eligible"))
		mux.HandleFunc("POST /api/disputes", forward(base))
	}
}

// MemberURL is a member's address in the network's members' dispute system.
func MemberURL(networkURL, side, member string) string {
	return strings.TrimSuffix(networkURL, "/") + "/api/members/" + side + "/" + url.PathEscape(member) + "/disputes"
}

// Member is an issuer's own connection to the members' dispute system, for
// the bank's servers rather than its back-office page: online banking files
// a chargeback with it when a cardholder disputes a charge.
type Member struct {
	URL    string // MemberURL
	Client *http.Client
}

// NewIssuer connects an issuer to the network's dispute system.
func NewIssuer(networkURL, bankID string) *Member {
	return &Member{URL: MemberURL(networkURL, ActorIssuer, bankID), Client: &http.Client{Timeout: 10 * time.Second}}
}

// Eligible asks whether the network would accept a chargeback. It returns
// the network's reason for refusing, or "".
func (m *Member) Eligible(ctx context.Context, networkTxnID, reasonCode string, amount int64) (string, error) {
	q := url.Values{"txn": {networkTxnID}, "reason": {reasonCode}, "amount": {strconv.FormatInt(amount, 10)}}
	var out struct {
		Refusal string `json:"refusal"`
	}
	err := m.do(ctx, http.MethodGet, m.URL+"/eligible?"+q.Encode(), nil, &out)
	return out.Refusal, err
}

// File files a chargeback.
func (m *Member) File(ctx context.Context, networkTxnID, reasonCode string, amount int64, note string) (Dispute, error) {
	var out Dispute
	err := m.do(ctx, http.MethodPost, m.URL, map[string]any{"network_txn_id": networkTxnID, "reason": reasonCode, "amount": amount, "note": note}, &out)
	return out, err
}

func (m *Member) do(ctx context.Context, method, target string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			return &RefusedError{e.Error}
		}
		return fmt.Errorf("dispute system: %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// RefusedError is the network refusing a dispute action, in its words.
type RefusedError struct{ Reason string }

func (e *RefusedError) Error() string { return e.Reason }
