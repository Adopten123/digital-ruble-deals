package service

import (
	"encoding/json"
	"regexp"
)

// This is a public projection, never a generic payload flattener. Adding a new
// event requires choosing the fields that are safe for its historical parties.
var knownTypes = map[string]bool{
	"DEAL_CREATED": true, "PATCH_DEAL": true, "GENERATE_DOCUMENT": true, "UPLOAD_DOCUMENT": true, "AGREE_DEAL": true, "DEAL_REWORKED": true, "DEAL_RESERVED": true, "DOCUMENT_SIGNED": true, "EDO_EVENT_READY": true, "DISPUTE_OPENED": true, "CANCELLATION_REQUESTED": true, "CANCELLATION_ACCEPTED": true, "CANCELLATION_REJECTED": true, "DEAL_SETTLED": true, "EDO_EVENT_PROCESSED": true, "EDO_EVENT_REJECTED": true, "EDO_DELIVERY_REQUESTED": true, "DEMO_CREDITED": true, "DEMO_DEBITED": true, "DEAL_CREATE_REJECTED": true, "PATCH_DEAL_REJECTED": true, "GENERATE_DOCUMENT_REJECTED": true, "UPLOAD_DOCUMENT_REJECTED": true, "AGREE_DEAL_REJECTED": true, "REWORK_DEAL_REJECTED": true, "RESERVE_DEAL_REJECTED": true, "SIGN_DOCUMENT_REJECTED": true, "OPEN_DISPUTE_REJECTED": true, "REQUEST_CANCELLATION_REJECTED": true, "ACCEPT_CANCELLATION_REJECTED": true, "REJECT_CANCELLATION_REJECTED": true, "DEMO_CREDIT_REJECTED": true, "DEMO_DEBIT_REJECTED": true,
}
var hashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var moneyPattern = regexp.MustCompile(`^[1-9][0-9]{0,18}$`)

func object(m map[string]json.RawMessage, key string) map[string]json.RawMessage {
	var v map[string]json.RawMessage
	_ = json.Unmarshal(m[key], &v)
	return v
}
func text(m map[string]json.RawMessage, key string) string {
	var v string
	_ = json.Unmarshal(m[key], &v)
	return v
}
func positive(m map[string]json.RawMessage, key string) *int64 {
	var v int64
	if json.Unmarshal(m[key], &v) != nil || v <= 0 {
		return nil
	}
	return &v
}
func safeID(value string) string {
	if ValidID(value) {
		return value
	}
	return ""
}
func safeCode(value string) string {
	if typePattern.MatchString(value) {
		return value
	}
	return ""
}
func monetary(d *Details, m map[string]json.RawMessage) {
	if v := text(m, "amount_kopecks"); moneyPattern.MatchString(v) {
		d.AmountKopecks = v
	}
	if text(m, "currency") == "RUB" {
		d.Currency = "RUB"
	}
}
func document(d *Details, m map[string]json.RawMessage) {
	if len(m) == 0 {
		return
	}
	d.DocumentID = safeID(text(m, "document_id"))
	if d.DocumentID == "" {
		d.DocumentID = safeID(text(m, "id"))
	}
	d.DocumentVersion = positive(m, "document_version")
	hash := text(m, "document_sha256")
	if hash == "" {
		hash = text(m, "sha256")
	}
	if hashPattern.MatchString(hash) {
		d.DocumentSHA256 = hash
	}
}
func Project(record Record) Event {
	out := record.Event
	out.CreatedAt = out.CreatedAt.UTC()
	out.Result = "RECORDED"
	out.ErrorCode = nil
	out.Details = Details{}
	if !knownTypes[out.Type] {
		return out
	}
	var payload map[string]json.RawMessage
	if json.Unmarshal(record.Payload, &payload) != nil {
		return out
	}
	result := object(payload, "result")
	code := safeCode(text(payload, "error_code"))
	if code == "" {
		code = safeCode(text(object(payload, "error"), "code"))
	}
	if code == "" {
		code = safeCode(text(object(result, "error"), "code"))
	}
	out.Details.Operation = safeCode(text(payload, "operation"))
	if code != "" {
		out.Result = "REJECTED"
		out.ErrorCode = &code
		return out
	}
	// CANCELLATION_REJECTED is a successful decision, unlike command refusal.
	out.Result = "SUCCEEDED"
	deal := object(payload, "deal")
	if len(deal) == 0 {
		deal = payload
	}
	switch out.Type {
	case "DEAL_CREATED", "PATCH_DEAL", "GENERATE_DOCUMENT", "UPLOAD_DOCUMENT", "AGREE_DEAL", "DEAL_REWORKED", "DEAL_RESERVED", "DOCUMENT_SIGNED", "DISPUTE_OPENED", "CANCELLATION_REQUESTED", "CANCELLATION_ACCEPTED", "CANCELLATION_REJECTED":
		out.Details.Status = safeCode(text(deal, "status"))
		monetary(&out.Details, deal)
		document(&out.Details, object(deal, "document"))
		out.Details.ReserveID = safeID(text(object(deal, "reserve"), "id"))
		out.Details.PaymentID = safeID(text(deal, "payment_id"))
	}
	switch out.Type {
	case "DEAL_REWORKED":
		out.Details.Reason = text(payload, "reason")
	case "DISPUTE_OPENED":
		out.Details.Reason = text(object(payload, "dispute"), "reason")
	case "CANCELLATION_REQUESTED", "CANCELLATION_ACCEPTED", "CANCELLATION_REJECTED":
		request := object(payload, "cancellation_request")
		out.Details.CancellationRequestID = safeID(text(request, "id"))
		out.Details.Reason = text(request, "reason")
		decision := object(request, "decision")
		out.Details.Decision = safeCode(text(decision, "decision"))
		if out.Type == "CANCELLATION_REJECTED" {
			out.Details.Reason = text(decision, "reason")
		}
	case "DOCUMENT_SIGNED":
		document(&out.Details, object(payload, "signature"))
	case "EDO_EVENT_READY":
		document(&out.Details, payload)
		monetary(&out.Details, payload)
		out.Details.EventID = safeID(text(payload, "event_id"))
	case "DEAL_SETTLED":
		monetary(&out.Details, payload)
		out.Details.PaymentID = safeID(text(payload, "payment_id"))
		out.Details.EventID = safeID(text(payload, "event_id"))
	case "EDO_EVENT_PROCESSED":
		switch text(result, "result") {
		case "settled", "SETTLED":
			out.Details.Status = "SETTLED"
		case "already_settled", "ALREADY_SETTLED":
			out.Details.Status = "ALREADY_SETTLED"
		}
		out.Details.PaymentID = safeID(text(result, "payment_id"))
		out.Details.EventID = safeID(text(payload, "event_id"))
	case "DEMO_CREDITED", "DEMO_DEBITED":
		out.Details.AccountID = safeID(text(object(payload, "account"), "id"))
		out.Details.TransactionID = safeID(text(payload, "transaction_id"))
	case "EDO_DELIVERY_REQUESTED":
		out.Details.EventID = safeID(text(payload, "event_id"))
	}
	return out
}
