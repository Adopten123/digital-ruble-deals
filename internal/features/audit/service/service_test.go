package service

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

type fakeRepository struct {
	records []Record
	queries []Query
	err     error
}

func (r *fakeRepository) Read(_ context.Context, q Query) ([]Record, error) {
	r.queries = append(r.queries, q)
	return r.records, r.err
}
func TestCursorScopeFiltersAndHighwater(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	repo := &fakeRepository{records: []Record{{Event: Event{ID: "c", CreatedAt: now}}, {Event: Event{ID: "b", CreatedAt: now.Add(-time.Second)}}, {Event: Event{ID: "a", CreatedAt: now.Add(-2 * time.Second)}}}}
	s := New(repo, []byte("test-only-application-key"))
	a := Actor{UserID: "user", OrganizationID: "org"}
	f := Filters{DealID: "deal", Limit: 2}
	first, err := s.List(context.Background(), a, f, "")
	if err != nil || len(first.Items) != 2 || first.NextCursor == nil {
		t.Fatalf("first: %+v %v", first, err)
	}
	repo.records = repo.records[2:]
	next, err := s.List(context.Background(), a, f, *first.NextCursor)
	if err != nil || len(next.Items) != 1 || next.NextCursor != nil {
		t.Fatalf("next: %+v %v", next, err)
	}
	q := repo.queries[1]
	if q.Before == nil || q.Before.ID != "b" || q.Highwater == nil || q.Highwater.ID != "c" || q.Filters.Limit != 3 {
		t.Fatalf("query %+v", q)
	}
	checks := []struct {
		actor   Actor
		filters Filters
		cursor  string
	}{
		{Actor{UserID: "other", OrganizationID: "org"}, f, *first.NextCursor},
		{Actor{UserID: "user", OrganizationID: "other"}, f, *first.NextCursor},
		{Actor{UserID: "user", Operator: true}, f, *first.NextCursor},
		{a, Filters{DealID: "other", Limit: 2}, *first.NextCursor},
		{a, Filters{DealID: "deal", Limit: 1}, *first.NextCursor},
		{a, Filters{DealID: "deal", Type: "DEAL_CREATED", Limit: 2}, *first.NextCursor},
		{a, f, *first.NextCursor + "x"},
	}
	for _, c := range checks {
		if _, err := s.List(context.Background(), c.actor, c.filters, c.cursor); !errors.Is(err, ErrInvalidQuery) {
			t.Errorf("scope query got %v", err)
		}
	}
	if _, err := New(repo, []byte("other-key")).List(context.Background(), a, f, *first.NextCursor); !errors.Is(err, ErrInvalidQuery) {
		t.Fatal(err)
	}
}
func TestExportBoundedWithoutPartialResult(t *testing.T) {
	r := &fakeRepository{records: make([]Record, MaxExportItems+1)}
	s := New(r, nil)
	out, err := s.Export(context.Background(), Actor{UserID: "u", OrganizationID: "o"}, Filters{DealID: "d"})
	if !errors.Is(err, ErrExportTooLarge) || out.Items != nil || r.queries[0].Filters.Limit != MaxExportItems+1 {
		t.Fatalf("out %+v err %v", out, err)
	}
}
func TestSafeProjectionAndCancellationDecision(t *testing.T) {
	rec := Record{Event: Event{ID: "x", Type: "CANCELLATION_REJECTED", CreatedAt: time.Now()}, Payload: []byte(`{"deal":{"status":"REVIEW_REQUIRED","title":"SECRET TITLE","description":"SECRET DESCRIPTION","customer":{"name":"PRIVATE NAME"},"amount_kopecks":"30000","currency":"RUB"},"cancellation_request":{"id":"request","reason":"old reason","decision":{"decision":"REJECTED","reason":"=1+1, \"Кириллица\"\nСтрока"}},"password":"SECRET PASSWORD","raw_body":"SECRET RAW"}`)}
	e := Project(rec)
	if e.Result != "SUCCEEDED" || e.ErrorCode != nil || e.Details.Reason != "=1+1, \"Кириллица\"\nСтрока" || e.Details.Decision != "REJECTED" || e.Details.CancellationRequestID != "request" {
		t.Fatalf("event %+v", e)
	}
	raw, _ := json.Marshal(e)
	if bytes.Contains(raw, []byte("SECRET")) || bytes.Contains(raw, []byte("PRIVATE")) || bytes.Contains(raw, []byte("old reason")) {
		t.Fatalf("leaked %s", raw)
	}
	rec.Type = "REQUEST_CANCELLATION_REJECTED"
	rec.Payload = []byte(`{"operation":"REQUEST_CANCELLATION","result":{"error":{"code":"VERSION_CONFLICT","message":"private error details"}}}`)
	e = Project(rec)
	if e.Result != "REJECTED" || e.ErrorCode == nil || *e.ErrorCode != "VERSION_CONFLICT" || e.Details.Operation != "REQUEST_CANCELLATION" {
		t.Fatalf("refusal %+v", e)
	}
	rec.Type = "FUTURE_EVENT"
	e = Project(rec)
	if e.Result != "RECORDED" || e.ErrorCode != nil || e.Details != (Details{}) {
		t.Fatalf("future %+v", e)
	}
}
func TestCSVUnicodeQuotesMultilineAndFormulaDefense(t *testing.T) {
	values := []string{"=1+1", " +SUM(1,2)", "\t@cmd", "\r\n-2+3", "\u00a0=1", "Обычная, \"причина\"\nвторая строка"}
	events := make([]Event, len(values))
	for i, value := range values {
		events[i] = Event{ID: "id", Details: Details{Reason: value}}
	}
	raw, err := CSV(events)
	if err != nil || !bytes.HasPrefix(raw, []byte{0xef, 0xbb, 0xbf}) || !bytes.Contains(raw, []byte("\r\n")) {
		t.Fatalf("CSV %q %v", raw, err)
	}
	if !bytes.Contains(raw, []byte("\r\n-2+3")) || !bytes.Contains(raw, []byte("причина\"\"\nвторая строка")) {
		t.Fatalf("rewritten quoted text %q", raw)
	}
	reader := csv.NewReader(strings.NewReader(string(raw[3:])))
	header, err := reader.Read()
	if err != nil || strings.Join(header, ",") != strings.Join(csvColumns, ",") {
		t.Fatal(header, err)
	}
	for i, v := range values {
		row, err := reader.Read()
		if err != nil {
			t.Fatal(err)
		}
		want := v
		if i < 5 {
			want = "'" + v
		}
		want = strings.ReplaceAll(want, "\r\n", "\n")
		if row[16] != want {
			t.Errorf("reason got %q want %q", row[16], want)
		}
	}
	if _, err = reader.Read(); !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
}
func TestFiltersValidationAndUTC(t *testing.T) {
	repo := &fakeRepository{}
	s := New(repo, nil)
	a := Actor{UserID: "u", OrganizationID: "o"}
	invalid := []Filters{{DealID: "d", Limit: 0}, {DealID: "d", Limit: 101}, {DealID: "d", Limit: 1, Type: "type"}, {DealID: "bad id", Limit: 1}, {DealID: "d", Limit: 1, Type: strings.Repeat("A", 65)}}
	for _, f := range invalid {
		if _, err := s.List(context.Background(), a, f, ""); !errors.Is(err, ErrInvalidQuery) {
			t.Errorf("%+v: %v", f, err)
		}
	}
	from := time.Date(2026, 10, 24, 15, 0, 0, 0, time.FixedZone("MSK", 3*60*60))
	to := from.Add(time.Second)
	if _, err := s.List(context.Background(), a, Filters{DealID: "d", Limit: 1, From: &from, To: &to, Type: "UNKNOWN_EVENT"}, ""); err != nil {
		t.Fatal(err)
	}
	if repo.queries[0].Filters.From.Location() != time.UTC {
		t.Fatal("not UTC")
	}
	if _, err := s.List(context.Background(), a, Filters{DealID: "d", Limit: 1, From: &to, To: &from}, ""); !errors.Is(err, ErrInvalidQuery) {
		t.Fatal(err)
	}
}

func TestOperatorResourcesAndEDOResultProjection(t *testing.T) {
	rec := Record{Event: Event{Type: "DEMO_CREDITED"}, Payload: []byte(`{"transaction_id":"batch-id","account":{"id":"account-id","organisation_id":"private-org","total_kopecks":"900000","reserved_kopecks":"100000","name":"PRIVATE"}}`)}
	event := Project(rec)
	if event.Details.AccountID != "account-id" || event.Details.TransactionID != "batch-id" || event.Details.AmountKopecks != "" {
		t.Fatalf("operator projection %+v", event)
	}
	raw, _ := json.Marshal(event)
	if strings.Contains(string(raw), "PRIVATE") || strings.Contains(string(raw), "900000") || strings.Contains(string(raw), "private-org") {
		t.Fatalf("private account data %s", raw)
	}
	for _, result := range []string{"settled", "already_settled"} {
		rec = Record{Event: Event{Type: "EDO_EVENT_PROCESSED"}, Payload: []byte(`{"event_id":"event","result":{"result":"` + result + `","payment_id":"payment"}}`)}
		event = Project(rec)
		if event.Details.Status != strings.ToUpper(result) || event.Details.PaymentID != "payment" {
			t.Fatalf("EDO projection %+v", event)
		}
	}
}

func TestCSVFormulaPrefixWithInvisibleFormatCharacters(t *testing.T) {
	for _, prefix := range []string{"\ufeff", "\u200b", "\u200e", " \t\u2060"} {
		input := prefix + "=SUM(1,2)"
		if got := csvText(input); got != "'"+input {
			t.Fatalf("formula guard got %q", got)
		}
	}
}
