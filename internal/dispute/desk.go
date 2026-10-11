package dispute

import (
	"io"
	"net/http"
	"net/url"
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
	base := strings.TrimSuffix(networkURL, "/") + "/api/members/" + side + "/" + url.PathEscape(member) + "/disputes"
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
