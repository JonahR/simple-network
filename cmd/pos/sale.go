package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/JonahR/simple-network/internal/card"
	"github.com/JonahR/simple-network/internal/iso8583"
	"github.com/JonahR/simple-network/internal/pin"
)

const (
	maxAmount   = 99_999_999 // Largest sale the terminal accepts, in cents
	maxCashback = 20_000     // Cash back limit, in cents
)

// fieldErrors maps form field names to error messages.
type fieldErrors map[string]string

// buildAuthRequest validates a sale and builds an 0100 message. It returns
// field errors when the input is invalid.
func (s *server) buildAuthRequest(in SaleInput, now time.Time) (iso8583.AuthRequest, fieldErrors) {
	errs := fieldErrors{}
	entry := in.EntryMode
	if _, ok := iso8583.EntryModes[entry]; !ok {
		errs["entry_mode"] = "unsupported entry mode"
	}

	// Card-on-file sales use the merchant's stored card instead of form input.
	cardNumber, expiryInput := in.CardNumber, in.Expiry
	if entry == iso8583.EntryCredentialOnFile {
		if stored, ok := cardOnFile(in.CardOnFileID); ok {
			cardNumber, expiryInput = stored.PAN, stored.Expiry
		} else {
			errs["card_on_file"] = "choose a saved card"
		}
		if _, ok := iso8583.COFIndicators[in.COFIndicator]; !ok {
			errs["cof_indicator"] = "choose who initiated the charge"
		}
	} else if in.CardOnFileID != "" || in.COFIndicator != "" {
		errs["form"] = "saved card sent without credential-on-file entry"
	}

	pan := card.Normalize(cardNumber)
	if err := card.ValidatePAN(pan); err != nil {
		errs["card_number"] = err.Error()
	}
	expiry, err := card.ParseExpiry(expiryInput, now)
	if err != nil {
		errs["expiry"] = err.Error()
	}
	product := card.ProductFor(pan)

	amount, err := parseAmount(in.Amount)
	if err != nil {
		errs["amount"] = err.Error()
	}
	if _, ok := iso8583.Currencies[in.Currency]; !ok {
		errs["currency"] = "unsupported currency"
	}

	arqc, track2 := checkCardRead(in, pan, expiry, errs)
	cvv := checkCVV(in, errs)
	clearPIN := checkPIN(in, product, errs)
	cashback := checkCashback(in, product, clearPIN != "", errs)
	fleet := checkFleet(in, product, errs)
	healthcare := checkHealthcare(in, product, amount, errs)
	if amount+cashback > maxAmount {
		errs["amount"] = "amount exceeds terminal limit"
	}
	if len(errs) > 0 {
		return iso8583.AuthRequest{}, errs
	}

	var pinBlock string
	if clearPIN != "" {
		if pinBlock, err = pin.Encrypt(clearPIN, pan, s.pinKey); err != nil {
			return iso8583.AuthRequest{}, fieldErrors{"pin": err.Error()}
		}
	}

	txn := iso8583.TxnPurchase
	var extra []iso8583.AdditionalAmount
	if cashback > 0 {
		txn = iso8583.TxnPurchaseCashback
		extra = append(extra, iso8583.AdditionalAmount{Type: iso8583.AmountCashback, Amount: cashback})
	}
	if healthcare > 0 {
		extra = append(extra, iso8583.AdditionalAmount{Type: iso8583.AmountHealthcare, Amount: healthcare})
	}

	stan := s.nextSTAN()
	utc := now.UTC()
	local := now.In(s.location) // DE12/DE13 are the merchant's local time
	t := s.terminal
	return iso8583.AuthRequest{
		MTI:              iso8583.MTIAuthRequest,
		PAN:              pan,
		ProcessingCode:   iso8583.ProcessingCode(txn, accountType(product)),
		Amount:           amount + cashback,
		TransmissionTime: utc.Format("0102150405"),
		STAN:             stan,
		LocalTime:        local.Format("150405"),
		LocalDate:        local.Format("0102"),
		Expiry:           expiry,
		MCC:              t.MCC,
		EntryMode:        entry,
		Track2:           track2,
		// RRN: last digit of year, day of year, hour, then the STAN (12 chars).
		RRN:               fmt.Sprintf("%s%03d%02d%s", utc.Format("2006")[3:], utc.YearDay(), utc.Hour(), stan),
		TerminalID:        t.TerminalID,
		MerchantID:        t.MerchantID,
		MerchantNameLoc:   fmt.Sprintf("%-25.25s%-13.13s%2.2s", t.MerchantName, t.City, t.Country),
		Fleet:             fleet,
		COFIndicator:      in.COFIndicator,
		Currency:          in.Currency,
		PINData:           pinBlock,
		AdditionalAmounts: extra,
		ARQC:              arqc,
		CVV2:              cvv,
		CardholderName:    strings.TrimSpace(in.CardholderName),
		WalletProvider:    in.Wallet,
	}, nil
}

// checkCardRead validates what the card or phone supplied for the entry mode:
// a cryptogram for chip, contactless, and wallet payments, and track 2 data
// for swipes. It returns the normalized cryptogram and track 2.
//
// Chip, contactless, and in-store wallet payments carry an 8-byte EMV
// cryptogram as 16 hex characters. In-app and online wallet payments carry a
// 20-byte token cryptogram (Visa TAVV, Mastercard UCAF) as 28 base64
// characters, which is case-sensitive and so is not upper-cased.
func checkCardRead(in SaleInput, pan, expiry string, errs fieldErrors) (arqc, track2 string) {
	entry := in.EntryMode
	if in.Wallet != "" {
		if _, ok := iso8583.Wallets[in.Wallet]; !ok {
			errs["form"] = "unsupported wallet"
		}
		if entry != iso8583.EntryContactless && entry != iso8583.EntryEcommerce {
			errs["entry_mode"] = "wallet payments must be contactless or e-commerce"
		}
	}

	arqc = strings.TrimSpace(in.Cryptogram)
	needsARQC := entry == iso8583.EntryChip || entry == iso8583.EntryContactless || in.Wallet != ""
	inApp := in.Wallet != "" && entry == iso8583.EntryEcommerce
	if !inApp {
		arqc = strings.ToUpper(arqc)
	}
	switch {
	case needsARQC && arqc == "" && in.Wallet == "":
		errs["entry_mode"] = "chip and contactless reads come from the card: insert or tap one from the wallet"
	case inApp && !isBase64(arqc, 20):
		errs["form"] = "in-app wallet cryptogram must be 28 base64 characters"
	case needsARQC && !inApp && !isHex(arqc, 16):
		errs["form"] = "cryptogram must be 16 hex characters"
	case !needsARQC && arqc != "":
		errs["form"] = "cryptograms are only sent for chip, contactless, and wallet payments"
	}

	track2 = strings.TrimSpace(in.Track2)
	if entry == iso8583.EntryMagstripe {
		if track2 == "" {
			errs["entry_mode"] = "swipe a card from the wallet to read its magnetic stripe"
		} else if err := checkTrack2(track2, pan, expiry); err != nil {
			errs["form"] = err.Error()
		} else if chipCard(track2) {
			errs["entry_mode"] = "this card has a chip: insert or tap it instead of swiping"
		}
	} else if track2 != "" {
		errs["form"] = "track 2 data is only sent for swiped cards"
	}
	return arqc, track2
}

// checkTrack2 checks that track 2 ("PAN=YYMM" + service code + discretionary
// data) matches the PAN and expiry the terminal is sending.
func checkTrack2(track2, pan, expiry string) error {
	trackPAN, rest, ok := strings.Cut(track2, "=")
	if !ok || len(rest) < 7 || strings.Trim(rest, "0123456789") != "" {
		return errors.New("track 2 data is malformed")
	}
	if trackPAN != pan || rest[:4] != expiry {
		return errors.New("track 2 data does not match the card number and expiry")
	}
	return nil
}

// chipCard reports whether track 2's service code starts with 2, meaning the
// card has a chip. A chip-capable terminal must read the chip, so swiping it
// is declined. (Real terminals allow a swipe as fallback after a failed chip
// read, flagged in DE22; this terminal doesn't model that.)
func chipCard(track2 string) bool {
	_, rest, _ := strings.Cut(track2, "=")
	return rest[4] == '2'
}

// checkCVV requires a CVV when a card is keyed or used online, since nothing
// else proves the cardholder has it. Wallets send a cryptogram instead, and
// card-on-file sales can never include one because CVVs may not be stored.
func checkCVV(in SaleInput, errs fieldErrors) string {
	cvv := strings.TrimSpace(in.CVV)
	keyed := in.EntryMode == iso8583.EntryManual || in.EntryMode == iso8583.EntryEcommerce
	switch {
	case in.EntryMode == iso8583.EntryCredentialOnFile && cvv != "":
		errs["cvv"] = "CVVs can't be stored, so card-on-file sales never include one"
	case cvv != "" || (keyed && in.Wallet == ""):
		if err := card.ValidateCVV(cvv); err != nil {
			errs["cvv"] = err.Error()
		}
	}
	return cvv
}

// checkPIN validates a PIN entered on the terminal's PIN pad. Debit cards
// require one for chip and swipe. Mobile wallets verify the cardholder on the
// phone instead, and keyed or stored cards have no PIN pad.
func checkPIN(in SaleInput, product card.Product, errs fieldErrors) string {
	p := strings.TrimSpace(in.PIN)
	entry := in.EntryMode
	onPINPad := in.Wallet == "" &&
		(entry == iso8583.EntryChip || entry == iso8583.EntryMagstripe || entry == iso8583.EntryContactless)
	required := product == card.Debit && in.Wallet == "" &&
		(entry == iso8583.EntryChip || entry == iso8583.EntryMagstripe)

	switch {
	case p == "" && required:
		errs["pin"] = "debit cards need a PIN for chip and swipe"
	case p == "":
	case !onPINPad:
		errs["pin"] = "a PIN can only be entered for a card read by the terminal"
	default:
		if err := pin.Validate(p); err != nil {
			errs["pin"] = err.Error()
		}
	}
	return p
}

// checkCashback validates cash back, which only debit cards with a PIN allow.
func checkCashback(in SaleInput, product card.Product, hasPIN bool, errs fieldErrors) int64 {
	s := strings.TrimSpace(in.Cashback)
	if s == "" {
		return 0
	}
	cashback, err := parseAmount(s)
	switch {
	case err != nil:
		errs["cashback"] = err.Error()
	case product != card.Debit:
		errs["cashback"] = "cash back is only available on debit cards"
	case !hasPIN:
		errs["cashback"] = "cash back requires the cardholder's PIN"
	case cashback > maxCashback:
		errs["cashback"] = "cash back is limited to $200.00"
	}
	return cashback
}

// checkFleet validates the prompts fleet cards require.
func checkFleet(in SaleInput, product card.Product, errs fieldErrors) *iso8583.FleetData {
	f := iso8583.FleetData{
		Odometer:  strings.TrimSpace(in.Odometer),
		VehicleID: strings.ToUpper(strings.TrimSpace(in.VehicleID)),
		DriverID:  strings.TrimSpace(in.DriverID),
	}
	if product != card.Fleet {
		if f != (iso8583.FleetData{}) {
			errs["form"] = "fleet prompts are only for fleet cards"
		}
		return nil
	}
	if !isDigits(f.Odometer, 1, 7) {
		errs["odometer"] = "odometer must be 1-7 digits"
	}
	if len(f.VehicleID) < 1 || len(f.VehicleID) > 17 || strings.Trim(f.VehicleID, "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ") != "" {
		errs["vehicle_id"] = "vehicle ID must be 1-17 letters or digits"
	}
	if !isDigits(f.DriverID, 4, 8) {
		errs["driver_id"] = "driver ID must be 4-8 digits"
	}
	return &f
}

// checkHealthcare validates the HSA/FSA-eligible amount, which can't exceed
// the sale amount.
func checkHealthcare(in SaleInput, product card.Product, amount int64, errs fieldErrors) int64 {
	s := strings.TrimSpace(in.HealthcareAmount)
	if product != card.Healthcare {
		if s != "" {
			errs["form"] = "healthcare amounts are only for HSA/FSA cards"
		}
		return 0
	}
	if s == "" {
		errs["healthcare_amount"] = "enter the HSA/FSA-eligible amount"
		return 0
	}
	h, err := parseAmount(s)
	switch {
	case err != nil:
		errs["healthcare_amount"] = err.Error()
	case amount > 0 && h > amount:
		errs["healthcare_amount"] = "eligible amount can't exceed the sale amount"
	}
	return h
}

// accountType is the DE3 "from account" for a card product.
func accountType(p card.Product) string {
	switch p {
	case card.Debit:
		return iso8583.AccountChecking
	case card.Credit, card.Fleet:
		return iso8583.AccountCredit
	default:
		return iso8583.AccountDefault
	}
}

// nextSTAN returns the next 6-digit system trace audit number, 000001-999999.
func (s *server) nextSTAN() string {
	n := s.stan.Add(1)
	return fmt.Sprintf("%06d", (n-1)%999_999+1)
}

// parseAmount converts a decimal string like "12.50" into cents without
// going through floating point.
func parseAmount(s string) (int64, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "$")
	whole, frac, hasFrac := strings.Cut(s, ".")
	if whole == "" {
		whole = "0"
	}
	if hasFrac && (len(frac) == 0 || len(frac) > 2) {
		return 0, errors.New("amount must have at most 2 decimal places")
	}
	frac += strings.Repeat("0", 2-len(frac))
	w, err1 := strconv.ParseUint(whole, 10, 63)
	f, err2 := strconv.ParseUint(frac, 10, 63)
	if err1 != nil || err2 != nil {
		return 0, errors.New("amount must be a number like 12.50")
	}
	cents := int64(w)*100 + int64(f)
	if cents <= 0 {
		return 0, errors.New("amount must be greater than zero")
	}
	if cents > maxAmount || w > maxAmount {
		return 0, errors.New("amount exceeds terminal limit")
	}
	return cents, nil
}

// isHex reports whether s is exactly n hexadecimal characters.
func isHex(s string, n int) bool {
	return len(s) == n && strings.Trim(s, "0123456789ABCDEFabcdef") == ""
}

// isBase64 reports whether s is standard base64 encoding exactly n bytes.
func isBase64(s string, n int) bool {
	b, err := base64.StdEncoding.DecodeString(s)
	return err == nil && len(b) == n
}

// isDigits reports whether s is between min and max decimal digits long.
func isDigits(s string, min, max int) bool {
	return len(s) >= min && len(s) <= max && strings.Trim(s, "0123456789") == ""
}
