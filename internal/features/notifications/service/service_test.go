package service

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	audit "github.com/Adopten123/digital-ruble-deals/internal/features/audit/service"
)

type fakeRepo struct {
	result   Result
	queries  []Query
	reads    int
	delivery func(context.Context, int) (int, error)
}

func (r *fakeRepo) List(_ context.Context, q Query) (Result, error) {
	r.queries = append(r.queries, q)
	return r.result, nil
}
func (r *fakeRepo) Read(_ context.Context, _ Actor, _ string) error { r.reads++; return nil }
func (r *fakeRepo) Deliver(ctx context.Context, n int) (int, error) {
	if r.delivery == nil {
		return 0, nil
	}
	return r.delivery(ctx, n)
}
func TestCursorBindsUserOrganizationModeFilterLimitAndHighwater(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	repo := &fakeRepo{result: Result{Items: []Record{{ID: "c", CreatedAt: now}, {ID: "b", CreatedAt: now}, {ID: "a", CreatedAt: now}}, UnreadCount: 12}}
	s := New(repo, []byte("test-notification-key"))
	a := Actor{UserID: "user", OrganizationID: "org"}
	f := Filters{Limit: 2}
	first, err := s.List(context.Background(), a, f, "")
	if err != nil || len(first.Items) != 2 || first.NextCursor == nil || first.UnreadCount != 12 {
		t.Fatalf("first %+v %v", first, err)
	}
	repo.result.Items = repo.result.Items[2:]
	if _, err = s.List(context.Background(), a, f, *first.NextCursor); err != nil {
		t.Fatal(err)
	}
	q := repo.queries[1]
	if q.Before == nil || q.Before.ID != "b" || q.Highwater == nil || q.Highwater.ID != "c" || q.Filters.Limit != 3 {
		t.Fatalf("query %+v", q)
	}
	yes, no := true, false
	for _, c := range []struct {
		a     Actor
		f     Filters
		token string
	}{
		{Actor{UserID: "other", OrganizationID: "org"}, f, *first.NextCursor},
		{Actor{UserID: "user", OrganizationID: "other"}, f, *first.NextCursor},
		{Actor{UserID: "user", Operator: true}, f, *first.NextCursor},
		{a, Filters{Limit: 1}, *first.NextCursor},
		{a, Filters{Limit: 2, Unread: &yes}, *first.NextCursor},
		{a, Filters{Limit: 2, Unread: &no}, *first.NextCursor},
		{a, f, *first.NextCursor + "x"},
	} {
		if _, err = s.List(context.Background(), c.a, c.f, c.token); !errors.Is(err, ErrInvalidQuery) {
			t.Errorf("scope/filter accepted %v", err)
		}
	}
	if _, err = New(repo, []byte("different")).List(context.Background(), a, f, *first.NextCursor); !errors.Is(err, ErrInvalidQuery) {
		t.Fatal(err)
	}
}
func TestProjectionOriginalAndDeliveryTimesAndReadPermissions(t *testing.T) {
	original := time.Date(2020, 1, 1, 1, 0, 0, 0, time.FixedZone("offset", 3600))
	delivery := original.Add(10 * 24 * time.Hour)
	repo := &fakeRepo{result: Result{Items: []Record{{ID: "notification", CreatedAt: delivery, ReadAt: &delivery, Event: audit.Record{Event: audit.Event{ID: "event", Type: "DEAL_CREATED", CreatedAt: original}, Payload: []byte(`{"title":"PRIVATE TITLE","password":"SECRET","amount_kopecks":"100","currency":"RUB"}`)}}}}}
	svc := New(repo, nil)
	a := Actor{UserID: "u", OrganizationID: "o"}
	page, err := svc.List(context.Background(), a, Filters{Limit: 20}, "")
	if err != nil {
		t.Fatal(err)
	}
	n := page.Items[0]
	if !n.CreatedAt.Equal(delivery) || !n.Event.CreatedAt.Equal(original) || n.CreatedAt.Location() != time.UTC || n.ReadAt.Location() != time.UTC || n.Event.Details.AmountKopecks != "100" {
		t.Fatalf("projection %+v", n)
	}
	for _, actor := range []Actor{{UserID: "operator", Operator: true}, {UserID: "u"}} {
		if err = svc.Read(context.Background(), actor, "notification"); !errors.Is(err, ErrNotFound) {
			t.Fatal(err)
		}
	}
	if err = svc.Read(context.Background(), a, "invalid id"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if repo.reads != 0 {
		t.Fatal("invalid read reached repository")
	}
	if err = svc.Read(context.Background(), a, "notification"); err != nil || repo.reads != 1 {
		t.Fatal(err, repo.reads)
	}
}
func TestWorkerImmediateDrainAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	repo := &fakeRepo{delivery: func(ctx context.Context, limit int) (int, error) {
		calls++
		if limit != 50 {
			t.Errorf("batch limit %d", limit)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Error("unbounded worker batch")
		}
		if calls == 2 {
			cancel()
			return 0, ctx.Err()
		}
		return 50, nil
	}}
	if err := NewWorker(New(repo, nil)).Run(ctx); err != nil || calls != 2 {
		t.Fatalf("worker %d %v", calls, err)
	}
	if err := NewWorker(nil).Run(context.Background()); err == nil {
		t.Fatal("missing service accepted")
	}
}

func TestWorkerLogsSafeFailureAndRecovery(t *testing.T) {
	var output bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	calls := 0
	repo := &fakeRepo{delivery: func(context.Context, int) (int, error) {
		calls++
		if calls == 1 {
			return 0, errors.New("PRIVATE DSN postgres://secret and SQL payload")
		}
		return 0, nil
	}}
	logger := slog.New(slog.NewTextHandler(&output, nil))
	if err := NewWorker(New(repo, nil), logger).Run(ctx); err != nil {
		t.Fatal(err)
	}
	log := output.String()
	if calls < 2 || strings.Count(log, "delivery deferred") != 1 || !strings.Contains(log, "delivery recovered") || strings.Contains(log, "PRIVATE") || strings.Contains(log, "postgres://") {
		t.Fatalf("unsafe or missing worker diagnostic %s", log)
	}
}
