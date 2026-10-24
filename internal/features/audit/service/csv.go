package service

import (
	"bytes"
	"encoding/csv"
	"strings"
	"time"
	"unicode"
)

var csvColumns = []string{"id", "deal_id", "deal_version", "type", "actor_user_id", "actor_organization_id", "actor_source", "request_id", "created_at", "result", "error_code", "operation", "status", "document_id", "document_version", "document_sha256", "reason", "decision", "cancellation_request_id", "reserve_id", "payment_id", "event_id", "amount_kopecks", "currency", "account_id", "transaction_id"}

func value(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// Spreadsheet applications may ignore whitespace/control bytes before a formula.
// Prefix the original text rather than trimming it, preserving the recorded reason.
func csvText(value string) string {
	probe := strings.TrimLeftFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) })
	if len(probe) > 0 && strings.ContainsRune("=+-@", rune(probe[0])) {
		return "'" + value
	}
	return value
}
func CSV(items []Event) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString("\xef\xbb\xbf")
	write := func(row []string) error {
		// UseCRLF rewrites line breaks inside quoted fields and drops lone CR.
		// Encode one record with original field bytes, replace only its final LF.
		var record bytes.Buffer
		writer := csv.NewWriter(&record)
		if err := writer.Write(row); err != nil {
			return err
		}
		writer.Flush()
		if err := writer.Error(); err != nil {
			return err
		}
		raw := record.Bytes()
		b.Write(raw[:len(raw)-1])
		b.WriteString("\r\n")
		return nil
	}
	if err := write(csvColumns); err != nil {
		return nil, err
	}
	for _, e := range items {
		d := e.Details
		row := []string{e.ID, value(e.DealID), number(e.DealVersion), e.Type, value(e.ActorUserID), value(e.ActorOrganizationID), value(e.ActorSource), e.RequestID, e.CreatedAt.UTC().Format(time.RFC3339Nano), e.Result, value(e.ErrorCode), d.Operation, d.Status, d.DocumentID, number(d.DocumentVersion), d.DocumentSHA256, d.Reason, d.Decision, d.CancellationRequestID, d.ReserveID, d.PaymentID, d.EventID, d.AmountKopecks, d.Currency, d.AccountID, d.TransactionID}
		for i := range row {
			row[i] = csvText(row[i])
		}
		if err := write(row); err != nil {
			return nil, err
		}
	}
	return b.Bytes(), nil
}
