package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/JonahR/simple-network/internal/clearing"
	"github.com/JonahR/simple-network/internal/dispute"
	"github.com/JonahR/simple-network/internal/iso8583"
)

// testSales holds cleared sales for the members' dispute system; it is also
// the dispute engine's source of facts and money.
type testSales map[string]clearing.Item

func (s testSales) ClearedSale(id string) (clearing.Item, string, bool) {
	it, ok := s[id]
	return it, "2026-10-10", ok
}

func (s testSales) ClearedSales() []clearing.Item {
	var out []clearing.Item
	for _, it := range s {
		out = append(out, it)
	}
	return out
}

func (s testSales) Facts(id string) (dispute.Facts, bool) {
	it, ok := s[id]
	return dispute.Facts{Sale: it, ClearedOn: "2026-10-10"}, ok
}

func (s testSales) AddDispute(clearing.DisputeMoney) (clearing.Item, error) {
	return clearing.Item{CycleID: "2026-10-12.1"}, nil
}

func (s testSales) BusinessDate() string { return "2026-10-12" }

// TestMemberDisputeDesks drives a dispute from the issuer's and acquirer's
// own desks and checks each member sees and moves only its own cases.
func TestMemberDisputeDesks(t *testing.T) {
	sales := testSales{}
	for id, issuer := range map[string]string{"fsb-sale": "FSB", "ucb-sale": "UCB"} {
		sales[id] = clearing.Item{Presentment: clearing.Presentment{NetworkTxnID: id, Amount: 5_000, Currency: "840",
			MerchantID: "M1", MerchantName: "Shop", EntryMode: iso8583.EntryManual, PAN: "424242******4242"},
			AcquirerID: "100001", IssuerID: issuer, Channel: clearing.CardNotPresent}
	}
	d := &dispute.Engine{Source: sales, Money: sales, Notifiers: map[string]dispute.Notifier{}, Now: time.Now}
	netMux := http.NewServeMux()
	registerMemberDisputes(netMux, d, sales, map[string]string{})
	network := httptest.NewServer(netMux)
	defer network.Close()

	desk := func(side, member string) *httptest.Server {
		mux := http.NewServeMux()
		dispute.RegisterDesk(mux, network.URL, side, member)
		return httptest.NewServer(mux)
	}
	fsb, ucb, acq := desk(dispute.ActorIssuer, "FSB"), desk(dispute.ActorIssuer, "UCB"), desk(dispute.ActorAcquirer, "100001")
	defer fsb.Close()
	defer ucb.Close()
	defer acq.Close()

	post := func(srv *httptest.Server, path string, body any) (int, map[string]any) {
		t.Helper()
		b, _ := json.Marshal(body)
		res, err := http.Post(srv.URL+path, "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		out := map[string]any{}
		json.NewDecoder(res.Body).Decode(&out)
		return res.StatusCode, out
	}
	cases := func(srv *httptest.Server) (n, sales int) {
		t.Helper()
		res, err := http.Get(srv.URL + "/api/disputes")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var v struct {
			Desk  struct{ Disputes []dispute.Dispute }
			Sales []clearing.Item
		}
		json.NewDecoder(res.Body).Decode(&v)
		return len(v.Desk.Disputes), len(v.Sales)
	}
	refused := func(name string, status int, out map[string]any) {
		t.Helper()
		if status == http.StatusOK {
			t.Errorf("%s: allowed, want refused (%v)", name, out)
		}
	}
	ok := func(name string, status int, out map[string]any, state string) {
		t.Helper()
		if status != http.StatusOK || out["state"] != state {
			t.Fatalf("%s: status %d %v, want state %s", name, status, out, state)
		}
	}

	if _, n := cases(fsb); n != 1 {
		t.Errorf("FSB sees %d sales to dispute, want only its own 1", n)
	}
	s, out := post(ucb, "/api/disputes", map[string]any{"network_txn_id": "fsb-sale", "reason": "10.4", "amount": 5_000})
	refused("UCB files on an FSB card", s, out)
	s, out = post(acq, "/api/disputes", map[string]any{"network_txn_id": "fsb-sale", "reason": "10.4", "amount": 5_000})
	refused("the acquirer files", s, out)

	s, out = post(fsb, "/api/disputes", map[string]any{"network_txn_id": "fsb-sale", "reason": "10.4", "amount": 5_000})
	ok("FSB files", s, out, dispute.StateChargeback)
	id := out["id"].(string)
	if n, _ := cases(ucb); n != 0 {
		t.Errorf("UCB sees %d of FSB's disputes", n)
	}
	if n, _ := cases(acq); n != 1 {
		t.Errorf("the acquirer sees %d disputes, want 1", n)
	}

	s, out = post(ucb, "/api/disputes/"+id+"/accept", nil)
	refused("UCB acts on FSB's case", s, out)
	s, out = post(fsb, "/api/disputes/"+id+"/represent", map[string]string{"evidence": "compelling_evidence"})
	refused("the issuer represents", s, out)
	s, out = post(fsb, "/api/disputes/"+id+"/accept", nil)
	refused("the issuer accepts on the acquirer's turn", s, out)

	s, out = post(acq, "/api/disputes/"+id+"/represent", map[string]string{"evidence": "compelling_evidence"})
	ok("the acquirer represents", s, out, dispute.StateRepresented)
	s, out = post(acq, "/api/disputes/"+id+"/prearbitrate", nil)
	refused("the acquirer goes to pre-arbitration", s, out)
	s, out = post(fsb, "/api/disputes/"+id+"/prearbitrate", map[string]string{"rebuttal": "new_device"})
	ok("FSB goes to pre-arbitration", s, out, dispute.StatePreArbitration)
	s, out = post(acq, "/api/disputes/"+id+"/arbitrate", nil)
	ok("the acquirer asks for arbitration", s, out, dispute.StateClosed)
	if out["outcome"] != dispute.OutcomeMerchantLiable {
		t.Errorf("outcome %v, want %s: the rebuttal answers the evidence", out["outcome"], dispute.OutcomeMerchantLiable)
	}
}
