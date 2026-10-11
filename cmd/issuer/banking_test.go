package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/JonahR/simple-network/internal/issuer"
)

// Each bank's online banking knows which bank every customer signs in to, so
// a customer arriving at the wrong bank's site (John Smith, sent from the POS
// to First Simple Bank) can be sent to their own.
func TestBankingDirectory(t *testing.T) {
	fa, fe := issuer.FirstSimpleBankAccounts()
	ua, ue := issuer.UnionCardBankAccounts()
	links := []bankLink{{"FSB", "First Simple Bank", "http://fsb.test"}, {"UCB", "Union Card Bank", "http://ucb.test/"}}
	fsb := &bankServer{bank: issuer.NewBank("FSB", "First Simple Bank", nil, fa, fe), banks: links}
	ucb := &bankServer{bank: issuer.NewBank("UCB", "Union Card Bank", nil, ua, ue), banks: links}
	fsb.peers, ucb.peers = []*bankServer{fsb, ucb}, []*bankServer{fsb, ucb}

	rec := httptest.NewRecorder()
	fsb.handleCustomers(rec, httptest.NewRequest(http.MethodGet, "/api/banking/customers", nil))
	var out struct {
		Customers []issuer.Customer `json:"customers"`
		Directory map[string]string `json:"directory"`
		Banks     []bankLink        `json:"banks"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Customers) != 1 || out.Customers[0].Holder != "Jane Doe" {
		t.Errorf("FSB customers %+v; want only its own", out.Customers)
	}
	want := map[string]string{
		"Jane Doe":       "http://fsb.test/banking/",
		"John Smith":     "http://ucb.test/banking/",
		"Acme Logistics": "http://ucb.test/banking/",
	}
	for holder, url := range want {
		if out.Directory[holder] != url {
			t.Errorf("directory[%q] = %q, want %q", holder, out.Directory[holder], url)
		}
	}
	if out.Banks[1].URL != "http://ucb.test/banking/" {
		t.Errorf("bank switcher links to %q", out.Banks[1].URL)
	}
}
