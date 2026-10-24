package service

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Optional means absent, not JSON null. A custom decoder preserves that
// distinction before operator commands can enter their durable command scope.
func (in *Input) UnmarshalJSON(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	bad := errors.New("invalid EDO command schema")
	if err != nil || token != json.Delim('{') {
		return bad
	}
	fields := map[string]json.RawMessage{}
	for d.More() {
		t, e := d.Token()
		if e != nil {
			return bad
		}
		name, ok := t.(string)
		if !ok {
			return bad
		}
		if _, ok = fields[name]; ok {
			return bad
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return bad
		}
		if name != "deal_id" && name != "template" && name != "replay_event_id" {
			return bad
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return bad
		}
		fields[name] = value
	}
	if _, err = d.Token(); err != nil {
		return bad
	}
	if d.Decode(new(any)) != io.EOF {
		return bad
	}
	if fields["deal_id"] == nil || fields["template"] == nil {
		return bad
	}
	type plain Input
	var value plain
	if json.Unmarshal(raw, &value) != nil {
		return bad
	}
	*in = Input(value)
	return nil
}

// Parse keeps exact raw bytes separate from business fields and rejects duplicate
// keys. A false signature flag has an explicit null ID, not a missing field.
func Parse(raw []byte) (Event, error) {
	bad := errors.New("invalid EDO schema")
	if len(raw) == 0 || len(raw) > MaxBody || !utf8.Valid(raw) {
		return Event{}, bad
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return Event{}, bad
	}
	fields := map[string]json.RawMessage{}
	for d.More() {
		t, e := d.Token()
		if e != nil {
			return Event{}, bad
		}
		key, ok := t.(string)
		if !ok {
			return Event{}, bad
		}
		if _, ok = fields[key]; ok {
			return Event{}, bad
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return Event{}, bad
		}
		fields[key] = value
	}
	if _, err = d.Token(); err != nil {
		return Event{}, bad
	}
	if d.Decode(new(any)) != io.EOF {
		return Event{}, bad
	}
	names := []string{"schema_version", "event_id", "source", "event_type", "deal_id", "deal_version", "document_id", "document_version", "document_sha256", "customer_party_id", "supplier_party_id", "amount_kopecks", "currency", "signed_by_customer", "signed_by_supplier", "customer_signature_id", "supplier_signature_id", "occurred_at"}
	if len(fields) != len(names) {
		return Event{}, bad
	}
	for _, name := range names {
		v, ok := fields[name]
		if !ok || (bytes.Equal(bytes.TrimSpace(v), []byte("null")) && name != "customer_signature_id" && name != "supplier_signature_id") {
			return Event{}, bad
		}
	}
	var event Event
	if json.Unmarshal(raw, &event) != nil {
		return Event{}, bad
	}
	if event.SchemaVersion != 1 || event.EventType != "document.accepted_by_both" || event.DealVersion < 1 || event.DocumentVersion < 1 || event.OccurredAt.IsZero() {
		return Event{}, bad
	}
	for _, v := range []string{event.EventID, event.Source, event.DealID, event.DocumentID, event.CustomerPartyID, event.SupplierPartyID} {
		if !text(v, 128) {
			return Event{}, bad
		}
	}
	if len(event.DocumentSHA256) != 64 || strings.ToLower(event.DocumentSHA256) != event.DocumentSHA256 {
		return Event{}, bad
	}
	if _, err = hex.DecodeString(event.DocumentSHA256); err != nil {
		return Event{}, bad
	}
	if !text(event.Currency, 16) {
		return Event{}, bad
	}
	amount, e := strconv.ParseInt(event.AmountKopecks, 10, 64)
	if e != nil || amount <= 0 || strconv.FormatInt(amount, 10) != event.AmountKopecks {
		return Event{}, bad
	}
	if event.SignedByCustomer != (event.CustomerSignatureID != nil) || event.SignedBySupplier != (event.SupplierSignatureID != nil) {
		return Event{}, bad
	}
	for _, id := range []*string{event.CustomerSignatureID, event.SupplierSignatureID} {
		if id != nil && !text(*id, 128) {
			return Event{}, bad
		}
	}
	return event, nil
}
