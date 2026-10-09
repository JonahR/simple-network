package clearing

import (
	"fmt"
	"sort"
	"time"
)

// Ledger accounts, from the network's point of view.
//
//	due_from:<issuer>    asset: what an issuer owes the network
//	due_to:<acquirer>    liability: what the network owes an acquirer
//	revenue:network      network fee income
//	cash:settlement      the network's account at the settlement bank
func DueFrom(member string) string { return "due_from:" + member }
func DueTo(member string) string   { return "due_to:" + member }

const (
	AccountRevenue = "revenue:network"
	AccountCash    = "cash:settlement"
)

// Line is one side of a journal entry.
type Line struct {
	Account string `json:"account"`
	Debit   int64  `json:"debit,omitempty"`
	Credit  int64  `json:"credit,omitempty"`
}

// Entry is a balanced journal entry in one currency. Entries are only ever
// appended; a correction is a new entry (D6).
type Entry struct {
	Seq      int       `json:"seq"`
	Time     time.Time `json:"time"`
	CycleID  string    `json:"cycle_id"`
	Ref      string    `json:"ref"` // Network transaction ID or settlement transfer
	Memo     string    `json:"memo"`
	Currency string    `json:"currency"`
	Lines    []Line    `json:"lines"`
}

// Ledger is an append-only double-entry journal.
type Ledger struct {
	entries []Entry
}

// Post appends an entry after checking that debits equal credits.
func (l *Ledger) Post(e Entry) error {
	var dr, cr int64
	for _, ln := range e.Lines {
		if ln.Debit < 0 || ln.Credit < 0 {
			return fmt.Errorf("entry %q: negative amount on %s", e.Memo, ln.Account)
		}
		dr += ln.Debit
		cr += ln.Credit
	}
	if dr != cr {
		return fmt.Errorf("entry %q does not balance: debits %d, credits %d", e.Memo, dr, cr)
	}
	e.Seq = len(l.entries) + 1
	l.entries = append(l.entries, e)
	return nil
}

// Balance is an account's debits minus credits in one currency.
type Balance struct {
	Account  string `json:"account"`
	Currency string `json:"currency"`
	Debit    int64  `json:"debit"`
	Credit   int64  `json:"credit"`
	Net      int64  `json:"net"` // Debit - Credit
}

// Balances returns every account's balance, sorted by account.
func (l *Ledger) Balances() []Balance {
	byKey := map[[2]string]*Balance{}
	for _, e := range l.entries {
		for _, ln := range e.Lines {
			k := [2]string{ln.Account, e.Currency}
			b := byKey[k]
			if b == nil {
				b = &Balance{Account: ln.Account, Currency: e.Currency}
				byKey[k] = b
			}
			b.Debit += ln.Debit
			b.Credit += ln.Credit
			b.Net = b.Debit - b.Credit
		}
	}
	out := make([]Balance, 0, len(byKey))
	for _, b := range byKey {
		out = append(out, *b)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Account != out[j].Account {
			return out[i].Account < out[j].Account
		}
		return out[i].Currency < out[j].Currency
	})
	return out
}

// Entries returns up to n entries, newest first.
func (l *Ledger) Entries(n int) []Entry {
	out := []Entry{}
	for i := len(l.entries) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, l.entries[i])
	}
	return out
}

// Len is the number of entries.
func (l *Ledger) Len() int { return len(l.entries) }
