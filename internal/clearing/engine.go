package clearing

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/JonahR/simple-network/internal/card"
)

// AuthInfo is what the network knows about an authorization, for matching
// a clearing record to it.
type AuthInfo struct {
	AcquirerID string
	IssuerID   string
	Approved   bool
	Amount     int64 // Approved amount
	Currency   string
	AuthCode   string
	Product    card.Product
	Reversed   bool // An 0420 or 0400 released the issuer's hold
}

// AuthSource looks up authorizations by network transaction ID.
type AuthSource interface {
	Lookup(networkTxnID string) (AuthInfo, bool)
}

// IssuerClient delivers an outgoing clearing file to an issuer.
type IssuerClient interface {
	Post(ctx context.Context, f IssuerFile) (IssuerAck, error)
}

// AcquirerClient talks to an acquirer at cutoff.
type AcquirerClient interface {
	// RequestFile asks the acquirer to send its clearing file now. It is a
	// simulation control: real acquirers send files on their own schedule.
	RequestFile(ctx context.Context) error
	// Advise sends the acquirer its settlement advice.
	Advise(ctx context.Context, a Advice) error
}

// Member roles.
const (
	RoleIssuer   = "issuer"
	RoleAcquirer = "acquirer"
	NetworkID    = "NETWORK"
)

// Cycle statuses.
const (
	CycleOpen    = "open"
	CycleClosed  = "closed"
	CycleSettled = "settled"
	CycleHeld    = "held" // The zero-sum check failed; no money moved
)

// Item is one cleared transaction with its fees.
type Item struct {
	Presentment
	CycleID    string  `json:"cycle_id"`
	AcquirerID string  `json:"acquirer_id"`
	IssuerID   string  `json:"issuer_id"`
	Product    string  `json:"product"`
	Channel    Channel `json:"channel"`
	Pricing
	IssuerOwes   int64  `json:"issuer_owes"`   // Amount − interchange + issuer fee
	AcquirerGets int64  `json:"acquirer_gets"` // Amount − interchange − acquirer fee
	Posting      string `json:"posting"`       // pending, posted, unmatched, or delivery failed
}

// FileSummary is a received clearing file and what came of it.
type FileSummary struct {
	FileID         string    `json:"file_id"`
	AcquirerID     string    `json:"acquirer_id"`
	Sequence       int       `json:"sequence"`
	Received       time.Time `json:"received"`
	Records        int       `json:"records"`
	HashTotal      int64     `json:"hash_total"`
	Status         string    `json:"status"`
	Reason         string    `json:"reason,omitempty"`
	Accepted       int       `json:"accepted"`
	AcceptedAmount int64     `json:"accepted_amount"`
	Rejected       int       `json:"rejected"`
}

// IssuerFileSummary is an outgoing file and whether the issuer posted it.
type IssuerFileSummary struct {
	FileID    string `json:"file_id"`
	IssuerID  string `json:"issuer_id"`
	Records   int    `json:"records"`
	Amount    int64  `json:"amount"`
	Status    string `json:"status"` // posted, or failed with a reason
	Unmatched int    `json:"unmatched"`
}

// Position is one member's obligations in one role and currency. Positive
// amounts are owed to the member; negative ones are owed by it.
type Position struct {
	MemberID    string `json:"member_id"`
	Name        string `json:"name"`
	Role        string `json:"role"`
	Currency    string `json:"currency"`
	Count       int    `json:"count"`
	Gross       int64  `json:"gross"`
	Interchange int64  `json:"interchange"`
	NetworkFees int64  `json:"network_fees"`
	Net         int64  `json:"net"`
}

// Transfer is one settlement payment through the settlement bank.
type Transfer struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Currency string `json:"currency"`
	Amount   int64  `json:"amount"`
	Memo     string `json:"memo"`
}

// Check is the zero-sum invariant for one currency: every member's net
// position plus network revenue must be exactly zero (D6, KT 05).
type Check struct {
	Currency string `json:"currency"`
	Members  int64  `json:"members"` // Sum of member net positions
	Revenue  int64  `json:"revenue"` // Network fee revenue
	Sum      int64  `json:"sum"`     // Members + revenue; must be 0
	OK       bool   `json:"ok"`
}

// Stage is one step of closing a cycle, for the end-of-day pipeline view.
type Stage struct {
	Name   string `json:"name"`
	Detail string `json:"detail"`
	OK     bool   `json:"ok"`
}

// AdviceStatus is whether an acquirer received its settlement advice.
type AdviceStatus struct {
	AcquirerID string `json:"acquirer_id"`
	Currency   string `json:"currency"`
	Net        int64  `json:"net"`
	Status     string `json:"status"` // delivered, or failed with a reason
}

// Cycle is one business day of clearing (D10).
type Cycle struct {
	ID           string              `json:"id"` // business date + cycle number
	BusinessDate string              `json:"business_date"`
	Number       int                 `json:"number"`
	ValueDate    string              `json:"value_date"` // When the money moves (T+1)
	Status       string              `json:"status"`
	Opened       time.Time           `json:"opened"`
	Closed       *time.Time          `json:"closed,omitempty"`
	Files        []FileSummary       `json:"files"`
	Items        []Item              `json:"items"`
	Rejected     []Rejection         `json:"rejected"`
	IssuerFiles  []IssuerFileSummary `json:"issuer_files"`
	Positions    []Position          `json:"positions"`
	Checks       []Check             `json:"checks"`
	Transfers    []Transfer          `json:"transfers"`
	Advices      []AdviceStatus      `json:"advices"`
	Stages       []Stage             `json:"stages"`
}

func (c *Cycle) clone() Cycle {
	out := *c
	out.Files = slices.Clone(c.Files)
	out.Items = slices.Clone(c.Items)
	out.Rejected = slices.Clone(c.Rejected)
	out.IssuerFiles = slices.Clone(c.IssuerFiles)
	out.Positions = slices.Clone(c.Positions)
	out.Checks = slices.Clone(c.Checks)
	out.Transfers = slices.Clone(c.Transfers)
	out.Advices = slices.Clone(c.Advices)
	out.Stages = slices.Clone(c.Stages)
	return out
}

// tolerances is how far over the authorized amount a clearing record may be,
// in basis points, by MCC (KT 05: tips, hotel and car-rental estimates).
var tolerances = map[string]int64{
	"5812": 2_000, // Restaurants: tips up to 20%
	"5813": 2_000, // Bars
	"7011": 1_500, // Hotels: final bill against the estimate
	"7512": 1_500, // Car rental
}

// Engine runs clearing and settlement for the network.
type Engine struct {
	Fees      FeeTable
	Auths     AuthSource
	Issuers   map[string]IssuerClient
	Acquirers map[string]AcquirerClient
	Names     map[string]string // Member display names by ID
	Now       func() time.Time
	// OpeningBalance funds each member's settlement-bank account, in minor
	// units of every currency, so the simulation can show money moving.
	OpeningBalance int64
	CallTimeout    time.Duration

	mu          sync.Mutex
	open        *Cycle
	history     []*Cycle // Newest last
	acks        map[string]Ack
	lastSeq     map[string]int
	cleared     map[string]string // Network transaction ID -> cycle ID
	ledger      Ledger
	bank        map[[2]string]int64 // (member, currency) -> settlement-bank balance
	undelivered []IssuerFile        // Issuer files to retry at the next close
}

func (e *Engine) init() {
	if e.open != nil {
		return
	}
	e.acks = map[string]Ack{}
	e.lastSeq = map[string]int{}
	e.cleared = map[string]string{}
	e.bank = map[[2]string]int64{}
	if e.CallTimeout == 0 {
		e.CallTimeout = 5 * time.Second
	}
	e.open = e.newCycle(e.Now().UTC().Format("2006-01-02"), 1)
}

func (e *Engine) newCycle(date string, n int) *Cycle {
	d, _ := time.Parse("2006-01-02", date)
	return &Cycle{
		ID:           fmt.Sprintf("%s.%d", date, n),
		BusinessDate: date,
		Number:       n,
		ValueDate:    d.AddDate(0, 0, 1).Format("2006-01-02"),
		Status:       CycleOpen,
		Opened:       e.Now().UTC(),
		Files:        []FileSummary{},
		Items:        []Item{},
		Rejected:     []Rejection{},
		IssuerFiles:  []IssuerFileSummary{},
		Positions:    []Position{},
		Checks:       []Check{},
		Transfers:    []Transfer{},
		Advices:      []AdviceStatus{},
		Stages:       []Stage{},
	}
}

func (e *Engine) name(id string) string {
	if n, ok := e.Names[id]; ok {
		return n
	}
	return id
}

// Receive ingests a clearing file into the open cycle and returns its
// acknowledgement. Ingest is idempotent: a file ID seen before gets its
// original acknowledgement back and changes nothing.
func (e *Engine) Receive(f File) Ack {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.init()

	if prev, ok := e.acks[f.Header.FileID]; ok {
		prev.Status = FileDuplicate
		return prev
	}
	c := e.open
	ack := Ack{FileID: f.Header.FileID, CycleID: c.ID, Rejected: []Rejection{}}
	summary := FileSummary{
		FileID: f.Header.FileID, AcquirerID: f.Header.AcquirerID, Sequence: f.Header.Sequence,
		Received: e.Now().UTC(), Records: len(f.Records), HashTotal: f.Trailer.HashTotal,
	}
	reject := func(reason string) Ack {
		ack.Status, ack.Reason = FileRejected, reason
		summary.Status, summary.Reason = FileRejected, reason
		c.Files = append(c.Files, summary)
		if f.Header.FileID != "" {
			e.acks[f.Header.FileID] = ack
		}
		return ack
	}
	if err := f.CheckControls(); err != nil {
		return reject(err.Error())
	}
	if _, ok := e.Acquirers[f.Header.AcquirerID]; !ok {
		return reject("unknown acquirer " + f.Header.AcquirerID)
	}
	if want := e.lastSeq[f.Header.AcquirerID] + 1; f.Header.Sequence != want {
		return reject(fmt.Sprintf("file sequence %d, expected %d", f.Header.Sequence, want))
	}
	e.lastSeq[f.Header.AcquirerID] = f.Header.Sequence

	for _, p := range f.Records {
		item, reason := e.match(c, f.Header.AcquirerID, p)
		if reason != "" {
			r := Rejection{NetworkTxnID: p.NetworkTxnID, ARN: p.ARN, Amount: p.Amount, Reason: reason}
			ack.Rejected = append(ack.Rejected, r)
			c.Rejected = append(c.Rejected, r)
			continue
		}
		c.Items = append(c.Items, item)
		e.cleared[p.NetworkTxnID] = c.ID
		e.postClearing(c, item)
		ack.Accepted++
		ack.AcceptedAmount += p.Amount
	}
	ack.Status = FileAccepted
	summary.Status, summary.Accepted, summary.AcceptedAmount, summary.Rejected = FileAccepted, ack.Accepted, ack.AcceptedAmount, len(ack.Rejected)
	c.Files = append(c.Files, summary)
	e.acks[f.Header.FileID] = ack
	return ack
}

// match checks one record against its authorization and prices it. It
// returns a rejection reason instead when the record can't be cleared.
func (e *Engine) match(c *Cycle, acquirerID string, p Presentment) (Item, string) {
	auth, ok := e.Auths.Lookup(p.NetworkTxnID)
	switch {
	case !ok:
		return Item{}, "no matching authorization"
	case !auth.Approved:
		return Item{}, "the authorization was declined"
	case auth.Reversed:
		return Item{}, "the authorization was reversed"
	case auth.AcquirerID != acquirerID:
		return Item{}, "the authorization belongs to another acquirer"
	case e.cleared[p.NetworkTxnID] != "":
		return Item{}, "already presented in cycle " + e.cleared[p.NetworkTxnID]
	case p.Currency != auth.Currency:
		return Item{}, "currency differs from the authorization"
	case p.Amount <= 0:
		return Item{}, "amount must be positive"
	case p.Amount > auth.Amount+BPS(auth.Amount, tolerances[p.MCC]):
		return Item{}, fmt.Sprintf("amount %d is over the authorized %d plus the tolerance for MCC %s", p.Amount, auth.Amount, p.MCC)
	}
	ch := ChannelOf(p.EntryMode)
	price := e.Fees.Price(auth.Product, ch, p.Amount, p.Cashback)
	return Item{
		Presentment:  p,
		CycleID:      c.ID,
		AcquirerID:   acquirerID,
		IssuerID:     auth.IssuerID,
		Product:      string(auth.Product),
		Channel:      ch,
		Pricing:      price,
		IssuerOwes:   p.Amount - price.Interchange + price.IssuerFee,
		AcquirerGets: p.Amount - price.Interchange - price.AcquirerFee,
		Posting:      "pending",
	}, ""
}

// postClearing records a cleared item in the ledger (KT 05's entry): the
// issuer owes the amount, the acquirer is owed it, interchange moves from the
// acquirer to the issuer, and each side pays its network fee.
func (e *Engine) postClearing(c *Cycle, it Item) {
	iss, acq := DueFrom(it.IssuerID), DueTo(it.AcquirerID)
	lines := []Line{
		{Account: iss, Debit: it.Amount},
		{Account: acq, Credit: it.Amount},
		{Account: acq, Debit: it.Interchange},
		{Account: iss, Credit: it.Interchange},
		{Account: acq, Debit: it.AcquirerFee},
		{Account: AccountRevenue, Credit: it.AcquirerFee},
		{Account: iss, Debit: it.IssuerFee},
		{Account: AccountRevenue, Credit: it.IssuerFee},
	}
	lines = slices.DeleteFunc(lines, func(l Line) bool { return l.Debit == 0 && l.Credit == 0 })
	if err := e.ledger.Post(Entry{Time: e.Now().UTC(), CycleID: c.ID, Ref: it.NetworkTxnID, Currency: it.Currency,
		Memo: fmt.Sprintf("Clearing %s: %s → %s, %s", it.ARN, it.AcquirerID, it.IssuerID, it.ProgramID), Lines: lines}); err != nil {
		panic(err) // The lines above balance by construction.
	}
}

// Run is the simulated end of day: it asks every acquirer to send its
// clearing file, then closes the cycle and settles it.
func (e *Engine) Run(ctx context.Context) (Cycle, error) {
	e.mu.Lock()
	e.init()
	acquirers := e.Acquirers
	e.mu.Unlock()
	var errs []string
	for _, id := range sortedKeys(acquirers) {
		cctx, cancel := context.WithTimeout(ctx, e.CallTimeout)
		if err := acquirers[id].RequestFile(cctx); err != nil {
			errs = append(errs, fmt.Sprintf("acquirer %s: %v", id, err))
		}
		cancel()
	}
	c := e.Close(ctx)
	if len(errs) > 0 {
		return c, fmt.Errorf("some clearing files were not sent: %v", errs)
	}
	return c, nil
}

// Close ends the open cycle at cutoff: it sends issuers their files, nets
// every member's position, checks the zero-sum invariant, settles through
// the settlement bank, and advises acquirers. Files that arrive meanwhile go
// into the next cycle, which opens immediately.
func (e *Engine) Close(ctx context.Context) Cycle {
	e.mu.Lock()
	e.init()
	c := e.open
	now := e.Now().UTC()
	c.Closed, c.Status = &now, CycleClosed
	next, _ := time.Parse("2006-01-02", c.BusinessDate)
	e.open = e.newCycle(next.AddDate(0, 0, 1).Format("2006-01-02"), c.Number+1)
	e.history = append(e.history, c)

	received := 0
	for _, f := range c.Files {
		received += f.Records
	}
	c.Stages = append(c.Stages,
		Stage{"Files received", fmt.Sprintf("%d file(s), %d record(s)", len(c.Files), received), true},
		Stage{"Matched to authorizations", fmt.Sprintf("%d cleared, %d rejected", len(c.Items), len(c.Rejected)), true},
	)
	var ic, fees int64
	for _, it := range c.Items {
		ic += it.Interchange
		fees += it.AcquirerFee + it.IssuerFee
	}
	c.Stages = append(c.Stages, Stage{"Priced", fmt.Sprintf("interchange %s, network fees %s (fee table %s)", minor(ic), minor(fees), e.Fees.Version), true})

	files := e.issuerFiles(c)
	issuers := e.Issuers
	e.mu.Unlock()

	// Deliver issuer files without holding the lock.
	type result struct {
		ack IssuerAck
		err error
	}
	results := make([]result, len(files))
	for i, f := range files {
		client, ok := issuers[f.IssuerID]
		if !ok {
			results[i].err = fmt.Errorf("no route to issuer %s", f.IssuerID)
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, e.CallTimeout)
		results[i].ack, results[i].err = client.Post(cctx, f)
		cancel()
	}

	e.mu.Lock()
	posted, failed := 0, 0
	for i, f := range files {
		s := IssuerFileSummary{FileID: f.FileID, IssuerID: f.IssuerID, Records: len(f.Records)}
		for _, r := range f.Records {
			s.Amount += r.Amount
		}
		status := "posted"
		if err := results[i].err; err != nil {
			s.Status = "failed: " + err.Error()
			status = "delivery failed"
			e.undelivered = append(e.undelivered, f)
			failed++
		} else {
			s.Status, s.Unmatched = "posted", len(results[i].ack.Unmatched)
			posted++
		}
		unmatched := map[string]bool{}
		for _, id := range results[i].ack.Unmatched {
			unmatched[id] = true
		}
		e.setPosting(f, status, unmatched)
		c.IssuerFiles = append(c.IssuerFiles, s)
	}
	c.Stages = append(c.Stages, Stage{"Issuer files", fmt.Sprintf("%d posted, %d failed (retried at next cutoff)", posted, failed), failed == 0})

	e.net(c)
	ok := true
	for _, chk := range c.Checks {
		ok = ok && chk.OK
	}
	if !ok {
		c.Status = CycleHeld
		c.Stages = append(c.Stages, Stage{"Zero-sum check", "positions and revenue do not sum to zero; settlement held", false})
		e.mu.Unlock()
		return c.clone()
	}
	c.Stages = append(c.Stages, Stage{"Zero-sum check", "member positions + network revenue = 0", true})
	e.settle(c)
	c.Status = CycleSettled
	c.Stages = append(c.Stages, Stage{"Settled", fmt.Sprintf("%d transfer(s), value date %s", len(c.Transfers), c.ValueDate), true})
	advices := e.advices(c)
	acquirers := e.Acquirers
	e.mu.Unlock()

	statuses := make([]AdviceStatus, len(advices))
	for i, a := range advices {
		statuses[i] = AdviceStatus{AcquirerID: a.AcquirerID, Currency: a.Currency, Net: a.Net, Status: "delivered"}
		cctx, cancel := context.WithTimeout(ctx, e.CallTimeout)
		if err := acquirers[a.AcquirerID].Advise(cctx, a); err != nil {
			statuses[i].Status = "failed: " + err.Error()
		}
		cancel()
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	c.Advices = statuses
	delivered := 0
	for _, s := range statuses {
		if s.Status == "delivered" {
			delivered++
		}
	}
	c.Stages = append(c.Stages, Stage{"Acquirers advised", fmt.Sprintf("%d of %d settlement advice(s) delivered; acquirers fund their merchants", delivered, len(statuses)), delivered == len(statuses)})
	return c.clone()
}

// issuerFiles groups the cycle's items by issuer, plus any files that
// failed to deliver at an earlier cutoff. The caller holds e.mu.
func (e *Engine) issuerFiles(c *Cycle) []IssuerFile {
	files := e.undelivered
	e.undelivered = nil
	byIssuer := map[string]*IssuerFile{}
	var order []string
	for _, it := range c.Items {
		f := byIssuer[it.IssuerID]
		if f == nil {
			f = &IssuerFile{FileID: c.ID + "-" + it.IssuerID, CycleID: c.ID, IssuerID: it.IssuerID}
			byIssuer[it.IssuerID] = f
			order = append(order, it.IssuerID)
		}
		f.Records = append(f.Records, IssuerRecord{
			NetworkTxnID: it.NetworkTxnID, ARN: it.ARN, Amount: it.Amount, Currency: it.Currency,
			MerchantName: it.MerchantName, Interchange: it.Interchange, NetworkFee: it.IssuerFee,
		})
	}
	sort.Strings(order)
	for _, id := range order {
		files = append(files, *byIssuer[id])
	}
	return files
}

// setPosting records the outcome of an issuer file on its items, in
// whichever cycle they belong to. The caller holds e.mu.
func (e *Engine) setPosting(f IssuerFile, status string, unmatched map[string]bool) {
	ids := map[string]bool{}
	for _, r := range f.Records {
		ids[r.NetworkTxnID] = true
	}
	for _, c := range e.history {
		if c.ID != f.CycleID {
			continue
		}
		for i := range c.Items {
			if !ids[c.Items[i].NetworkTxnID] {
				continue
			}
			c.Items[i].Posting = status
			if unmatched[c.Items[i].NetworkTxnID] {
				c.Items[i].Posting = "unmatched"
			}
		}
	}
}

// net computes positions per member, role, and currency, and the zero-sum
// check per currency. The caller holds e.mu.
func (e *Engine) net(c *Cycle) {
	type key struct{ member, role, cur string }
	pos := map[key]*Position{}
	get := func(member, role, cur string) *Position {
		k := key{member, role, cur}
		if pos[k] == nil {
			pos[k] = &Position{MemberID: member, Name: e.name(member), Role: role, Currency: cur}
		}
		return pos[k]
	}
	revenue := map[string]int64{}
	for _, it := range c.Items {
		iss := get(it.IssuerID, RoleIssuer, it.Currency)
		iss.Count++
		iss.Gross -= it.Amount
		iss.Interchange += it.Interchange
		iss.NetworkFees -= it.IssuerFee
		iss.Net -= it.IssuerOwes

		acq := get(it.AcquirerID, RoleAcquirer, it.Currency)
		acq.Count++
		acq.Gross += it.Amount
		acq.Interchange -= it.Interchange
		acq.NetworkFees -= it.AcquirerFee
		acq.Net += it.AcquirerGets

		revenue[it.Currency] += it.AcquirerFee + it.IssuerFee
	}
	c.Positions = c.Positions[:0]
	members := map[string]int64{}
	for _, p := range pos {
		c.Positions = append(c.Positions, *p)
		members[p.Currency] += p.Net
	}
	sort.Slice(c.Positions, func(i, j int) bool {
		a, b := c.Positions[i], c.Positions[j]
		if a.Role != b.Role {
			return a.Role < b.Role // acquirers, then issuers
		}
		return a.MemberID+a.Currency < b.MemberID+b.Currency
	})
	c.Checks = c.Checks[:0]
	for _, cur := range sortedKeys(revenue) {
		sum := members[cur] + revenue[cur]
		c.Checks = append(c.Checks, Check{Currency: cur, Members: members[cur], Revenue: revenue[cur], Sum: sum, OK: sum == 0})
	}
}

// settle nets each member's positions across roles into one payment per
// member and currency, and moves the money through the settlement bank:
// net payers fund the network's settlement account, which pays net
// receivers and keeps the network's fee revenue. The caller holds e.mu.
func (e *Engine) settle(c *Cycle) {
	net := map[[2]string]int64{}
	owes := map[[2]string]int64{} // As an issuer
	gets := map[[2]string]int64{} // As an acquirer
	var keys [][2]string
	for _, p := range c.Positions {
		k := [2]string{p.MemberID, p.Currency}
		if _, ok := net[k]; !ok {
			keys = append(keys, k)
		}
		net[k] += p.Net
		if p.Net < 0 {
			owes[k] -= p.Net
		} else {
			gets[k] += p.Net
		}
	}
	// A member that is both an issuer and an acquirer settles one net
	// amount, so offset what it is owed against what it owes first.
	for _, k := range keys {
		if off := min(owes[k], gets[k]); off > 0 {
			e.ledger.Post(Entry{Time: e.Now().UTC(), CycleID: c.ID, Ref: "netting:" + k[0], Currency: k[1],
				Memo:  fmt.Sprintf("Netting: %s's acquirer credit offsets its issuer debit", e.name(k[0])),
				Lines: []Line{{Account: DueTo(k[0]), Debit: off}, {Account: DueFrom(k[0]), Credit: off}}})
		}
	}
	sort.Slice(keys, func(i, j int) bool { return net[keys[i]] < net[keys[j]] }) // Payers first
	for _, k := range keys {
		member, cur, amt := k[0], k[1], net[k]
		e.fund(member, cur)
		e.fund(NetworkID, cur)
		switch {
		case amt < 0:
			c.Transfers = append(c.Transfers, Transfer{From: member, To: NetworkID, Currency: cur, Amount: -amt, Memo: "Net settlement payment"})
			e.bank[[2]string{member, cur}] += amt
			e.bank[[2]string{NetworkID, cur}] -= amt
			e.ledger.Post(Entry{Time: e.Now().UTC(), CycleID: c.ID, Ref: "settlement:" + member, Currency: cur,
				Memo:  fmt.Sprintf("Settlement: %s pays the network", e.name(member)),
				Lines: []Line{{Account: AccountCash, Debit: -amt}, {Account: DueFrom(member), Credit: -amt}}})
		case amt > 0:
			c.Transfers = append(c.Transfers, Transfer{From: NetworkID, To: member, Currency: cur, Amount: amt, Memo: "Net settlement credit"})
			e.bank[[2]string{member, cur}] += amt
			e.bank[[2]string{NetworkID, cur}] -= amt
			e.ledger.Post(Entry{Time: e.Now().UTC(), CycleID: c.ID, Ref: "settlement:" + member, Currency: cur,
				Memo:  fmt.Sprintf("Settlement: the network pays %s", e.name(member)),
				Lines: []Line{{Account: DueTo(member), Debit: amt}, {Account: AccountCash, Credit: amt}}})
		}
	}
}

// fund opens a settlement-bank account with the opening balance. The caller holds e.mu.
func (e *Engine) fund(member, cur string) {
	k := [2]string{member, cur}
	if _, ok := e.bank[k]; !ok {
		e.bank[k] = e.OpeningBalance
	}
}

// advices builds each acquirer's settlement advice. The caller holds e.mu.
func (e *Engine) advices(c *Cycle) []Advice {
	byKey := map[[2]string]*Advice{}
	var keys [][2]string
	for _, it := range c.Items {
		k := [2]string{it.AcquirerID, it.Currency}
		a := byKey[k]
		if a == nil {
			a = &Advice{CycleID: c.ID, ValueDate: c.ValueDate, AcquirerID: it.AcquirerID, Currency: it.Currency}
			byKey[k] = a
			keys = append(keys, k)
		}
		a.Gross += it.Amount
		a.Net += it.AcquirerGets
		a.Items = append(a.Items, AdviceItem{
			NetworkTxnID: it.NetworkTxnID, ARN: it.ARN, MerchantID: it.MerchantID, Amount: it.Amount,
			Interchange: it.Interchange, NetworkFee: it.AcquirerFee, ProgramID: it.ProgramID,
		})
	}
	out := make([]Advice, len(keys))
	for i, k := range keys {
		out[i] = *byKey[k]
	}
	return out
}

// BankBalance is a member's account at the settlement bank.
type BankBalance struct {
	MemberID string `json:"member_id"`
	Name     string `json:"name"`
	Currency string `json:"currency"`
	Opening  int64  `json:"opening"`
	Balance  int64  `json:"balance"`
}

// View is a copy of the engine's state for display.
type View struct {
	Open        Cycle         `json:"open"`
	History     []Cycle       `json:"history"` // Newest first
	Entries     []Entry       `json:"entries"`
	EntryCount  int           `json:"entry_count"`
	Balances    []Balance     `json:"balances"`
	Bank        []BankBalance `json:"bank"`
	Fees        FeeTable      `json:"fees"`
	Undelivered int           `json:"undelivered"` // Issuer files waiting for the next cutoff
}

// View returns the engine's state, with up to entries ledger entries.
func (e *Engine) View(entries int) View {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.init()
	v := View{Open: e.open.clone(), History: []Cycle{}, Entries: e.ledger.Entries(entries), EntryCount: e.ledger.Len(),
		Balances: e.ledger.Balances(), Bank: []BankBalance{}, Fees: e.Fees, Undelivered: len(e.undelivered)}
	for i := len(e.history) - 1; i >= 0; i-- {
		v.History = append(v.History, e.history[i].clone())
	}
	for k, bal := range e.bank {
		v.Bank = append(v.Bank, BankBalance{MemberID: k[0], Name: e.name(k[0]), Currency: k[1], Opening: e.OpeningBalance, Balance: bal})
	}
	sort.Slice(v.Bank, func(i, j int) bool {
		return v.Bank[i].MemberID+v.Bank[i].Currency < v.Bank[j].MemberID+v.Bank[j].Currency
	})
	return v
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func minor(v int64) string {
	sign := ""
	if v < 0 {
		sign, v = "-", -v
	}
	return fmt.Sprintf("%s%d.%02d", sign, v/100, v%100)
}
