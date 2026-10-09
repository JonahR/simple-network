package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/JonahR/simple-network/internal/card"
	"github.com/JonahR/simple-network/internal/iso8583"
	"github.com/JonahR/simple-network/internal/pin"
)

const (
	// maxRuns is how many finished runs the page can look back at.
	maxRuns = 20
	// maxResults caps the rows one run keeps. Counts cover every probe, so a
	// large burst still scores correctly when its rows are trimmed.
	maxResults = 500
	// httpTimeout is longer than the acquirer's wait for the network, so a
	// timeout drill gets the acquirer's answer rather than giving up first.
	httpTimeout = 20 * time.Second
)

// Config is where the lab sends messages and which merchant it poses as.
type Config struct {
	AcquirerURL string
	NetworkURL  string
	IssuerURLs  map[string]string // By issuer ID
	AcquirerID  string            // DE32 for probes sent straight to the network
	MerchantID  string            // The Risk Lab merchant, signed up at the acquirer
	TerminalID  string
	PINKey      []byte // Acquirer PIN key, as the PIN pad holds it
	Links       Links
}

// Links are the browser URLs of the other pages, for following a probe there.
type Links struct {
	Acquirer   string            `json:"acquirer"`
	Network    string            `json:"network"`
	Settlement string            `json:"settlement"`
	Issuers    map[string]string `json:"issuers"`
}

// Where a probe is sent.
const (
	ViaAcquirer = "acquirer" // As a terminal of the Risk Lab merchant
	ViaNetwork  = "network"  // Straight to the switch, bypassing any acquirer
)

// Result kinds.
const (
	KindProbe   = "probe"   // An 0100 the lab sent
	KindAction  = "action"  // A change the lab made: a fault mode, a frozen card, a void
	KindCheck   = "check"   // Something the lab confirmed afterwards, e.g. a reversal
	KindFinding = "finding" // A weakness the drill showed, with no message of its own
)

// Verdicts.
const (
	VerdictCaught     = "caught"     // Declined by the control the drill expected
	VerdictAllowed    = "allowed"    // A legitimate baseline, approved as it should be
	VerdictGap        = "gap"        // Behaved as built, but a real network would have stopped it
	VerdictUnexpected = "unexpected" // Not what the drill expected
	VerdictOK         = "ok"         // An action or check that succeeded
	VerdictFailed     = "failed"     // An action or check that did not
)

// Result is one row of a run.
type Result struct {
	Seq     int     `json:"seq"`
	Kind    string  `json:"kind"`
	Label   string  `json:"label"`
	Verdict string  `json:"verdict"`
	Control string  `json:"control,omitempty"` // The defense the probe exercises
	Term    string  `json:"term,omitempty"`    // Glossary term for the control
	Gap     string  `json:"gap,omitempty"`     // What a real network would do instead
	Note    string  `json:"note,omitempty"`
	MS      float64 `json:"ms,omitempty"`

	// Probes only.
	Via          string   `json:"via,omitempty"`
	Card         string   `json:"card,omitempty"` // Masked
	Entry        string   `json:"entry,omitempty"`
	Amount       int64    `json:"amount,omitempty"`
	Expect       []string `json:"expect,omitempty"`
	Code         string   `json:"code,omitempty"`
	Text         string   `json:"text,omitempty"`
	StoppedAt    string   `json:"stopped_at,omitempty"` // acquirer, network, or issuer
	StopStep     string   `json:"stop_step,omitempty"`  // The network trace step that failed
	StopDetail   string   `json:"stop_detail,omitempty"`
	NetworkTxnID string   `json:"network_txn_id,omitempty"`
	IssuerID     string   `json:"issuer_id,omitempty"`
	TerminalID   string   `json:"terminal_id,omitempty"` // Set when the acquirer has the record
	STAN         string   `json:"stan,omitempty"`        // The terminal's DE11, for the acquirer link
	DE7          string   `json:"de7,omitempty"`
}

// Run is one execution of a drill.
type Run struct {
	ID       string         `json:"id"`
	Drill    string         `json:"drill"`
	Title    string         `json:"title"`
	Status   string         `json:"status"` // running, done
	Started  time.Time      `json:"started"`
	Finished *time.Time     `json:"finished,omitempty"`
	Probes   int            `json:"probes"`
	Tally    map[string]int `json:"tally"` // By verdict, over every row including trimmed ones
	Codes    map[string]int `json:"codes"` // Probe response codes
	Trimmed  int            `json:"trimmed"`
	Results  []Result       `json:"results"`
}

func (r *Run) summary() Run {
	c := *r
	c.Results = nil
	c.Tally, c.Codes = cloneMap(r.Tally), cloneMap(r.Codes)
	return c
}

func (r *Run) clone() Run {
	c := r.summary()
	c.Results = slices.Clone(r.Results)
	return c
}

func cloneMap(m map[string]int) map[string]int {
	c := make(map[string]int, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

// Lab runs drills, one at a time: drills change shared state such as an
// issuer's fault mode, so two at once would spoil each other's results.
type Lab struct {
	cfg  Config
	http *http.Client
	stan atomic.Uint32

	mu     sync.Mutex
	runs   []*Run // Oldest first
	active *Run
	seq    int

	bgCtx    context.Context
	bgCancel context.CancelFunc
	bg       sync.WaitGroup
}

// NewLab creates a lab.
func NewLab(cfg Config) *Lab {
	l := &Lab{cfg: cfg, http: &http.Client{Timeout: httpTimeout}}
	l.bgCtx, l.bgCancel = context.WithCancel(context.Background())
	l.stan.Store(uint32(rand.IntN(900_000)))
	return l
}

// Close stops the running drill and waits for its clean-up.
func (l *Lab) Close() {
	l.bgCancel()
	l.bg.Wait()
}

var errBusy = errors.New("a drill is already running")

// Start runs a drill in the background and returns its run.
func (l *Lab) Start(id string) (Run, error) {
	d, ok := drillByID(id)
	if !ok {
		return Run{}, fmt.Errorf("unknown drill %q", id)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.active != nil {
		return Run{}, errBusy
	}
	l.seq++
	run := &Run{
		ID: fmt.Sprintf("run-%d", l.seq), Drill: d.ID, Title: d.Title, Status: "running",
		Started: time.Now().UTC(), Tally: map[string]int{}, Codes: map[string]int{}, Results: []Result{},
	}
	l.active = run
	l.runs = append(l.runs, run)
	if len(l.runs) > maxRuns {
		l.runs = l.runs[1:]
	}
	l.bg.Add(1)
	go func() {
		defer l.bg.Done()
		r := &runner{lab: l, run: run, ctx: l.bgCtx}
		r.execute(d)
		l.mu.Lock()
		now := time.Now().UTC()
		run.Status, run.Finished = "done", &now
		l.active = nil
		l.mu.Unlock()
		log.Printf("drill %s finished: %v", d.ID, run.Tally)
	}()
	return run.clone(), nil
}

// Run returns a copy of a run.
func (l *Lab) Run(id string) (Run, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, r := range l.runs {
		if r.ID == id {
			return r.clone(), true
		}
	}
	return Run{}, false
}

// Runs returns summaries of the kept runs, newest first.
func (l *Lab) Runs() []Run {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Run, 0, len(l.runs))
	for i := len(l.runs) - 1; i >= 0; i-- {
		out = append(out, l.runs[i].summary())
	}
	return out
}

// Register mounts the lab's API.
func (l *Lab) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/drills", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"drills":         catalogView(),
			"links":          l.cfg.Links,
			"merchant_id":    l.cfg.MerchantID,
			"terminal_id":    l.cfg.TerminalID,
			"response_codes": iso8583.ResponseCodes,
			"entry_modes":    iso8583.EntryModes,
		})
	})
	mux.HandleFunc("GET /api/runs", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, l.Runs())
	})
	mux.HandleFunc("GET /api/runs/{id}", func(w http.ResponseWriter, r *http.Request) {
		run, ok := l.Run(r.PathValue("id"))
		if !ok {
			http.Error(w, "run not found", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, run)
	})
	mux.HandleFunc("POST /api/runs", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Drill string `json:"drill"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&body); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		run, err := l.Start(body.Drill)
		switch {
		case errors.Is(err, errBusy):
			http.Error(w, err.Error(), http.StatusConflict)
		case err != nil:
			http.Error(w, err.Error(), http.StatusNotFound)
		default:
			writeJSON(w, http.StatusAccepted, run)
		}
	})
}

// Card is what a probe presents: a stolen, guessed, or legitimate card.
type Card struct {
	PAN    string
	Expiry string // YYMM
	CVV2   string
	PIN    string // Encrypted into DE52 under the acquirer key
	Wallet string // Set when PAN is a device token
	ARQC   string // Cryptogram from a chip, contactless card, or wallet
}

// Probe is one 0100 a drill sends and what it expects back.
type Probe struct {
	Label  string
	Via    string
	Card   Card
	Entry  string
	Amount int64
	// AcquirerID claims to be this acquirer when sent straight to the network.
	// Empty means the configured acquirer.
	AcquirerID string
	TerminalID string                     // Empty means the lab's terminal
	Mutate     func(*iso8583.AuthRequest) // Last-minute changes, e.g. a bad currency
	Expect     []string                   // Response codes that count as expected
	ExpectStep string                     // When set, the network trace step that must have stopped it
	Control    string
	Term       string
	Gap        string
	// Replay resends this exact request (same STAN and DE7) instead of a new one.
	Replay *iso8583.AuthRequest
	// Verify, when set, must also accept the response for the probe to count
	// as caught: for a control whose effect isn't a decline, like answering a
	// replay with the original response.
	Verify func(iso8583.AuthResponse) (bool, string)
}

// sent is an approval the lab must undo when the drill ends.
type sent struct {
	via string
	req iso8583.AuthRequest
}

// runner executes one drill.
type runner struct {
	lab *Lab
	run *Run
	ctx context.Context

	mu       sync.Mutex
	approved map[string]sent // By acquirer ID + STAN + DE7
	cleanups []cleanup
}

type cleanup struct {
	label string
	fn    func(context.Context) error
}

func (r *runner) execute(d Drill) {
	defer func() {
		if p := recover(); p != nil {
			log.Printf("drill %s panicked: %v", d.ID, p)
			r.add(Result{Kind: KindAction, Label: "Drill stopped", Verdict: VerdictFailed, Note: fmt.Sprint(p)})
		}
		r.finish()
	}()
	d.run(r)
}

// finish undoes the drill's changes, then voids its approvals. It runs even
// when the lab is shutting down, with its own deadline.
func (r *runner) finish() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for i := len(r.cleanups) - 1; i >= 0; i-- {
		c := r.cleanups[i]
		r.action(ctx, c.label, c.fn)
	}
	r.mu.Lock()
	pending := make([]sent, 0, len(r.approved))
	for _, s := range r.approved {
		pending = append(pending, s)
	}
	r.approved = nil
	r.mu.Unlock()
	if len(pending) == 0 {
		return
	}
	start := time.Now()
	var failed []string
	for _, s := range pending {
		if err := r.lab.void(ctx, s); err != nil {
			failed = append(failed, err.Error())
		}
	}
	res := Result{
		Kind: KindAction, Verdict: VerdictOK, Term: "void", Control: "Void",
		Label: fmt.Sprintf("Voided %d approval%s the drill made (0420, reason 17)", len(pending), plural(len(pending))),
		Note:  "So no hold stays on the card and nothing reaches clearing.",
		MS:    msSince(start),
	}
	if len(failed) > 0 {
		res.Verdict = VerdictFailed
		res.Note = strings.Join(failed, "; ")
	}
	r.add(res)
}

// onFinish registers a clean-up that runs when the drill ends, newest first.
func (r *runner) onFinish(label string, fn func(context.Context) error) {
	r.cleanups = append(r.cleanups, cleanup{label, fn})
}

// action runs a change and records it. It reports whether it succeeded.
func (r *runner) action(ctx context.Context, label string, fn func(context.Context) error) bool {
	start := time.Now()
	err := fn(ctx)
	res := Result{Kind: KindAction, Label: label, Verdict: VerdictOK, MS: msSince(start)}
	if err != nil {
		res.Verdict, res.Note = VerdictFailed, err.Error()
	}
	r.add(res)
	return err == nil
}

// finding records a weakness the drill showed.
func (r *runner) finding(label, gap, term string) {
	r.add(Result{Kind: KindFinding, Label: label, Verdict: VerdictGap, Gap: gap, Term: term, Control: "Not in this network"})
}

// check records something the lab confirmed after the fact.
func (r *runner) check(label, control, term string, ok bool, note string) {
	v := VerdictOK
	if !ok {
		v = VerdictFailed
	}
	r.add(Result{Kind: KindCheck, Label: label, Control: control, Term: term, Verdict: v, Note: note})
}

func (r *runner) add(res Result) {
	l := r.lab
	l.mu.Lock()
	defer l.mu.Unlock()
	run := r.run
	run.Tally[res.Verdict]++
	if res.Kind == KindProbe {
		run.Probes++
		run.Codes[res.Code]++
	}
	if len(run.Results) >= maxResults {
		run.Trimmed++
		return
	}
	res.Seq = len(run.Results) + 1
	run.Results = append(run.Results, res)
}

// burst sends probes with up to workers in flight at once.
func (r *runner) burst(probes []Probe, workers int) {
	jobs := make(chan Probe)
	var wg sync.WaitGroup
	for range max(1, workers) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range jobs {
				r.probe(p)
			}
		}()
	}
	for _, p := range probes {
		if r.ctx.Err() != nil {
			break
		}
		jobs <- p
	}
	close(jobs)
	wg.Wait()
}

// probe sends one 0100 and records what happened to it. It returns the row
// and the request, so a drill can replay it.
func (r *runner) probe(p Probe) (Result, iso8583.AuthRequest) {
	l := r.lab
	var req iso8583.AuthRequest
	if p.Replay != nil {
		req = *p.Replay
	} else {
		req = l.build(p)
	}
	res := Result{
		Kind: KindProbe, Label: p.Label, Via: p.Via, Card: card.Mask(req.PAN), Entry: req.EntryMode,
		Amount: req.Amount, Expect: p.Expect, Control: p.Control, Term: p.Term, Gap: p.Gap,
	}
	if p.Via == ViaAcquirer {
		res.TerminalID, res.STAN, res.DE7 = req.TerminalID, req.STAN, req.TransmissionTime
	}

	start := time.Now()
	resp, err := l.send(r.ctx, p.Via, req)
	res.MS = msSince(start)
	if err != nil {
		res.Verdict, res.Note = VerdictUnexpected, err.Error()
		r.add(res)
		return res, req
	}
	res.Code, res.Text = resp.ResponseCode, iso8583.ResponseText(resp.ResponseCode)
	res.NetworkTxnID = resp.NetworkTxnID

	if iso8583.IsApproved(resp.ResponseCode) {
		r.remember(p.Via, req)
	}
	if resp.NetworkTxnID == "" {
		if !iso8583.IsApproved(resp.ResponseCode) {
			res.StoppedAt, res.StopDetail = "acquirer", "Declined before reaching the network"
		}
	} else if tr, err := l.trace(r.ctx, resp.NetworkTxnID); err == nil {
		res.IssuerID = tr.IssuerID
		if !iso8583.IsApproved(resp.ResponseCode) {
			res.StoppedAt, res.StopStep, res.StopDetail = tr.stoppedAt()
		}
	}

	expected := slices.Contains(p.Expect, res.Code) && (p.ExpectStep == "" || p.ExpectStep == res.StopStep)
	verified := false
	if p.Verify != nil {
		verified, res.Note = p.Verify(resp)
		expected = expected && verified
	}
	switch {
	case !expected:
		res.Verdict = VerdictUnexpected
		if p.ExpectStep != "" && res.StopStep != p.ExpectStep {
			res.Note = fmt.Sprintf("Expected the network's %s step to stop it, got %q", p.ExpectStep, res.StopStep)
		}
	case p.Gap != "":
		res.Verdict = VerdictGap
	case iso8583.IsApproved(res.Code) && !verified:
		res.Verdict = VerdictAllowed
	default:
		res.Verdict = VerdictCaught
	}
	r.add(res)
	return res, req
}

// remember notes an approval to void when the drill ends.
func (r *runner) remember(via string, req iso8583.AuthRequest) {
	key := via + "|" + req.AcquirerID + "|" + req.TerminalID + "|" + req.STAN + "|" + req.TransmissionTime
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.approved == nil {
		r.approved = map[string]sent{}
	}
	r.approved[key] = sent{via: via, req: req}
}

// waitTrace polls the network's trace for txnID until ok accepts it or
// timeout passes, and reports whether it did.
func (r *runner) waitTrace(txnID string, timeout time.Duration, ok func(networkTrace) bool) (networkTrace, bool) {
	deadline := time.Now().Add(timeout)
	for {
		tr, err := r.lab.trace(r.ctx, txnID)
		if err == nil && ok(tr) {
			return tr, true
		}
		if time.Now().After(deadline) || r.ctx.Err() != nil {
			return tr, false
		}
		select {
		case <-time.After(250 * time.Millisecond):
		case <-r.ctx.Done():
		}
	}
}

// pause waits d unless the lab is shutting down.
func (r *runner) pause(d time.Duration) {
	select {
	case <-time.After(d):
	case <-r.ctx.Done():
	}
}

// build fills in an 0100 for a probe the way a terminal does.
func (l *Lab) build(p Probe) iso8583.AuthRequest {
	now := time.Now().UTC()
	stan := l.nextSTAN()
	req := iso8583.AuthRequest{
		MTI:              iso8583.MTIAuthRequest,
		PAN:              p.Card.PAN,
		ProcessingCode:   iso8583.ProcessingCode(iso8583.TxnPurchase, iso8583.AccountDefault),
		Amount:           p.Amount,
		TransmissionTime: now.Format("0102150405"),
		STAN:             stan,
		LocalTime:        now.Format("150405"),
		LocalDate:        now.Format("0102"),
		Expiry:           p.Card.Expiry,
		MCC:              "5999",
		EntryMode:        p.Entry,
		RRN:              fmt.Sprintf("%d%03d%02d%s", now.Year()%10, now.YearDay(), now.Hour(), stan),
		TerminalID:       l.cfg.TerminalID,
		MerchantID:       l.cfg.MerchantID,
		MerchantNameLoc:  fmt.Sprintf("%-25s%-13sUS", "Risk Lab", "San Francisco"),
		Currency:         "840",
		CVV2:             p.Card.CVV2,
		ARQC:             p.Card.ARQC,
		WalletProvider:   p.Card.Wallet,
	}
	if p.TerminalID != "" {
		req.TerminalID = p.TerminalID
	}
	if p.Via == ViaNetwork {
		req.AcquirerID = l.cfg.AcquirerID
		if p.AcquirerID != "" {
			req.AcquirerID = p.AcquirerID
		}
	}
	if p.Card.PIN != "" {
		block, err := pin.Encrypt(p.Card.PIN, p.Card.PAN, l.cfg.PINKey)
		if err != nil {
			log.Printf("PIN block for %s: %v", card.Mask(p.Card.PAN), err)
		}
		req.PINData = block
	}
	if p.Mutate != nil {
		p.Mutate(&req)
	}
	return req
}

// nextSTAN returns the lab terminal's next DE11.
func (l *Lab) nextSTAN() string {
	n := l.stan.Add(1)
	return fmt.Sprintf("%06d", (n-1)%999_999+1)
}

// send posts an 0100 and returns the 0110.
func (l *Lab) send(ctx context.Context, via string, req iso8583.AuthRequest) (iso8583.AuthResponse, error) {
	base := l.cfg.AcquirerURL
	if via == ViaNetwork {
		base = l.cfg.NetworkURL
	}
	var resp iso8583.AuthResponse
	err := l.post(ctx, base+"/authorize", req, &resp)
	return resp, err
}

// void undoes an approval with an 0420, reason 17 customer cancellation: to
// the acquirer as the terminal, or to the network as the acquirer it claimed.
func (l *Lab) void(ctx context.Context, s sent) error {
	now := time.Now().UTC()
	adv := iso8583.ReversalAdvice{
		MTI: iso8583.MTIReversalAdvice, PAN: s.req.PAN, Amount: s.req.Amount,
		TransmissionTime: now.Format("0102150405"), STAN: l.nextSTAN(), RRN: s.req.RRN,
		ResponseCode: iso8583.RCCustomerCancel,
		OriginalData: &iso8583.OriginalData{MTI: iso8583.MTIAuthRequest, STAN: s.req.STAN, TransmissionTime: s.req.TransmissionTime},
	}
	base := l.cfg.AcquirerURL
	if s.via == ViaNetwork {
		base = l.cfg.NetworkURL
		adv.AcquirerID, adv.OriginalData.AcquirerID = s.req.AcquirerID, s.req.AcquirerID
	} else {
		adv.TerminalID, adv.MerchantID = s.req.TerminalID, s.req.MerchantID
	}
	var ack iso8583.ReversalResponse
	if err := l.post(ctx, base+"/reverse", adv, &ack); err != nil {
		return fmt.Errorf("void of STAN %s: %w", s.req.STAN, err)
	}
	return nil
}

// networkTrace is the part of the network's transaction record the lab reads.
type networkTrace struct {
	IssuerID string      `json:"issuer_id"`
	Reversed bool        `json:"reversed"`
	Steps    []traceStep `json:"steps"`
}

type traceStep struct {
	Name   string `json:"name"`
	Detail string `json:"detail"`
	OK     bool   `json:"ok"`
}

// stoppedAt finds the first failed step: which participant stopped the
// authorization, the step's name, and why.
func (t networkTrace) stoppedAt() (party, step, detail string) {
	for _, s := range t.Steps {
		if s.OK {
			continue
		}
		party = "network"
		// The issuer step fails both when the issuer declines and when it
		// never answered; only an answer is the issuer's decision.
		if s.Name == "issuer" && strings.HasPrefix(s.Detail, "0110 ") {
			party = "issuer"
		}
		return party, s.Name, s.Detail
	}
	return "", "", ""
}

func (l *Lab) trace(ctx context.Context, txnID string) (networkTrace, error) {
	var t networkTrace
	err := l.get(ctx, l.cfg.NetworkURL+"/api/transactions/"+txnID, &t)
	return t, err
}

// issuerMode returns an issuer's simulated fault mode.
func (l *Lab) issuerMode(ctx context.Context, issuer string) (string, error) {
	var body struct {
		Mode string `json:"mode"`
	}
	err := l.get(ctx, l.cfg.IssuerURLs[issuer]+"/admin/mode", &body)
	return body.Mode, err
}

func (l *Lab) setIssuerMode(ctx context.Context, issuer, mode string) error {
	return l.post(ctx, l.cfg.IssuerURLs[issuer]+"/admin/mode", map[string]string{"mode": mode}, nil)
}

// setFrozen freezes or unfreezes a card at its issuer, as the cardholder can
// in their banking app.
func (l *Lab) setFrozen(ctx context.Context, issuer, pan string, frozen bool) error {
	var overview struct {
		Accounts []issuerAccount `json:"accounts"`
	}
	base := l.cfg.IssuerURLs[issuer]
	if err := l.get(ctx, base+"/api/overview", &overview); err != nil {
		return err
	}
	i := slices.IndexFunc(overview.Accounts, func(a issuerAccount) bool { return a.PAN == card.Mask(pan) })
	if i < 0 {
		return fmt.Errorf("%s has no account for %s", issuer, card.Mask(pan))
	}
	return l.post(ctx, base+"/api/accounts/"+overview.Accounts[i].ID+"/blocked", map[string]bool{"blocked": frozen}, nil)
}

// issuerAccount is the part of an issuer's back-office account the lab reads.
type issuerAccount struct {
	ID  string `json:"id"`
	PAN string `json:"pan"` // Masked
}

// setTokenActive suspends or reactivates a device token in the network's vault.
func (l *Lab) setTokenActive(ctx context.Context, token string, active bool) error {
	return l.post(ctx, l.cfg.NetworkURL+"/api/tokens/"+token+"/active", map[string]bool{"active": active}, nil)
}

func (l *Lab) post(ctx context.Context, url string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return l.do(req, out)
}

func (l *Lab) get(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	return l.do(req, out)
}

func (l *Lab) do(req *http.Request, out any) error {
	res, err := l.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s unreachable: %v", req.URL.Host, err)
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		text, _ := io.ReadAll(io.LimitReader(res.Body, 200))
		return fmt.Errorf("%s answered HTTP %d: %s", req.URL.Host, res.StatusCode, strings.TrimSpace(string(text)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(res.Body).Decode(out)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// msSince returns the milliseconds elapsed since t, to a tenth.
func msSince(t time.Time) float64 {
	return float64(time.Since(t).Microseconds()/100) / 10
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
