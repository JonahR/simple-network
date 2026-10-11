package main

import (
	"net/http"
	"strings"
)

// The cardholder's online banking: what a customer of this bank sees when
// they sign in. It shows the same accounts as the back office, from the
// customer's side: limit, balance, pending and posted transactions, and a
// switch to lock the card. The page is at /banking/.

func (s *bankServer) handleCustomers(w http.ResponseWriter, _ *http.Request) {
	banks := make([]bankLink, len(s.banks))
	for i, b := range s.banks {
		banks[i] = b
		banks[i].URL = strings.TrimSuffix(b.URL, "/") + "/banking/"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"bank":      map[string]string{"id": s.bank.ID, "name": s.bank.Name},
		"banks":     banks,
		"customers": s.bank.Customers(),
	})
}

func (s *bankServer) handleStatement(w http.ResponseWriter, r *http.Request) {
	st, ok := s.bank.Statement(r.PathValue("id"))
	if !ok {
		http.Error(w, "account not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, st)
}
