package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	deals "github.com/Adopten123/digital-ruble-deals/internal/features/deals/service"
)

type receiptMemory struct{ accepts, processes int }

func (r *receiptMemory) Accept(context.Context, Envelope) (*deals.Result, error) {
	r.accepts++
	return nil, nil
}
func (r *receiptMemory) Process(context.Context, Envelope) (deals.Result, error) {
	r.processes++
	return deals.Result{Status: 422, Body: []byte(`{"error":{"code":"SIGNATURES_MISSING"}}`)}, nil
}
func (*receiptMemory) Queue(context.Context, Command) (deals.Result, error) {
	return deals.Result{}, nil
}
func (*receiptMemory) Get(context.Context, Actor, string) (Delivery, error)        { return Delivery{}, nil }
func (*receiptMemory) Claim(context.Context) (*Delivery, error)                    { return nil, nil }
func (*receiptMemory) Complete(context.Context, Delivery, int, []byte, bool) error { return nil }
func exampleEvent(t *testing.T) []byte {
	t.Helper()
	raw, err := json.Marshal(Event{SchemaVersion: 1, EventID: "example-event", Source: Source, EventType: "document.accepted_by_both", DealID: "deal", DealVersion: 2, DocumentID: "doc", DocumentVersion: 1, DocumentSHA256: strings.Repeat("a", 64), CustomerPartyID: "customer", SupplierPartyID: "supplier", AmountKopecks: "30000000", Currency: "RUB", OccurredAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func TestTransportAuthenticatesExactBytesBeforeInbox(t *testing.T) {
	repo := &receiptMemory{}
	s := New(repo, make([]byte, 32))
	raw := exampleEvent(t)
	h := s.Sign(raw, time.Now())
	out, err := s.Receive(context.Background(), h, raw, "request")
	if err != nil || out.Status != 422 || repo.accepts != 1 || repo.processes != 1 {
		t.Fatal("valid source did not reach durable domain decision", out, err)
	}
	for _, tc := range []struct {
		body    []byte
		headers Headers
	}{{append(raw, ' '), h}, {raw, s.Sign(raw, time.Now().Add(-301*time.Second))}, {raw, s.Sign(raw, time.Now().Add(301*time.Second))}, {raw, New(repo, []byte("different-key")).Sign(raw, time.Now())}} {
		out, err = s.Receive(context.Background(), tc.headers, tc.body, "request")
		if err != nil || out.Status != 403 {
			t.Fatal("invalid transport accepted", out, err)
		}
	}
	if repo.accepts != 1 || repo.processes != 1 {
		t.Fatal("invalid transport touched inbox")
	}
}
func TestEventRequiresExactFlatSchemaAndSignatureNulls(t *testing.T) {
	raw := exampleEvent(t)
	if _, err := Parse(raw); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{strings.Replace(string(raw), `"signed_by_customer":false`, `"signed_by_customer":null`, 1), strings.Replace(string(raw), `"customer_signature_id":null,`, "", 1), strings.Replace(string(raw), `"customer_signature_id":null`, `"customer_signature_id":"fake"`, 1), strings.Replace(string(raw), `"source":`, `"Source":`, 1), `{"event_id":"duplicate",` + string(raw[1:]), string(raw) + `{}`, strings.Replace(string(raw), `"deal_version":2`, `"deal_version":2.0`, 1)} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Fatal("invalid schema accepted", bad)
		}
	}
}

func TestOperatorInputOptionalReplayIsAbsentOrString(t *testing.T) {
	for _, raw := range []string{`{"deal_id":"deal","template":"valid","replay_event_id":null}`, `{"deal_id":null,"template":"valid"}`, `{"deal_id":"deal","template":"valid","extra":"field"}`, `{"deal_id":"deal","template":"valid","template":"replay"}`} {
		var in Input
		if json.Unmarshal([]byte(raw), &in) == nil {
			t.Fatal("invalid operator schema accepted", raw)
		}
	}
	var in Input
	if err := json.Unmarshal([]byte(`{"deal_id":"deal","template":"valid"}`), &in); err != nil || in.ReplayEventID != nil {
		t.Fatal("absent optional field rejected", err)
	}
}
