// Package documentfile handles the prototype's bounded, static PDF documents.
// Validation is a deliberately restricted format policy, not a malware-free
// certificate. The HTTP layer must still authorize every read, avoid public
// storage, and isolate PDF previews from the application's origin.
package documentfile

import "errors"

const MaxSize = 5 << 20

var (
	ErrInvalid  = errors.New("unsupported or invalid PDF")
	ErrTooLarge = errors.New("PDF exceeds 5 MiB")
	ErrNotFound = errors.New("PDF not found")
)

// Act contains the exact terms of one immutable deal version. Monetary input is
// a decimal integer string in kopecks; generation never uses floating point.
type Act struct {
	DealID        string
	DealVersion   int64
	Title         string
	Description   string
	CustomerName  string
	SupplierName  string
	AmountKopecks string
	Currency      string
	DeadlineAt    string
}
