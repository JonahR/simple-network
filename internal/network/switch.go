package network

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
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
	// An unacknowledged 0420 is resent after ReversalRetry, doubling each
	// time up to ReversalRetryMax (default 1s and 30s).
	ReversalRetry    time.Duration
	ReversalRetryMax time.Duration
	Recorder         *Recorder
	Now              func() time.Time // Injectable clock (D20)

	stan atomic.Uint32 // DE11 for messages the network originates

	mu       sync.Mutex
	seen     map[string]*attempt // Idempotency: acquirer ID + STAN + DE7 (D3)
	seenFIFO []string
	bgCtx    context.Context // Cancelled by Close
	bgCancel context.CancelFunc
	closed   bool
	bg       sync.WaitGroup
}

// attempt is one authorization, keyed by acquirer ID + STAN + DE7. An
// acquirer's 0420 can create it before the 0100 arrives.
type attempt struct {
	done     chan struct{} // Closed once resp is set
	started  bool          // An 0100 with this key arrived; guarded by Switch.mu
	reversed bool          // An acquirer 0420 named this key; guarded by Switch.mu
	resp     iso8583.AuthResponse
	rec      *Record
	tr       *tracer

	// Set when the issuer approved, so an acquirer reversal can be forwarded.
	// The PIN block, CVV2, track data, and cryptogram are dropped.
	fwd *iso8583.AuthRequest
	iss *Issuer
}

const maxSeen = 10_000

// Authorize processes an 0100 and returns the 0110 to send back to the
// acquirer. A repeated request (same acquirer, STAN, and transmission time)
// gets the original response and never reaches the issuer twice.
func (s *Switch) Authorize(ctx context.Context, req iso8583.AuthRequest) iso8583.AuthResponse {
	s.mu.Lock()
	a := s.attemptFor(idemKey(req.AcquirerID, req.STAN, req.TransmissionTime))
	if a.started {
		s.mu.Unlock()
		select {
		case <-a.done:
		case <-ctx.Done():
			return s.decline(req, "", iso8583.RCDuplicate)
		}
		s.Recorder.update(a.rec, func(r *Record) { r.Duplicates++ })
		return a.resp
	}
	a.started = true
	early := a.reversed
	s.mu.Unlock()

	a.resp = s.process(req, a, early)
	close(a.done)
	return a.resp
}

func idemKey(acquirerID, stan, transmissionTime string) string {
	return acquirerID + "|" + stan + "|" + transmissionTime
}

// attemptFor returns the attempt for key, creating it if needed. The caller
// holds s.mu.
func (s *Switch) attemptFor(key string) *attempt {
	if s.seen == nil {
		s.seen = map[string]*attempt{}
	}
	if a, ok := s.seen[key]; ok {
		return a
	}
	a := &attempt{done: make(chan struct{})}
	s.seen[key] = a
	s.seenFIFO = append(s.seenFIFO, key)
	if len(s.seenFIFO) > maxSeen {
		delete(s.seen, s.seenFIFO[0])
		s.seenFIFO = s.seenFIFO[1:]
	}
	return a
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

// process handles a new 0100. early means the acquirer already sent an 0420
// for it, so it is declined without reaching the issuer.
func (s *Switch) process(req iso8583.AuthRequest, a *attempt, early bool) iso8583.AuthResponse {
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
	a.tr = tr

	var resp iso8583.AuthResponse
	var fwd *iso8583.AuthRequest
	if early {
		tr.step("reversal", fmt.Sprintf("acquirer %s sent an 0420 for this authorization before the 0100 arrived; declined without contacting the issuer", req.AcquirerID), false, start)
		resp = s.decline(req, id, iso8583.RCDoNotHonor)
	} else {
		resp, fwd = s.authorize(req, id, tr)
	}

	began := s.Now()
	redacted := resp.Redacted(card.Mask)
	var issuerID string
	s.Recorder.update(rec, func(r *Record) {
		issuerID = r.IssuerID
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
	if fwd != nil && iso8583.IsApproved(resp.ResponseCode) {
		f := *fwd
		f.PINData, f.CVV2, f.Track2, f.ARQC = "", "", "", ""
		a.fwd, a.iss = &f, s.Issuers[issuerID]
	}
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
			s.background(func(ctx context.Context) { s.reverse(ctx, iss, fwd, tr, iso8583.RCLateResponse) })
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
		s.background(func(ctx context.Context) { s.reverse(ctx, iss, fwd, tr, iso8583.RCSuspectedMalfunc) })
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

// Reverse handles an 0420 from an acquirer that never got, or could not use,
// the 0110 for an authorization. It finds the original by DE90 (acquirer ID +
// STAN + DE7, the idempotency key) and, if the network approved it, sends the
// issuer the network's own 0420 in the background. The 0430 comes back at
// once: it acknowledges the advice, not the issuer's release.
//
// A reversal that arrives before its authorization is remembered, and the
// late 0100 is declined without reaching the issuer. A repeated 0420, or one
// for a declined authorization, is acknowledged and otherwise ignored.
func (s *Switch) Reverse(adv iso8583.ReversalAdvice) (iso8583.ReversalResponse, error) {
	if err := validateReversal(adv); err != nil {
		return iso8583.ReversalResponse{}, err
	}
	code := adv.ResponseCode
	if code == "" {
		code = iso8583.RCLateResponse
	}
	o := adv.OriginalData

	s.mu.Lock()
	a := s.attemptFor(idemKey(o.AcquirerID, o.STAN, o.TransmissionTime))
	started, repeat := a.started, a.reversed
	a.reversed = true
	s.mu.Unlock()

	ack := iso8583.ReversalResponse{
		MTI:          iso8583.MTIReversalResponse,
		STAN:         adv.STAN,
		ResponseCode: iso8583.RCApproved,
		Matched:      started,
	}
	if !started || repeat {
		return ack, nil
	}
	select {
	case <-a.done:
		ack.NetworkTxnID = a.resp.NetworkTxnID
	default: // Still waiting on the issuer
	}
	s.background(func(ctx context.Context) {
		select {
		case <-a.done:
		case <-ctx.Done():
			return
		}
		t0 := s.Now()
		from := fmt.Sprintf("0420 from acquirer %s (STAN %s, reason %s %s)", o.AcquirerID, adv.STAN, code, iso8583.ResponseText(code))
		if a.fwd == nil || a.iss == nil {
			a.tr.step("reversal", from+": authorization was not approved, nothing to reverse", true, t0)
			return
		}
		a.tr.step("reversal", from+": forwarding to "+a.iss.Name, true, t0)
		s.reverse(ctx, a.iss, *a.fwd, a.tr, code)
	})
	return ack, nil
}

// reverse sends the network's own 0420 to the issuer, so a hold the acquirer
// will never collect is released. It resends the same advice, backing off
// from ReversalRetry to ReversalRetryMax, until the issuer acknowledges or ctx
// is cancelled at shutdown. Every attempt is a step on the original trace.
func (s *Switch) reverse(ctx context.Context, iss *Issuer, fwd iso8583.AuthRequest, tr *tracer, code string) {
	adv := iso8583.ReversalAdvice{
		MTI:              iso8583.MTIReversalAdvice,
		NetworkTxnID:     fwd.NetworkTxnID,
		PAN:              fwd.PAN,
		Amount:           fwd.Amount,
		TransmissionTime: s.Now().UTC().Format("0102150405"),
		STAN:             s.nextSTAN(),
		AcquirerID:       fwd.AcquirerID,
		RRN:              fwd.RRN,
		ResponseCode:     code,
		OriginalData: &iso8583.OriginalData{
			MTI:              fwd.MTI,
			STAN:             fwd.STAN,
			TransmissionTime: fwd.TransmissionTime,
			AcquirerID:       fwd.AcquirerID,
		},
	}
	sent := fmt.Sprintf("0420 STAN %s (reason %s)", adv.STAN, code)
	delay, maxDelay := s.ReversalRetry, s.ReversalRetryMax
	if delay <= 0 {
		delay = time.Second
	}
	if maxDelay <= 0 {
		maxDelay = 30 * time.Second
	}
	for attempt := 1; ; attempt++ {
		t0 := s.Now()
		actx, cancel := context.WithTimeout(ctx, s.IssuerTimeout)
		resp, err := iss.Client.Reverse(actx, adv)
		cancel()
		if err == nil {
			detail := sent + " acknowledged: hold released"
			if !resp.Matched {
				detail = sent + " acknowledged: no hold yet, issuer will decline the late authorization"
			}
			tr.step("reversal", detail, true, t0)
			s.Recorder.update(tr.rec, func(r *Record) { r.Reversed = true })
			return
		}
		if ctx.Err() != nil {
			tr.step("reversal", sent+" not acknowledged; retries stopped because the network is shutting down", false, t0)
			return
		}
		tr.step("reversal", fmt.Sprintf("%s attempt %d failed: %v; retrying in %s", sent, attempt, err, delay), false, t0)
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			tr.step("reversal", sent+" not acknowledged; retries stopped because the network is shutting down", false, s.Now())
			return
		}
		delay = min(delay*2, maxDelay)
	}
}

// nextSTAN returns a DE11 for a message the network originates.
func (s *Switch) nextSTAN() string {
	for {
		if n := s.stan.Add(1) % 1_000_000; n != 0 {
			return fmt.Sprintf("%06d", n)
		}
	}
}

// background runs fn in a goroutine whose context Close cancels. Close waits
// for it to return.
func (s *Switch) background(fn func(ctx context.Context)) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	if s.bgCtx == nil {
		s.bgCtx, s.bgCancel = context.WithCancel(context.Background())
	}
	ctx := s.bgCtx
	s.bg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.bg.Done()
		fn(ctx)
	}()
}

// Close stops background work, such as reversals still waiting for an
// issuer's acknowledgement, and waits for it to finish.
func (s *Switch) Close() {
	s.mu.Lock()
	s.closed = true
	cancel := s.bgCancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.bg.Wait()
}

func (s *Switch) decline(req iso8583.AuthRequest, id, code string) iso8583.AuthResponse {
	return iso8583.AuthResponse{
		MTI:              iso8583.MTIAuthResponse,
		PAN:              req.PAN,
		ProcessingCode:   req.ProcessingCode,
		Amount:           req.Amount, // DE4 echoes the requested amount on a decline
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
	case !isAcquirerID(r.AcquirerID):
		return errors.New("DE32: acquirer ID must be 1-11 digits")
	case len(r.RRN) != 12:
		return errors.New("DE37: RRN must be 12 characters")
	case iso8583.Currencies[r.Currency] == "":
		return fmt.Errorf("DE49: unsupported currency %q", r.Currency)
	}
	return nil
}

// validateReversal checks an acquirer's 0420.
func validateReversal(adv iso8583.ReversalAdvice) error {
	o := adv.OriginalData
	switch {
	case adv.MTI != iso8583.MTIReversalAdvice:
		return fmt.Errorf("MTI %q is not 0420", adv.MTI)
	case !isDigits(adv.STAN, 6):
		return errors.New("DE11: STAN must be 6 digits")
	case !isDigits(adv.TransmissionTime, 10):
		return errors.New("DE7: transmission time must be MMDDhhmmss")
	case adv.AcquirerID != "" && !isAcquirerID(adv.AcquirerID):
		return errors.New("DE32: acquirer ID must be 1-11 digits")
	case adv.ResponseCode != "" && iso8583.ResponseCodes[adv.ResponseCode] == (iso8583.ResponseCode{}):
		return fmt.Errorf("DE39: unknown reversal reason %q", adv.ResponseCode)
	case o == nil:
		return errors.New("DE90: original data is required")
	case o.MTI != "" && o.MTI != iso8583.MTIAuthRequest:
		return fmt.Errorf("DE90: original MTI %q is not 0100", o.MTI)
	case !isDigits(o.STAN, 6):
		return errors.New("DE90: original STAN must be 6 digits")
	case !isDigits(o.TransmissionTime, 10):
		return errors.New("DE90: original transmission time must be MMDDhhmmss")
	case !isAcquirerID(o.AcquirerID):
		return errors.New("DE90: original acquirer ID must be 1-11 digits")
	case adv.AcquirerID != "" && adv.AcquirerID != o.AcquirerID:
		return errors.New("DE32: acquirer may only reverse its own authorizations")
	}
	return nil
}

// isAcquirerID reports whether s is a valid DE32: n..11, 1-11 digits.
func isAcquirerID(s string) bool {
	return len(s) >= 1 && len(s) <= 11 && isDigits(s, len(s))
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
