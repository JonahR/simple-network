// Package jsonapi holds checks shared by the services' JSON endpoints.
package jsonapi

import (
	"mime"
	"net/http"
)

// Require answers 415 and returns false unless r's body is declared as JSON.
// A web page on another site can POST a form or text/plain body to these
// local services without the browser asking first; a JSON body makes the
// browser send a CORS preflight, which these services never approve. So
// every state-changing JSON endpoint should check this first.
func Require(w http.ResponseWriter, r *http.Request) bool {
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
		return false
	}
	return true
}
