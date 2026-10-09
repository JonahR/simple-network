package network

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/JonahR/simple-network/internal/card"
	"github.com/JonahR/simple-network/internal/iso8583"
	"github.com/JonahR/simple-network/internal/pin"
)

// Errors an IssuerClient returns.
var (
	ErrIssuerTimeout     = errors.New("issuer did not respond in time")
	ErrIssuerUnavailable = errors.New("issuer unavailable")
)

// IssuerClient sends messages to one issuer.
type IssuerClient interface {
	Authorize(ctx context.Context, req iso8583.AuthRequest) (iso8583.AuthResponse, error)
	Reverse(ctx context.Context, adv iso8583.ReversalAdvice) (iso8583.ReversalResponse, error)
}

// Issuer is a member bank the network routes to.
type Issuer struct {
	ID      string
	Name    string
	Client  IssuerClient
	PINKey  []byte // Key the network shares with this issuer for PIN blocks
	Breaker *Breaker
}

// Switch processes authorization requests.
type Switch struct {
	BINs          *BinTable
	Vault         *Vault
	Issuers       map[string]*Issuer
	AcquirerKeys  map[string][]byte // PIN keys shared with each acquirer
	IssuerTimeout time.Duration     // D8: 5s from network to issuer
	ReversalRetry time.Duration     // Delay between 0420 attempts
	Recorder      *Recorder
	Now           func() time.Time // Injectable clock (D20)

	mu       sync.Mutex
	seen     map[string]*attempt // Idempotency: acquirer ID + STAN + DE7 (D3)
	seenFIFO []string
}

type attempt struct {
	done chan struct{}
	resp iso8583.AuthResponse
	rec  *Record
}

const maxSeen = 10_000

// Authorize processes an 0100 and returns the 0110 to send back to the
// acquirer. A repeated request (same acquirer, STAN, and transmission time)
// gets the original response and never reaches the issuer twice.
func (s *Switch) Authorize(ctx context.Context, req iso8583.AuthRequest) iso8583.AuthResponse {
	key := req.AcquirerID + "|" + req.STAN + "|" + req.TransmissionTime
	s.mu.Lock()
	if s.seen == nil {
		s.seen = map[string]*attempt{}
	}
	if a, ok := s.seen[key]; ok {
		s.mu.Unlock()
		select {
		case <-a.done:
		case <-ctx.Done():
			return s.decline(req, "", iso8583.RCDuplicate)
		}
		s.Recorder.update(a.rec, func(r *Record) { r.Duplicates++ })
		return a.resp
	}
	a := &attempt{done: make(chan struct{})}
	s.seen[key] = a
	s.seenFIFO = append(s.seenFIFO, key)
	if len(s.seenFIFO) > maxSeen {
		delete(s.seen, s.seenFIFO[0])
		s.seenFIFO = s.seenFIFO[1:]
	}
	s.mu.Unlock()

	a.resp = s.process(req, a)
	close(a.done)
	return a.resp
}

// tracer appends steps to a record.
type tracer struct {
	s     *Switch
	rec   *Record
	start time.Time
}

func (t *tracer) step(name, detail string, ok bool, began time.Time) {
	now := t.s.Now()
	t.s.Recorder.update(t.rec, func(r *Record) {
		r.Steps = append(r.Steps, Step{
			Name:       name,
			Detail:     detail,
			OK:         ok,
			StartUS:    began.Sub(t.start).Microseconds(),
			DurationUS: now.Sub(began).Microseconds(),
		})
	})
}

func (s *Switch) process(req iso8583.AuthRequest, a *attempt) iso8583.AuthResponse {
	start := s.Now()
	id := NewTxnID(start)
	rec := &Record{
		ID:         id,
		Received:   start.UTC(),
		Status:     "pending",
		AcquirerID: req.AcquirerID,
		Request:    req.Redacted(card.Mask),
		Steps:      []Step{},
	}
	a.rec = rec
	s.Recorder.add(rec)
	tr := &tracer{s: s, rec: rec, start: start}

	resp, fwd := s.authorize(req, id, tr)

	began := s.Now()
	redacted := resp.Redacted(card.Mask)
	s.Recorder.update(rec, func(r *Record) {
		r.ResponseCode = resp.ResponseCode
		r.ResponseText = resp.ResponseText
		r.Response = &redacted
		if fwd != nil {
			f := fwd.Redacted(card.Mask)
			r.IssuerRequest = &f
		}
		r.Status = "declined"
		if iso8583.IsApproved(resp.ResponseCode) {
			r.Status = "approved"
		}
	})
	tr.step("respond", fmt.Sprintf("0110 %s %s to acquirer %s", resp.ResponseCode, resp.ResponseText, req.AcquirerID), true, began)
	s.Recorder.update(rec, func(r *Record) { r.LatencyUS = s.Now().Sub(start).Microseconds() })
	return resp
}

// authorize runs the switch's steps. It returns the response and, if the
// request reached an issuer, what was forwarded.
func (s *Switch) authorize(req iso8583.AuthRequest, id string, tr *tracer) (iso8583.AuthResponse, *iso8583.AuthRequest) {
	decline := func(code string) (iso8583.AuthResponse, *iso8583.AuthRequest) {
		return s.decline(req, id, code), nil
	}

	// 1. Validate the message.
	t0 := s.Now()
	if err := validate(req); err != nil {
		tr.step("validate", err.Error(), false, t0)
		return decline(iso8583.RCFormatError)
	}
	tr.step("validate", "0100 well formed, PAN passes Luhn", true, t0)

	// 2. Route by BIN, detokenizing wallet payments first.
	fwd := req
	fwd.NetworkTxnID = id
	t0 = s.Now()
	entry, ok := s.BINs.Lookup(req.PAN)
	if !ok {
		tr.step("route", "no BIN table entry for "+card.BIN(req.PAN), false, t0)
		return decline(iso8583.RCNoSuchIssuer)
	}
	if entry.TokenRange {
		tr.step("route", fmt.Sprintf("BIN %s is a token range → token vault", entry.Prefix), true, t0)
		t0 = s.Now()
		tok, code, detail := s.detokenize(req)
		if code != "" {
			tr.step("detokenize", detail, false, t0)
			return decline(code)
		}
		fwd.PAN, fwd.Expiry, fwd.Token = tok.PAN, tok.CardExpiry, req.PAN
		tr.step("detokenize", detail, true, t0)
		t0 = s.Now()
		if entry, ok = s.BINs.Lookup(fwd.PAN); !ok || entry.TokenRange {
			tr.step("route", "no issuer for detokenized card "+card.BIN(fwd.PAN), false, t0)
			return decline(iso8583.RCNoSuchIssuer)
		}
	}
	iss, ok := s.Issuers[entry.IssuerID]
	if !ok {
		tr.step("route", fmt.Sprintf("BIN %s names unknown issuer %s", entry.Prefix, entry.IssuerID), false, t0)
		return decline(iso8583.RCNoSuchIssuer)
	}
	s.Recorder.update(tr.rec, func(r *Record) { r.IssuerID = iss.ID })
	tr.step("route", fmt.Sprintf("BIN %s → %s (%s)", entry.Prefix, iss.Name, card.ProductNames[entry.Product]), true, t0)

	// 3. Translate the PIN block from the acquirer's key to the issuer's.
	if req.PINData != "" {
		t0 = s.Now()
		acqKey, ok := s.AcquirerKeys[req.AcquirerID]
		if !ok {
			tr.step("translate_pin", "no PIN key shared with acquirer "+req.AcquirerID, false, t0)
			return decline(iso8583.RCSystemError)
		}
		block, err := pin.Translate(req.PINData, acqKey, iss.PINKey)
		if err != nil {
			tr.step("translate_pin", err.Error(), false, t0)
			return decline(iso8583.RCSystemError)
		}
		fwd.PINData = block
		tr.step("translate_pin", fmt.Sprintf("acquirer %s key → %s key", req.AcquirerID, iss.ID), true, t0)
	}

	// 4. Send to the issuer, unless its circuit breaker is open.
	t0 = s.Now()
	if !iss.Breaker.Allow() {
		tr.step("breaker", fmt.Sprintf("%s circuit breaker open; request not sent", iss.Name), false, t0)
		return decline(iso8583.RCIssuerUnavailable)
	}
	before := iss.Breaker.State()
	tr.step("send", "0100 forwarded to "+iss.Name, true, t0)
	t0 = s.Now()
	ctx, cancel := context.WithTimeout(context.Background(), s.IssuerTimeout)
	iresp, err := iss.Client.Authorize(ctx, fwd)
	cancel()
	issuerUS := s.Now().Sub(t0).Microseconds()
	s.Recorder.update(tr.rec, func(r *Record) { r.IssuerUS = issuerUS })
	if err != nil {
		if iss.Breaker.Failure() {
			s.Recorder.PublishHealth()
		}
		if errors.Is(err, ErrIssuerTimeout) {
			tr.step("issuer", fmt.Sprintf("no response from %s within %s", iss.Name, s.IssuerTimeout), false, t0)
			// D9: the issuer may still approve and hold funds, so reverse it.
			go s.reverse(iss, fwd, tr, "issuer timeout")
		} else {
			tr.step("issuer", fmt.Sprintf("%s: %v", iss.Name, err), false, t0)
		}
		return s.decline(req, id, iso8583.RCIssuerUnavailable), &fwd
	}
	iss.Breaker.Success()
	if before != BreakerClosed {
		s.Recorder.PublishHealth()
	}
	if _, known := iso8583.ResponseCodes[iresp.ResponseCode]; !known || iresp.NetworkTxnID != id {
		tr.step("issuer", fmt.Sprintf("invalid response from %s (code %q)", iss.Name, iresp.ResponseCode), false, t0)
		go s.reverse(iss, fwd, tr, "invalid issuer response")
		return s.decline(req, id, iso8583.RCSystemError), &fwd
	}
	text := iso8583.ResponseText(iresp.ResponseCode)
	detail := fmt.Sprintf("0110 %s %s", iresp.ResponseCode, text)
	if iresp.AuthCode != "" {
		detail += ", auth code " + iresp.AuthCode
	}
	tr.step("issuer", detail, iso8583.IsApproved(iresp.ResponseCode), t0)

	// The acquirer gets back the number it sent: the token, never the card number.
	resp := iresp
	resp.PAN = req.PAN
	resp.NetworkTxnID = id
	resp.ResponseText = text
	return resp, &fwd
}

// detokenize checks a device token and returns the card it stands for. On
// failure it returns a response code and a reason.
func (s *Switch) detokenize(req iso8583.AuthRequest) (Token, string, string) {
	tok, ok := s.Vault.Lookup(req.PAN)
	wallet := iso8583.Wallets[tok.Wallet]
	switch {
	case !ok:
		return tok, iso8583.RCInvalidCard, "token not found in vault"
	case !tok.Active:
		return tok, iso8583.RCRestrictedCard, wallet + " token is suspended"
	case req.Expiry != tok.TokenExpiry:
		return tok, iso8583.RCExpiredCard, "token expiry does not match"
	case req.WalletProvider != tok.Wallet:
		return tok, iso8583.RCDoNotHonor, fmt.Sprintf("token belongs to %s, used as %q", wallet, req.WalletProvider)
	case req.EntryMode != iso8583.EntryContactless && req.EntryMode != iso8583.EntryEcommerce:
		return tok, iso8583.RCDoNotHonor, "token domain: wallet tokens are contactless or in-app only"
	case req.ARQC == "":
		return tok, iso8583.RCDoNotHonor, "token used without a cryptogram"
	}
	return tok, "", fmt.Sprintf("%s token → card %s", wallet, card.Mask(tok.PAN))
}

// reverse sends an 0420 to the issuer until it acknowledges, so a hold the
// acquirer will never collect is released.
func (s *Switch) reverse(iss *Issuer, fwd iso8583.AuthRequest, tr *tracer, reason string) {
	adv := iso8583.ReversalAdvice{
		MTI:          iso8583.MTIReversalAdvice,
		NetworkTxnID: fwd.NetworkTxnID,
		PAN:          fwd.PAN,
		Amount:       fwd.Amount,
		STAN:         fwd.STAN,
		RRN:          fwd.RRN,
		Reason:       reason,
	}
	for attempt := 1; attempt <= 5; attempt++ {
		t0 := s.Now()
		ctx, cancel := context.WithTimeout(context.Background(), s.IssuerTimeout)
		resp, err := iss.Client.Reverse(ctx, adv)
		cancel()
		if err == nil {
			detail := "0420 acknowledged: hold released"
			if !resp.Matched {
				detail = "0420 acknowledged: no hold yet, issuer will decline the late authorization"
			}
			tr.step("reversal", detail, true, t0)
			return
		}
		tr.step("reversal", fmt.Sprintf("0420 attempt %d failed: %v", attempt, err), false, t0)
		time.Sleep(s.ReversalRetry)
	}
}

func (s *Switch) decline(req iso8583.AuthRequest, id, code string) iso8583.AuthResponse {
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
		NetworkTxnID:     id,
		ResponseText:     iso8583.ResponseText(code),
	}
}

func validate(r iso8583.AuthRequest) error {
	switch {
	case r.MTI != iso8583.MTIAuthRequest:
		return fmt.Errorf("MTI %q is not 0100", r.MTI)
	case card.ValidatePAN(r.PAN) != nil:
		return fmt.Errorf("DE2: %v", card.ValidatePAN(r.PAN))
	case r.Amount <= 0:
		return errors.New("DE4: amount must be positive")
	case !isDigits(r.STAN, 6):
		return errors.New("DE11: STAN must be 6 digits")
	case !isDigits(r.TransmissionTime, 10):
		return errors.New("DE7: transmission time must be MMDDhhmmss")
	case r.AcquirerID == "":
		return errors.New("DE32: acquirer ID is required")
	case len(r.RRN) != 12:
		return errors.New("DE37: RRN must be 12 characters")
	case iso8583.Currencies[r.Currency] == "":
		return fmt.Errorf("DE49: unsupported currency %q", r.Currency)
	}
	return nil
}

func isDigits(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// NewTxnID returns a UUIDv7: time-ordered, so IDs sort by when the network
// received the transaction (D3).
func NewTxnID(now time.Time) string {
	var b [16]byte
	binary.BigEndian.PutUint64(b[:8], uint64(now.UnixMilli())<<16)
	rand.Read(b[6:])
	b[6] = 0x70 | b[6]&0x0F // version 7
	b[8] = 0x80 | b[8]&0x3F // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
