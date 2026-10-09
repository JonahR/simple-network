// Package clearing turns authorizations into final, priced obligations and
// settles them (KT 05). Acquirers send clearing files of presentments; the
// network validates each file, matches each record to its authorization,
// prices interchange and network fees, posts double-entry ledger entries, and
// sends each issuer its records. At cutoff it nets every member's position,
// checks that the positions and network revenue sum to zero, and has the
// settlement bank move the money.
//
// The message types here are shared by the acquirer, network, and issuer.
package clearing

import (
	"errors"
	"fmt"
	"time"
)

// FileHeader identifies a clearing file and its sender.
type FileHeader struct {
	FileID     string    `json:"file_id"`
	AcquirerID string    `json:"acquirer_id"`
	Sequence   int       `json:"sequence"` // Per acquirer, starting at 1, no gaps
	Created    time.Time `json:"created"`
}

// Presentment is one clearing record: the acquirer saying "this sale
// happened, for this final amount, and here is the authorization behind it".
// It carries the network transaction ID instead of the card number, so the
// PAN stays out of the clearing system.
type Presentment struct {
	NetworkTxnID   string `json:"network_txn_id"`
	ARN            string `json:"arn"` // Acquirer reference number: the record's lifetime ID
	RRN            string `json:"rrn"`
	AuthCode       string `json:"auth_code"`
	PAN            string `json:"pan"` // Masked, for display only
	Amount         int64  `json:"amount"`
	Cashback       int64  `json:"cashback,omitempty"` // Part of Amount; earns no interchange
	Currency       string `json:"currency"`
	MerchantID     string `json:"merchant_id"`
	MerchantName   string `json:"merchant_name"`
	MCC            string `json:"mcc"`
	EntryMode      string `json:"entry_mode"`
	ProcessingCode string `json:"processing_code"`
	TxnDate        string `json:"txn_date"` // YYYY-MM-DD
}

// FileTrailer carries the file's control totals.
type FileTrailer struct {
	RecordCount int   `json:"record_count"`
	HashTotal   int64 `json:"hash_total"` // Sum of record amounts
}

// File is a clearing file from an acquirer.
type File struct {
	Header  FileHeader    `json:"header"`
	Records []Presentment `json:"records"`
	Trailer FileTrailer   `json:"trailer"`
}

// NewFile builds a file with its trailer totals filled in.
func NewFile(h FileHeader, records []Presentment) File {
	f := File{Header: h, Records: records}
	f.Trailer.RecordCount = len(records)
	for _, r := range records {
		f.Trailer.HashTotal += r.Amount
	}
	return f
}

// CheckControls verifies the trailer against the records.
func (f File) CheckControls() error {
	if f.Header.FileID == "" || f.Header.AcquirerID == "" {
		return errors.New("header needs a file ID and acquirer ID")
	}
	var total int64
	for _, r := range f.Records {
		total += r.Amount
	}
	if f.Trailer.RecordCount != len(f.Records) {
		return fmt.Errorf("trailer says %d records, file has %d", f.Trailer.RecordCount, len(f.Records))
	}
	if f.Trailer.HashTotal != total {
		return fmt.Errorf("trailer hash total %d does not match records total %d", f.Trailer.HashTotal, total)
	}
	return nil
}

// File statuses in an acknowledgement.
const (
	FileAccepted  = "accepted"
	FileRejected  = "rejected"
	FileDuplicate = "duplicate" // Seen before; this is the original acknowledgement
)

// Rejection is a clearing record the network would not clear.
type Rejection struct {
	NetworkTxnID string `json:"network_txn_id"`
	ARN          string `json:"arn"`
	Amount       int64  `json:"amount"`
	Reason       string `json:"reason"`
}

// Ack is the network's acknowledgement of a clearing file.
type Ack struct {
	FileID         string      `json:"file_id"`
	Status         string      `json:"status"`
	Reason         string      `json:"reason,omitempty"` // Why a whole file was rejected
	CycleID        string      `json:"cycle_id,omitempty"`
	Accepted       int         `json:"accepted"`
	AcceptedAmount int64       `json:"accepted_amount"`
	Rejected       []Rejection `json:"rejected"`
}

// IssuerRecord is a cleared transaction as sent to its issuer.
type IssuerRecord struct {
	NetworkTxnID string `json:"network_txn_id"`
	ARN          string `json:"arn"`
	Amount       int64  `json:"amount"` // Final amount to post to the cardholder
	Currency     string `json:"currency"`
	MerchantName string `json:"merchant_name"`
	Interchange  int64  `json:"interchange"` // What the issuer earns
	NetworkFee   int64  `json:"network_fee"` // What the issuer pays the network
}

// IssuerFile is the network's outgoing clearing file to one issuer.
type IssuerFile struct {
	FileID   string         `json:"file_id"`
	CycleID  string         `json:"cycle_id"`
	IssuerID string         `json:"issuer_id"`
	Records  []IssuerRecord `json:"records"`
}

// IssuerAck is an issuer's reply to an outgoing file.
type IssuerAck struct {
	FileID string `json:"file_id"`
	Posted int    `json:"posted"`
	// Unmatched lists records posted without a matching hold, which gives the
	// issuer a chargeback right.
	Unmatched []string `json:"unmatched"`
}

// AdviceItem is one cleared transaction in an acquirer's settlement advice.
type AdviceItem struct {
	NetworkTxnID string `json:"network_txn_id"`
	ARN          string `json:"arn"`
	MerchantID   string `json:"merchant_id"`
	Amount       int64  `json:"amount"`
	Interchange  int64  `json:"interchange"`
	NetworkFee   int64  `json:"network_fee"`
	ProgramID    string `json:"program_id"`
}

// Advice tells an acquirer what it will receive for a settled cycle.
type Advice struct {
	CycleID    string       `json:"cycle_id"`
	ValueDate  string       `json:"value_date"`
	AcquirerID string       `json:"acquirer_id"`
	Currency   string       `json:"currency"`
	Gross      int64        `json:"gross"`
	Net        int64        `json:"net"` // Credited to the acquirer's settlement account
	Items      []AdviceItem `json:"items"`
}
