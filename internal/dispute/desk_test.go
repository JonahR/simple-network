package dispute

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The desk relabels what it forwards as JSON, so it must refuse a caller
// whose body isn't JSON: otherwise a cross-site form post would reach the
// network as a member's dispute action.
func TestDeskRefusesNonJSONPosts(t *testing.T) {
	reached := false
	network := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	}))
	defer network.Close()
	mux := http.NewServeMux()
	RegisterDesk(mux, network.URL, ActorIssuer, "FSB")

	post := func(ct string) int {
		r := httptest.NewRequest(http.MethodPost, "/api/disputes", strings.NewReader(`{"reason":"10.4"}`))
		if ct != "" {
			r.Header.Set("Content-Type", ct)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code
	}
	if code := post("text/plain"); code != http.StatusUnsupportedMediaType || reached {
		t.Errorf("text/plain: status %d, reached network %v", code, reached)
	}
	if code := post("application/json"); code != http.StatusOK || !reached {
		t.Errorf("JSON: status %d, reached network %v", code, reached)
	}
}
