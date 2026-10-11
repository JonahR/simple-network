package jsonapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequire(t *testing.T) {
	for _, tc := range []struct {
		ct   string
		want bool
	}{
		{"application/json", true},
		{"application/json; charset=utf-8", true},
		{"text/plain", false},
		{"application/x-www-form-urlencoded", false},
		{"", false},
	} {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}"))
		if tc.ct != "" {
			r.Header.Set("Content-Type", tc.ct)
		}
		w := httptest.NewRecorder()
		if got := Require(w, r); got != tc.want {
			t.Errorf("%q: got %v, want %v", tc.ct, got, tc.want)
		}
		if !tc.want && w.Code != http.StatusUnsupportedMediaType {
			t.Errorf("%q: status %d", tc.ct, w.Code)
		}
	}
}
