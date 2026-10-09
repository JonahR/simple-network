package main

import (
	"errors"
	"fmt"
	"log"

	"github.com/JonahR/simple-network/internal/iso8583"
)

// TerminalReverse handles an 0420 reversal advice from a terminal: a void
// (reason 17, customer cancellation) or the terminal's own timeout (68). The
// terminal names the original in DE90 by its own STAN and transmission time;
// the acquirer renumbered the network leg, so it looks the leg up and sends
// the network its own 0420 for it, retrying until acknowledged.
//
// The 0430 comes back at once. Matched reports whether the acquirer had sent
// the original to the network; if not, nothing can be held and nothing is
// forwarded. A repeated advice is acknowledged again without a second 0420.
func (a *acquirer) TerminalReverse(adv iso8583.ReversalAdvice) (iso8583.ReversalResponse, error) {
	o := adv.OriginalData
	reason := adv.ResponseCode
	if reason == "" {
		reason = iso8583.RCLateResponse
	}
	switch {
	case adv.MTI != iso8583.MTIReversalAdvice:
		return iso8583.ReversalResponse{}, fmt.Errorf("MTI %q is not 0420", adv.MTI)
	case o == nil || o.STAN == "" || o.TransmissionTime == "":
		return iso8583.ReversalResponse{}, errors.New("DE90: original STAN and transmission time are required")
	case iso8583.ResponseCodes[reason] == (iso8583.ResponseCode{}):
		return iso8583.ReversalResponse{}, fmt.Errorf("DE39: unknown reversal reason %q", reason)
	}
	if m, ok := a.merchant(adv.MerchantID); !ok || !m.HasTerminal(adv.TerminalID) {
		return iso8583.ReversalResponse{}, errors.New("merchant or terminal is not on file")
	}

	ack := iso8583.ReversalResponse{MTI: iso8583.MTIReversalResponse, STAN: adv.STAN, ResponseCode: iso8583.RCApproved}
	key := adv.MerchantID + "|" + adv.TerminalID + "|" + o.STAN + "|" + o.TransmissionTime
	a.mu.Lock()
	l := a.legs[key]
	if l == nil {
		a.mu.Unlock()
		log.Printf("terminal reversal terminal=%s orig_stan=%s reason=%s: original never sent to the network", adv.TerminalID, o.STAN, reason)
		return ack, nil // Matched=false: nothing reached the network, so nothing is held
	}
	ack.Matched = true
	rec, out := l.rec, l.out
	if rec == nil {
		// Still waiting on the network; Authorize reverses it when the answer comes.
		if l.reason == "" {
			l.reason = reason
		}
		a.mu.Unlock()
		return ack, nil
	}
	ack.NetworkTxnID = rec.Response.NetworkTxnID
	a.mu.Unlock()

	why := "The acquirer sends the network its own 0420, with DE90 naming the network leg (STAN " + out.STAN + ")."
	if !a.reverseFor(rec, out, reason, why) {
		log.Printf("terminal reversal terminal=%s orig_stan=%s: already being reversed", adv.TerminalID, o.STAN)
	}
	return ack, nil
}
