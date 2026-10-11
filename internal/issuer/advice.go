package issuer

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/JonahR/simple-network/internal/iso8583"
)

// Advise honors an 0120: an authorization the network approved on the
// bank's behalf (stand-in) while the bank couldn't answer. Stand-in
// approvals bind the issuer, so the hold is placed even past the available
// credit. The bank finds the account by card hash; the advice never carries
// the card number. An advice repeated, or arriving after the bank's own late
// approval, places nothing new.
func (b *Bank) Advise(adv iso8583.AuthAdvice) iso8583.AuthAdviceResponse {
	b.mu.Lock()
	defer b.mu.Unlock()
	resp := iso8583.AuthAdviceResponse{MTI: iso8583.MTIAuthAdviceResponse, NetworkTxnID: adv.NetworkTxnID}
	if _, ok := b.holds[adv.NetworkTxnID]; ok {
		resp.Held, resp.Note = true, "already held: the bank's own late approval placed it"
		return resp
	}
	if _, ok := b.reversed[adv.NetworkTxnID]; ok {
		resp.Note = "the sale was reversed before the advice arrived"
		return resp
	}
	var acct *Account
	for _, a := range b.accounts {
		if sum := sha256.Sum256([]byte(a.PAN)); hex.EncodeToString(sum[:]) == adv.PANHash {
			acct = a
			break
		}
	}
	d := Decision{
		Time: b.now().UTC(), NetworkTxnID: adv.NetworkTxnID, PAN: adv.PAN, EntryMode: adv.EntryMode,
		Merchant: strings.TrimSpace(firstN(adv.MerchantNameLoc, 25)), MCC: adv.MCC, Currency: adv.Currency,
		Requested: adv.Amount, Approved: adv.Amount, ResponseCode: iso8583.RCApproved,
		ResponseText: "Approved in stand-in", AuthCode: adv.AuthCode, Hold: "none",
	}
	if acct == nil {
		d.Checks = []Check{{Name: "Stand-in", Result: Fail, Detail: "0120 advice for a card this bank has no account for"}}
		b.log(d)
		resp.Note = "no account for this card"
		return resp
	}
	d.AccountID, d.Product, d.Limit, d.AvailableBefore = acct.ID, string(acct.Product), acct.Limit, acct.Available
	acct.Available -= adv.Amount
	b.holds[adv.NetworkTxnID] = hold{pan: acct.PAN, amount: adv.Amount}
	d.AvailableAfter, d.Hold = acct.Available, "active"
	d.Checks = []Check{{Name: "Stand-in", Result: Pass, Detail: "The network approved this on the bank's behalf (" + adv.Reason +
		"), within the limits the bank agreed to. The 0120 advice binds the bank, so the hold is placed even past the available credit."}}
	b.log(d)
	resp.Held = true
	return resp
}

// alreadyHeld answers an authorization that already has a hold, as when an
// 0120 arrived before the bank's own late reply: approved once, no second
// hold. The caller holds b.mu.
func (b *Bank) alreadyHeld(req iso8583.AuthRequest) (iso8583.AuthResponse, bool) {
	h, ok := b.holds[req.NetworkTxnID]
	if !ok || req.NetworkTxnID == "" || req.AuthType == iso8583.AuthIncremental {
		return iso8583.AuthResponse{}, false
	}
	return iso8583.AuthResponse{
		MTI: iso8583.MTIAuthResponse, PAN: req.PAN, ProcessingCode: req.ProcessingCode, Amount: h.amount,
		TransmissionTime: req.TransmissionTime, STAN: req.STAN, RRN: req.RRN, ResponseCode: iso8583.RCApproved,
		TerminalID: req.TerminalID, MerchantID: req.MerchantID, Currency: req.Currency, NetworkTxnID: req.NetworkTxnID,
		ResponseText: iso8583.ResponseText(iso8583.RCApproved),
	}, true
}

// log appends a decision. The caller holds b.mu.
func (b *Bank) log(d Decision) {
	b.decisions = append(b.decisions, d)
	if len(b.decisions) > maxLog {
		b.decisions = b.decisions[1:]
	}
}
