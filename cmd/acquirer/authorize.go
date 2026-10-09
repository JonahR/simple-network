package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"math/rand/v2"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/JonahR/simple-network/internal/card"
	"github.com/JonahR/simple-network/internal/iso8583"
)

// networkTimeout is how long the acquirer waits for the network (D8).
const networkTimeout = 15 * time.Second

const (
	// maxRecords is how many authorizations the back office keeps, like the
	// network's recorder; older ones are dropped.
	maxRecords = 1000
	// seenTTL is how long a terminal's request is remembered for duplicate
	// detection. A terminal retries within seconds; after a day its STAN and
	// DE7 could legitimately come round again.
	seenTTL = 24 * time.Hour
)

// Participants, as named in a message's path.
const (
	PartyPOS      = "pos"
	PartyAcquirer = "acquirer"
	PartyNetwork  = "network"
)

// Step is one message on its way between two participants. MS is the time
// the sender spent before sending it; for a reply it is the round trip the
// receiver waited.
type Step struct {
	From   string  `json:"from"`
	To     string  `json:"to"`
	MTI    string  `json:"mti"`
	Title  string  `json:"title"`
	Detail string  `json:"detail"`
	MS     float64 `json:"ms"`
}

// Change is a field the acquirer set or rewrote before passing a message on.
type Change struct {
	Field  string `json:"field"`
	Name   string `json:"name"`
	Before string `json:"before"`
	After  string `json:"after"`
	Why    string `json:"why"`
}

// Network carries authorizations from the acquirer to card issuers, and
// reversal advices for authorizations whose answer never arrived.
type Network interface {
	Authorize(ctx context.Context, req iso8583.AuthRequest) (iso8583.AuthResponse, error)
	Reverse(ctx context.Context, adv iso8583.ReversalAdvice) (iso8583.ReversalResponse, error)
}

// Record is one authorization as the acquirer saw it. Card data is redacted:
// PAN masked, PIN block hidden, CVV removed.
type Record struct {
	Received     time.Time            `json:"received"`
	Status       string               `json:"status"` // APPROVED, PARTIAL, or DECLINED
	MerchantID   string               `json:"merchant_id"`
	MerchantName string               `json:"merchant_name"`
	TerminalID   string               `json:"terminal_id"`
	TerminalSTAN string               `json:"terminal_stan"`
	Request      iso8583.AuthRequest  `json:"request"`  // As sent to the network
	Response     iso8583.AuthResponse `json:"response"` // As returned to the terminal
	Steps        []Step               `json:"steps"`
	Changes      []Change             `json:"changes"`
	TotalMS      float64              `json:"total_ms"`
	// Reversal is the state of the 0420 the acquirer sends when the network
	// did not answer: pending, acknowledged, or stopped (at shutdown).
	Reversal string `json:"reversal,omitempty"`
}

// Reversal states.
const (
	ReversalPending      = "pending"
	ReversalAcknowledged = "acknowledged"
	ReversalStopped      = "stopped"
	ReversalRejected     = "rejected"
)

// seenEntry is the answer a terminal got for one request, and when.
type seenEntry struct {
	resp iso8583.AuthResponse
	at   time.Time
}

// seenKey is a duplicate-cache key in the order it was added, for expiry.
type seenKey struct {
	key string
	at  time.Time
}

type acquirer struct {
	id        string // DE32 acquiring institution ID
	name      string
	merchants []Merchant
	network   Network
	now       func() time.Time
	stan      atomic.Uint32

	// An unacknowledged 0420 is resent after reversalRetry, doubling each
	// time up to reversalRetryMax.
	reversalRetry    time.Duration
	reversalRetryMax time.Duration

	mu        sync.Mutex
	records   []*Record            // Oldest first, at most maxRecords
	seen      map[string]seenEntry // Idempotency key -> the answer the terminal got
	seenOrder []seenKey            // Keys of seen, oldest first
	closed    bool                 // Set by Close; no new reversals start
	bgCtx     context.Context      // Cancelled by Close
	bgCancel  context.CancelFunc
	bg        sync.WaitGroup // Running reversals
}

func newAcquirer(id, name string, merchants []Merchant, network Network) *acquirer {
	a := &acquirer{
		id: id, name: name, merchants: merchants, network: network, now: time.Now,
		reversalRetry: time.Second, reversalRetryMax: 30 * time.Second,
		seen: map[string]seenEntry{},
	}
	a.bgCtx, a.bgCancel = context.WithCancel(context.Background())
	// One counter serves every terminal, so it is rarely in step with any one
	// terminal's. Start it anywhere, as it would be after a restart.
	a.stan.Store(uint32(rand.IntN(900_000)))
	return a
}

func (a *acquirer) merchant(id string) (Merchant, bool) {
	for _, m := range a.merchants {
		if m.ID == id {
			return m, true
		}
	}
	return Merchant{}, false
}

// nextSTAN returns the acquirer's own trace number for the network leg.
// Terminals number their messages independently, so their STANs can collide.
func (a *acquirer) nextSTAN() string {
	n := a.stan.Add(1)
	return fmt.Sprintf("%06d", (n-1)%999_999+1)
}

// Authorize handles an 0100 from a terminal: it checks the merchant and
// terminal, adds the acquirer's fields, sends the request to the network,
// and returns the 0110 for the terminal. A retry of a request it has already
// answered gets the same answer back without reaching the network again.
func (a *acquirer) Authorize(ctx context.Context, req iso8583.AuthRequest) iso8583.AuthResponse {
	key := req.MerchantID + "|" + req.TerminalID + "|" + req.STAN + "|" + req.TransmissionTime
	a.mu.Lock()
	if e, ok := a.seen[key]; ok && a.now().Sub(e.at) < seenTTL {
		a.mu.Unlock()
		return e.resp
	}
	a.mu.Unlock()

	start := time.Now()
	rec := Record{
		Received:     a.now(),
		MerchantID:   req.MerchantID,
		TerminalID:   req.TerminalID,
		TerminalSTAN: req.STAN,
		Request:      req.Redacted(card.Mask),
		Steps: []Step{{
			From: PartyPOS, To: PartyAcquirer, MTI: req.MTI, Title: "Authorization request",
			Detail: fmt.Sprintf("Terminal %s sends STAN %s for merchant %s", req.TerminalID, req.STAN, req.MerchantID),
		}},
	}

	m, ok := a.merchant(req.MerchantID)
	if !ok || !m.HasTerminal(req.TerminalID) {
		resp := reply(req, iso8583.RCInvalidMerchant)
		rec.Steps = append(rec.Steps, Step{
			From: PartyAcquirer, To: PartyPOS, MTI: resp.MTI, Title: declineTitle(resp),
			Detail: "Merchant or terminal is not on file, so the acquirer declines without asking the network",
			MS:     msSince(start),
		})
		resp, _ = a.finish(key, rec, req, resp, start)
		return resp
	}
	rec.MerchantName = m.Name

	out := req
	out.STAN = a.nextSTAN()
	out.AcquirerID = a.id
	if out.STAN != req.STAN {
		rec.Changes = append(rec.Changes,
			Change{"DE11", "STAN", req.STAN, out.STAN, "The acquirer numbers the network leg itself; terminal STANs can collide"})
	}
	if req.AcquirerID != a.id {
		rec.Changes = append(rec.Changes,
			Change{"DE32", "Acquirer ID", req.AcquirerID, a.id, "Tells the network which bank sent the request and is owed the money"})
	}
	if out.RRN = rrn(a.now(), out.STAN); out.RRN != req.RRN {
		rec.Changes = append(rec.Changes,
			Change{"DE37", "Retrieval reference number", req.RRN, out.RRN, "The acquirer assigns the reference from its own STAN, so it is unique at this bank and matches the network leg"})
	}
	if req.MCC != m.MCC {
		out.MCC = m.MCC
		rec.Changes = append(rec.Changes,
			Change{"DE18", "Merchant category code", req.MCC, m.MCC, "Taken from the merchant agreement, not the terminal"})
	}
	rec.Request = out.Redacted(card.Mask)
	rec.Steps = append(rec.Steps, Step{
		From: PartyAcquirer, To: PartyNetwork, MTI: out.MTI, Title: "Forwarded to the network",
		Detail: fmt.Sprintf("Merchant %s and terminal %s are on file. Added acquirer ID %s, network-leg STAN %s and RRN %s. PIN block passed through unchanged.",
			m.Name, req.TerminalID, a.id, out.STAN, out.RRN),
		MS: msSince(start),
	})

	sent := time.Now()
	nctx, cancel := context.WithTimeout(ctx, networkTimeout)
	resp, err := a.network.Authorize(nctx, out)
	cancel()
	back := time.Now()
	if err != nil {
		// No 0110 arrived, so the acquirer answers the terminal itself. The
		// network may still have approved, so it also sends an 0420 to undo
		// whatever happened (KT 12 D9).
		reason, title := iso8583.RCSuspectedMalfunc, "Network call failed"
		if isTimeout(err) {
			reason, title = iso8583.RCLateResponse, "No answer from the network"
		}
		rec.Steps = append(rec.Steps, Step{
			From: PartyAcquirer, To: PartyAcquirer, Title: title,
			Detail: fmt.Sprintf("%v. No 0110 came back, so the acquirer declines with 91 itself and sends an 0420 reversal advice (reason %s %s) in case the network approved.",
				err, reason, iso8583.ResponseText(reason)),
			MS: msSince(sent),
		})
		resp = reply(req, iso8583.RCIssuerUnavailable)
		resp.RRN = out.RRN
		rec.Steps = append(rec.Steps, Step{
			From: PartyAcquirer, To: PartyPOS, MTI: resp.MTI, Title: declineTitle(resp),
			Detail: "The acquirer's own 0110 to terminal " + req.TerminalID + " with its STAN " + req.STAN,
			MS:     msSince(back),
		})
		rec.Reversal = ReversalPending
		resp, stored := a.finish(key, rec, req, resp, start)
		if !a.background(func(ctx context.Context) { a.reverse(ctx, stored, out, reason) }) {
			a.mu.Lock()
			stored.Reversal = ReversalStopped
			a.mu.Unlock()
		}
		return resp
	}
	rec.Steps = append(rec.Steps, Step{
		From: PartyNetwork, To: PartyAcquirer, MTI: resp.MTI, Title: answerTitle(resp),
		Detail: fmt.Sprintf("The network routed the request to the card's issuer and returned its answer. Network transaction %s.", resp.NetworkTxnID),
		MS:     msSince(sent),
	})

	// Hand the terminal back its own trace number so it can match the reply.
	back = time.Now()
	if resp.STAN != req.STAN {
		rec.Changes = append(rec.Changes,
			Change{"DE11", "STAN (in the 0110)", resp.STAN, req.STAN, "Restored so the terminal can match the response to its request"})
		resp.STAN = req.STAN
	}
	rec.Steps = append(rec.Steps, Step{
		From: PartyAcquirer, To: PartyPOS, MTI: resp.MTI, Title: answerTitle(resp),
		Detail: "Response passed back to terminal " + req.TerminalID + " with its original STAN " + req.STAN,
		MS:     msSince(back),
	})
	resp, _ = a.finish(key, rec, req, resp, start)
	return resp
}

// finish stores the record and returns the response for the terminal, and
// the stored record so a reversal can add to it.
func (a *acquirer) finish(key string, rec Record, req iso8583.AuthRequest, resp iso8583.AuthResponse, start time.Time) (iso8583.AuthResponse, *Record) {
	rec.Response = resp.Redacted(card.Mask)
	rec.Status = status(resp)
	rec.TotalMS = msSince(start)
	stored := &rec
	now := a.now()
	a.mu.Lock()
	// Forget requests old enough that a retry is no longer plausible.
	for len(a.seenOrder) > 0 && now.Sub(a.seenOrder[0].at) >= seenTTL {
		old := a.seenOrder[0]
		if e, ok := a.seen[old.key]; ok && e.at.Equal(old.at) {
			delete(a.seen, old.key)
		}
		a.seenOrder = a.seenOrder[1:]
	}
	a.seen[key] = seenEntry{resp: resp, at: now}
	a.seenOrder = append(a.seenOrder, seenKey{key: key, at: now})
	// Keep only the newest maxRecords.
	if len(a.records) >= maxRecords {
		n := copy(a.records, a.records[len(a.records)-maxRecords+1:])
		clear(a.records[n:])
		a.records = a.records[:n]
	}
	a.records = append(a.records, stored)
	a.mu.Unlock()
	log.Printf("auth merchant=%s terminal=%s stan=%s pan=%s amount=%d code=%s network_txn=%s total_ms=%.1f",
		req.MerchantID, req.TerminalID, req.STAN, card.Mask(req.PAN), req.Amount,
		resp.ResponseCode, resp.NetworkTxnID, rec.TotalMS)
	return resp, stored
}

// reverse sends an 0420 reversal advice for orig, the 0100 the network did
// not answer, so that an approval the acquirer never received doesn't leave
// a hold on the cardholder's account. It resends the same advice, backing
// off from reversalRetry to reversalRetryMax, until the network acknowledges
// it with an 0430 or ctx is cancelled at shutdown. Each step is added to rec.
func (a *acquirer) reverse(ctx context.Context, rec *Record, orig iso8583.AuthRequest, reason string) {
	adv := iso8583.ReversalAdvice{
		MTI:              iso8583.MTIReversalAdvice,
		PAN:              orig.PAN,
		Amount:           orig.Amount,
		TransmissionTime: a.now().UTC().Format("0102150405"),
		STAN:             a.nextSTAN(),
		AcquirerID:       a.id,
		RRN:              orig.RRN,
		ResponseCode:     reason,
		OriginalData: &iso8583.OriginalData{
			MTI:              orig.MTI,
			STAN:             orig.STAN,
			TransmissionTime: orig.TransmissionTime,
			AcquirerID:       orig.AcquirerID,
		},
	}
	a.addStep(rec, Step{
		From: PartyAcquirer, To: PartyNetwork, MTI: adv.MTI, Title: "Reversal advice",
		Detail: fmt.Sprintf("0420 STAN %s, reason %s %s. DE90 names the original 0100: STAN %s sent at %s by acquirer %s. Resent until the network acknowledges it.",
			adv.STAN, reason, iso8583.ResponseText(reason), orig.STAN, orig.TransmissionTime, orig.AcquirerID),
	}, false)
	delay, maxDelay := a.reversalRetry, a.reversalRetryMax
	if delay <= 0 {
		delay = time.Second
	}
	if maxDelay <= 0 {
		maxDelay = 30 * time.Second
	}
	stopped := func() {
		a.addStep(rec, Step{
			From: PartyAcquirer, To: PartyAcquirer, Title: "Reversal retries stopped",
			Detail: "The acquirer shut down before the network acknowledged the 0420.",
		}, false)
		a.setReversal(rec, ReversalStopped)
		log.Printf("reversal stan=%s orig_stan=%s not acknowledged: shutting down", adv.STAN, orig.STAN)
	}
	for attempt := 1; ; attempt++ {
		sent := time.Now()
		actx, cancel := context.WithTimeout(ctx, networkTimeout)
		ack, err := a.network.Reverse(actx, adv)
		cancel()
		if err == nil && ack.ResponseCode != iso8583.RCApproved {
			err = fmt.Errorf("0430 has response code %q", ack.ResponseCode)
		}
		if err == nil {
			detail := "The network had the original 0100 and will reverse it at the issuer, releasing any hold."
			if ack.NetworkTxnID != "" {
				detail = fmt.Sprintf("The network had the original 0100 (network transaction %s) and will reverse it at the issuer, releasing any hold.", ack.NetworkTxnID)
			}
			if !ack.Matched {
				detail = "The network never received the original 0100, so there is nothing to undo."
			}
			a.addStep(rec, Step{
				From: PartyNetwork, To: PartyAcquirer, MTI: iso8583.MTIReversalResponse, Title: "Reversal acknowledged",
				Detail: detail, MS: msSince(sent),
			}, true)
			a.setReversal(rec, ReversalAcknowledged)
			log.Printf("reversal stan=%s orig_stan=%s acknowledged matched=%v network_txn=%s attempts=%d",
				adv.STAN, orig.STAN, ack.Matched, ack.NetworkTxnID, attempt)
			return
		}
		if ctx.Err() != nil {
			stopped()
			return
		}
		// The network refused the advice itself (HTTP 4xx). Resending the
		// same message can't succeed, so stop and leave it for an operator.
		var rejected *rejectedError
		if errors.As(err, &rejected) {
			a.addStep(rec, Step{
				From: PartyNetwork, To: PartyAcquirer, Title: "Reversal rejected",
				Detail: fmt.Sprintf("The network refused the 0420: %v. Resending it can't help, so the acquirer stopped; any hold must be released by hand.", err),
				MS:     msSince(sent),
			}, true)
			a.setReversal(rec, ReversalRejected)
			log.Printf("reversal stan=%s orig_stan=%s rejected: %v", adv.STAN, orig.STAN, err)
			return
		}
		// One note, rewritten on every attempt, so a long outage doesn't
		// grow the record without limit.
		a.addStep(rec, Step{
			From: PartyAcquirer, To: PartyAcquirer, Title: "No 0430 yet",
			Detail: fmt.Sprintf("Attempt %d failed: %v. Resending the same 0420 in %s.", attempt, err, delay),
			MS:     msSince(sent),
		}, true)
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			stopped()
			return
		}
		delay = min(delay*2, maxDelay)
	}
}

// addStep appends a step to a stored record. With replaceNote, it replaces
// the last step instead if that is an acquirer-only note.
func (a *acquirer) addStep(rec *Record, s Step, replaceNote bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if n := len(rec.Steps); replaceNote && n > 0 && rec.Steps[n-1].From == PartyAcquirer && rec.Steps[n-1].To == PartyAcquirer && rec.Steps[n-1].Title == "No 0430 yet" {
		rec.Steps[n-1] = s
		return
	}
	rec.Steps = append(rec.Steps, s)
}

func (a *acquirer) setReversal(rec *Record, state string) {
	a.mu.Lock()
	rec.Reversal = state
	a.mu.Unlock()
}

// background runs fn in a goroutine whose context Close cancels, and reports
// whether it started. Close waits for it to return.
func (a *acquirer) background(fn func(ctx context.Context)) bool {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return false
	}
	a.bg.Add(1)
	a.mu.Unlock()
	go func() {
		defer a.bg.Done()
		fn(a.bgCtx)
	}()
	return true
}

// Close stops retrying reversals and waits for them to record where they
// got to. Call it after the HTTP server has stopped taking requests.
func (a *acquirer) Close() {
	a.mu.Lock()
	a.closed = true
	a.mu.Unlock()
	a.bgCancel()
	a.bg.Wait()
}

// isTimeout reports whether err means the network did not answer in time,
// as opposed to refusing the connection or answering with an error.
func isTimeout(err error) bool {
	var ne net.Error
	return errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout())
}

// rrn builds a retrieval reference number (DE37): the last digit of the
// year, the day of the year, the hour (UTC), then the STAN. 12 characters.
func rrn(t time.Time, stan string) string {
	t = t.UTC()
	return fmt.Sprintf("%d%03d%02d%s", t.Year()%10, t.YearDay(), t.Hour(), stan)
}

// Records returns the kept authorizations, newest first.
func (a *acquirer) Records() []Record {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]Record, len(a.records))
	for i, r := range a.records {
		c := *r
		c.Steps = append([]Step(nil), r.Steps...) // A reversal may still be adding steps
		out[len(a.records)-1-i] = c
	}
	return out
}

// reply builds an 0110 that answers req with a response code, for declines
// the acquirer makes itself.
func reply(req iso8583.AuthRequest, code string) iso8583.AuthResponse {
	return iso8583.AuthResponse{
		MTI:              iso8583.MTIAuthResponse,
		PAN:              req.PAN,
		ProcessingCode:   req.ProcessingCode,
		Amount:           req.Amount, // A decline echoes the requested amount
		TransmissionTime: req.TransmissionTime,
		STAN:             req.STAN,
		RRN:              req.RRN,
		ResponseCode:     code,
		TerminalID:       req.TerminalID,
		MerchantID:       req.MerchantID,
		Currency:         req.Currency,
		ResponseText:     iso8583.ResponseText(code),
	}
}

func status(resp iso8583.AuthResponse) string {
	switch {
	case resp.ResponseCode == iso8583.RCPartialApproval:
		return "PARTIAL"
	case iso8583.IsApproved(resp.ResponseCode):
		return "APPROVED"
	}
	return "DECLINED"
}

func answerTitle(resp iso8583.AuthResponse) string {
	if iso8583.IsApproved(resp.ResponseCode) {
		return fmt.Sprintf("%s %s, auth code %s", resp.ResponseCode, resp.ResponseText, resp.AuthCode)
	}
	return declineTitle(resp)
}

func declineTitle(resp iso8583.AuthResponse) string {
	return fmt.Sprintf("Declined: %s %s", resp.ResponseCode, iso8583.ResponseText(resp.ResponseCode))
}

// msSince returns the milliseconds elapsed since t, to a tenth.
func msSince(t time.Time) float64 {
	return math.Round(float64(time.Since(t).Microseconds())/100) / 10
}
