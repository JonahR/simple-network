package clearing

import (
	"context"
	"testing"

	"github.com/JonahR/simple-network/internal/card"
)

// A linked refund carries the sale's transaction ID (from the network's
// record, not the acquirer's) and its ARN, and can be found from the sale.
func TestLinkedRefundCarriesTheSale(t *testing.T) {
	a := auths{
		"sale":   {AcquirerID: "A1", IssuerID: "I1", Approved: true, Amount: 10_000, Currency: "840", Product: card.Credit},
		"refund": {AcquirerID: "A1", IssuerID: "I1", Approved: true, Amount: 4_000, Currency: "840", Product: card.Credit, ProcessingCode: "200000", OriginalTxnID: "sale"},
	}
	e, iss, _ := newEngine(DefaultFees(), a, []string{"I1"}, []string{"A1"})
	e.Receive(NewFile(FileHeader{FileID: "f1", AcquirerID: "A1", Sequence: 1}, []Presentment{pres("sale", 10_000)}))

	r := pres("refund", 4_000)
	r.ProcessingCode, r.OriginalTxnID, r.OriginalARN = "200000", "spoofed", "spoofed"
	if ack := e.Receive(NewFile(FileHeader{FileID: "f2", AcquirerID: "A1", Sequence: 2}, []Presentment{r})); ack.Accepted != 1 {
		t.Fatalf("ack %+v", ack)
	}
	it, ok := e.ClearedItem("refund")
	sale, _ := e.ClearedItem("sale")
	if !ok || !it.Credit || it.OriginalTxnID != "sale" || it.OriginalARN != sale.ARN || sale.ARN == "" {
		t.Fatalf("refund item %+v; sale ARN %q", it, sale.ARN)
	}
	if got := e.Refunds("sale"); len(got) != 1 || got[0].Amount != 4_000 {
		t.Errorf("Refunds(sale) = %+v", got)
	}
	if _, ok := e.ClearedItem("nope"); ok {
		t.Error("ClearedItem found an unknown transaction")
	}

	e.Close(context.Background())
	for _, rec := range iss["I1"].files[0].Records {
		if rec.NetworkTxnID == "refund" && (!rec.Credit || rec.OriginalTxnID != "sale") {
			t.Errorf("issuer record %+v", rec)
		}
	}
}
