package issuer

import (
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/JonahR/simple-network/internal/card"
	"github.com/JonahR/simple-network/internal/clearing"
)

// maxPostings caps the posted transactions kept per bank.
const maxPostings = 2_000

// Posting is a transaction clearing posted to an account: a charge for its
// final amount, or a refund credited back. It is what the cardholder sees
// on their statement once a pending transaction settles.
type Posting struct {
	Time         time.Time `json:"time"`      // When the bank posted it
	AuthTime     time.Time `json:"auth_time"` // When it was authorized, if known
	AccountID    string    `json:"account_id"`
	NetworkTxnID string    `json:"network_txn_id"`
	ARN          string    `json:"arn"`
	Merchant     string    `json:"merchant"`
	MCC          string    `json:"mcc"`
	Currency     string    `json:"currency"`
	Amount       int64     `json:"amount"` // Positive
	Credit       bool      `json:"credit"` // A refund: money back to the card
}

// recordPosting adds a posted line to the account's history. The caller holds b.mu.
func (b *Bank) recordPosting(acct *Account, r clearing.IssuerRecord) {
	p := Posting{
		Time: b.now().UTC(), AccountID: acct.ID, NetworkTxnID: r.NetworkTxnID, ARN: r.ARN,
		Merchant: r.MerchantName, Currency: r.Currency, Amount: r.Amount, Credit: r.Credit,
	}
	for _, d := range b.decisions {
		if d.NetworkTxnID == r.NetworkTxnID {
			p.AuthTime, p.MCC = d.Time, d.MCC
			if p.Merchant == "" {
				p.Merchant = d.Merchant
			}
		}
	}
	b.postings = append(b.postings, p)
	if len(b.postings) > maxPostings {
		b.postings = b.postings[1:]
	}
}

// Activity is one line in the cardholder's view of an account. Amount is
// what it does to the balance: positive for a charge, negative for a credit.
type Activity struct {
	Time         time.Time  `json:"time"`             // Authorized
	Posted       *time.Time `json:"posted,omitempty"` // Posted by clearing; nil while pending
	NetworkTxnID string     `json:"network_txn_id"`
	Merchant     string     `json:"merchant"`
	MCC          string     `json:"mcc,omitempty"`
	Currency     string     `json:"currency"`
	Amount       int64      `json:"amount"`
	Kind         string     `json:"kind"`                // "purchase", "refund", or "dispute" (a provisional credit)
	AuthCode     string     `json:"auth_code,omitempty"` // Pending lines only
	Note         string     `json:"note,omitempty"`      // e.g. "includes added authorizations"
}

// Statement is what the cardholder sees for one account.
type Statement struct {
	Bank    string      `json:"bank"`
	Account AccountView `json:"account"`
	Pending []Activity  `json:"pending"` // Newest first
	Posted  []Activity  `json:"posted"`  // Newest first
	// Balance is what the cardholder owes on a credit account, or what is
	// left on a debit, prepaid or HSA/FSA account, from posted lines only.
	Balance     int64 `json:"balance"`
	PendingNet  int64 `json:"pending_net"`  // Holds minus pending refunds
	PendingRefs int64 `json:"pending_refs"` // Refunds approved but not yet posted
}

// Customer is a cardholder and their accounts at this bank.
type Customer struct {
	Holder   string        `json:"holder"`
	Accounts []AccountView `json:"accounts"`
}

// view builds an account's display copy. The caller holds b.mu.
func (b *Bank) view(a *Account) AccountView {
	v := AccountView{
		ID: a.ID, PAN: card.Mask(a.PAN), Holder: a.Holder, Product: string(a.Product), Expiry: yymm(a.Expiry),
		HasPIN: a.PINHash != "", Blocked: a.Blocked, Limit: a.Limit, Available: a.Available, Posted: a.Posted,
	}
	for _, h := range b.holds {
		if h.pan == a.PAN {
			v.Held += h.amount
			v.Holds++
		}
	}
	return v
}

// Customers lists the bank's cardholders, in the order their first account
// was opened, each with their accounts.
func (b *Bank) Customers() []Customer {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := []Customer{}
	index := map[string]int{}
	for _, pan := range b.order {
		a := b.accounts[pan]
		i, ok := index[a.Holder]
		if !ok {
			i = len(out)
			index[a.Holder] = i
			out = append(out, Customer{Holder: a.Holder, Accounts: []AccountView{}})
		}
		out[i].Accounts = append(out[i].Accounts, b.view(a))
	}
	return out
}

// Statement returns the cardholder's view of an account: pending lines are
// live holds (with any incremental authorizations folded in) and refunds
// approved but not yet posted; posted lines are what clearing posted.
func (b *Bank) Statement(accountID string) (Statement, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var acct *Account
	for _, a := range b.accounts {
		if a.ID == accountID {
			acct = a
		}
	}
	if acct == nil {
		return Statement{}, false
	}
	st := Statement{Bank: b.Name, Account: b.view(acct), Pending: []Activity{}, Posted: []Activity{}}

	byTxn := map[string]Decision{}
	for _, d := range b.decisions {
		byTxn[d.NetworkTxnID] = d
	}
	for id, h := range b.holds {
		if h.pan != acct.PAN {
			continue
		}
		d := byTxn[id]
		line := Activity{Time: d.Time, NetworkTxnID: id, Merchant: d.Merchant, MCC: d.MCC, Currency: d.Currency,
			Amount: h.amount, Kind: "purchase", AuthCode: d.AuthCode}
		if h.amount != d.Approved && d.Approved > 0 {
			line.Note = "includes added authorizations"
		}
		st.Pending = append(st.Pending, line)
		st.PendingNet += h.amount
	}
	for _, d := range b.decisions {
		if d.AccountID == acct.ID && d.Hold == "credit pending" {
			st.Pending = append(st.Pending, Activity{Time: d.Time, NetworkTxnID: d.NetworkTxnID, Merchant: d.Merchant, MCC: d.MCC,
				Currency: d.Currency, Amount: -d.Approved, Kind: "refund", AuthCode: d.AuthCode})
			st.PendingNet -= d.Approved
			st.PendingRefs += d.Approved
		}
	}
	var disputeCredits int64
	for _, p := range b.postings {
		if p.AccountID != acct.ID {
			continue
		}
		posted := p.Time
		line := Activity{Time: p.AuthTime, Posted: &posted, NetworkTxnID: p.NetworkTxnID, Merchant: p.Merchant, MCC: p.MCC,
			Currency: p.Currency, Amount: p.Amount, Kind: "purchase"}
		if line.Time.IsZero() {
			line.Time = p.Time
		}
		if p.Credit {
			line.Amount, line.Kind = -p.Amount, "refund"
		}
		d := byTxn[p.NetworkTxnID]
		if !p.Credit && d.Dispute != "" {
			line.Note = "Disputed: " + d.Dispute
		}
		st.Posted = append(st.Posted, line)
		// A chargeback the cardholder is pursuing (or won) credits them back
		// for now; the dispute's outcome can take it away again.
		if !p.Credit && d.DisputeCredit > 0 {
			st.Posted = append(st.Posted, Activity{Time: posted, Posted: &posted, NetworkTxnID: p.NetworkTxnID, Merchant: p.Merchant,
				Currency: p.Currency, Amount: -d.DisputeCredit, Kind: "dispute", Note: "Provisional credit while your dispute is open, kept if you win"})
			disputeCredits += d.DisputeCredit
		}
	}
	newestFirst := func(l []Activity) {
		sort.SliceStable(l, func(i, j int) bool { return l[i].Time.After(l[j].Time) })
	}
	newestFirst(st.Pending)
	slices.Reverse(st.Posted) // Postings are kept oldest first
	st.Balance = acct.Posted - disputeCredits
	if acct.Product != card.Credit && acct.Product != card.Fleet {
		st.Balance = acct.Limit - acct.Posted + disputeCredits // What is left on the account
	}
	for i := range st.Pending {
		st.Pending[i].Merchant = strings.TrimSpace(st.Pending[i].Merchant)
	}
	return st, true
}
