package main

import (
	"context"
	"fmt"
	"log"
	"math"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/JonahR/simple-network/internal/card"
	"github.com/JonahR/simple-network/internal/iso8583"
)

// networkTimeout is how long the acquirer waits for the network (D8).
const networkTimeout = 15 * time.Second

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

// Network carries authorizations from the acquirer to card issuers.
type Network interface {
	Authorize(ctx context.Context, req iso8583.AuthRequest) (iso8583.AuthResponse, error)
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
}

type acquirer struct {
	id        string // DE32 acquiring institution ID
	name      string
	merchants []Merchant
	network   Network
	now       func() time.Time
	stan      atomic.Uint32

	mu      sync.Mutex
	records []Record
	seen    map[string]iso8583.AuthResponse // Idempotency key -> the answer the terminal got
}

func newAcquirer(id, name string, merchants []Merchant, network Network) *acquirer {
	a := &acquirer{id: id, name: name, merchants: merchants, network: network, now: time.Now, seen: map[string]iso8583.AuthResponse{}}
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
	if resp, ok := a.seen[key]; ok {
		a.mu.Unlock()
		return resp
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
		return a.finish(key, rec, req, resp, start)
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
	if req.MCC != m.MCC {
		out.MCC = m.MCC
		rec.Changes = append(rec.Changes,
			Change{"DE18", "Merchant category code", req.MCC, m.MCC, "Taken from the merchant agreement, not the terminal"})
	}
	rec.Request = out.Redacted(card.Mask)
	rec.Steps = append(rec.Steps, Step{
		From: PartyAcquirer, To: PartyNetwork, MTI: out.MTI, Title: "Forwarded to the network",
		Detail: fmt.Sprintf("Merchant %s and terminal %s are on file. Added acquirer ID %s and network-leg STAN %s. PIN block passed through unchanged.",
			m.Name, req.TerminalID, a.id, out.STAN),
		MS: msSince(start),
	})

	sent := time.Now()
	nctx, cancel := context.WithTimeout(ctx, networkTimeout)
	resp, err := a.network.Authorize(nctx, out)
	cancel()
	if err != nil {
		resp = reply(out, iso8583.RCIssuerUnavailable)
		rec.Steps = append(rec.Steps, Step{
			From: PartyNetwork, To: PartyAcquirer, MTI: iso8583.MTIAuthResponse, Title: "No answer",
			Detail: "The acquirer declines on the network's behalf: " + err.Error(), MS: msSince(sent),
		})
	} else {
		rec.Steps = append(rec.Steps, Step{
			From: PartyNetwork, To: PartyAcquirer, MTI: resp.MTI, Title: answerTitle(resp),
			Detail: fmt.Sprintf("The network routed the request to the card's issuer and returned its answer. Network transaction %s.", resp.NetworkTxnID),
			MS:     msSince(sent),
		})
	}

	// Hand the terminal back its own trace number so it can match the reply.
	back := time.Now()
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
	return a.finish(key, rec, req, resp, start)
}

// finish stores the record and returns the response for the terminal.
func (a *acquirer) finish(key string, rec Record, req iso8583.AuthRequest, resp iso8583.AuthResponse, start time.Time) iso8583.AuthResponse {
	rec.Response = resp.Redacted(card.Mask)
	rec.Status = status(resp)
	rec.TotalMS = msSince(start)
	a.mu.Lock()
	a.seen[key] = resp
	a.records = append(a.records, rec)
	a.mu.Unlock()
	log.Printf("auth merchant=%s terminal=%s stan=%s pan=%s amount=%d code=%s network_txn=%s total_ms=%.1f",
		req.MerchantID, req.TerminalID, req.STAN, card.Mask(req.PAN), req.Amount,
		resp.ResponseCode, resp.NetworkTxnID, rec.TotalMS)
	return resp
}

// Records returns every authorization, newest first.
func (a *acquirer) Records() []Record {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]Record, len(a.records))
	for i, r := range a.records {
		out[len(a.records)-1-i] = r
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
