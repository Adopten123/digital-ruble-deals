package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type memoryRepository struct {
	commands []Command
	items    []Deal
	query    Query
}

func (r *memoryRepository) Create(_ context.Context, c Command) (Result, error) {
	r.commands = append(r.commands, c)
	return Result{Status: 201}, nil
}
func (r *memoryRepository) List(_ context.Context, q Query) ([]Deal, error) {
	r.query = q
	return r.items, nil
}
func (r *memoryRepository) Get(_ context.Context, _ Actor, _ string) (Deal, error) {
	return Deal{}, nil
}
func validInput() Input {
	return Input{SupplierPartyID: "supplier", Title: "Упаковка", Description: "Описание печатных работ", AmountKopecks: "30000000", Currency: "RUB", DeadlineAt: "2027-01-01T12:00:00.123456789Z"}
}
func TestCanonicalCommandAndBoundaries(t *testing.T) {
	r := &memoryRepository{}
	s := New(r, make([]byte, 32))
	actor := Actor{UserID: "user", OrganizationID: "customer", RequestID: "request"}
	in := validInput()
	if _, err := s.Create(context.Background(), actor, "canonical-key-001", in); err != nil {
		t.Fatal(err)
	}
	first := r.commands[0]
	in.SupplierPartyID = " supplier "
	in.Title = " \t" + in.Title + "\n "
	in.Description = " " + in.Description + " "
	in.DeadlineAt = "2027-01-01T15:00:00.123456999+03:00"
	if _, err := s.Create(context.Background(), actor, "canonical-key-001", in); err != nil {
		t.Fatal(err)
	}
	second := r.commands[1]
	if first.Invalid != nil || second.Invalid != nil || first.Hash != second.Hash || first.Input != second.Input || first.Amount != 30000000 || first.Input.DeadlineAt != "2027-01-01T12:00:00.123456Z" {
		t.Fatalf("normalization mismatch: %+v %+v", first, second)
	}
	for _, value := range []string{"1", "9223372036854775807"} {
		in := validInput()
		in.AmountKopecks = value
		_, _ = s.Create(context.Background(), actor, "boundary-key-001", in)
		c := r.commands[len(r.commands)-1]
		if c.Invalid != nil || c.Amount <= 0 {
			t.Fatal("valid int64 boundary rejected", value)
		}
	}
	for _, value := range []string{"0", "01", "-1", "+1", "1.0", "1e3", "9223372036854775808"} {
		in := validInput()
		in.AmountKopecks = value
		_, _ = s.Create(context.Background(), actor, "invalid-key-0001", in)
		c := r.commands[len(r.commands)-1]
		if c.Invalid == nil || c.Invalid.Code != "INVALID_AMOUNT" {
			t.Fatal("invalid amount accepted", value)
		}
	}
	before := len(r.commands)
	result, _ := s.Create(context.Background(), actor, "short", validInput())
	if result.Status != 400 || len(r.commands) != before {
		t.Fatal("invalid key reached persistence")
	}
	if _, err := s.Create(context.Background(), Actor{}, "valid-key-000001", validInput()); !errors.Is(err, ErrForbidden) {
		t.Fatal("missing org accepted")
	}
	if _, err := s.Get(context.Background(), actor, "bad\x00id"); !errors.Is(err, ErrNotFound) {
		t.Fatal("unsafe resource ID reached persistence")
	}
}
func TestCursorBindsScopeAndAllFilters(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	r := &memoryRepository{items: []Deal{{ID: "c", CreatedAt: now}, {ID: "b", CreatedAt: now.Add(-time.Second)}, {ID: "a", CreatedAt: now.Add(-2 * time.Second)}}}
	s := New(r, make([]byte, 32))
	actor := Actor{UserID: "user", OrganizationID: "customer"}
	filters := Filters{Search: " тираж ", Role: "customer", Status: "DRAFT", CreatedFrom: &now, CreatedTo: &now, Limit: 2}
	page, err := s.List(context.Background(), actor, filters, "")
	if err != nil || len(page.Items) != 2 || page.NextCursor == nil || r.query.Filters.Limit != 3 {
		t.Fatalf("page: %+v %v", page, err)
	}
	r.items = nil
	if _, err := s.List(context.Background(), actor, filters, *page.NextCursor); err != nil || r.query.BeforeID != "b" {
		t.Fatal("valid cursor rejected", err)
	}
	other := actor
	other.OrganizationID = "supplier"
	if _, err := s.List(context.Background(), other, filters, *page.NextCursor); !errors.Is(err, ErrInvalidQuery) {
		t.Fatal("cross-org cursor accepted")
	}
	for _, change := range []func(*Filters){func(f *Filters) { f.Search = "other" }, func(f *Filters) { f.Role = "supplier" }, func(f *Filters) { f.Status = "AGREED" }, func(f *Filters) { f.Limit = 1 }, func(f *Filters) { t := now.Add(-time.Hour); f.CreatedFrom = &t }, func(f *Filters) { t := now.Add(time.Hour); f.CreatedTo = &t }} {
		modified := filters
		change(&modified)
		if _, err := s.List(context.Background(), actor, modified, *page.NextCursor); !errors.Is(err, ErrInvalidQuery) {
			t.Fatal("filter-changing cursor accepted")
		}
	}
	for _, value := range []string{*page.NextCursor + "x", strings.Repeat("x", 4097)} {
		if _, err := s.List(context.Background(), actor, filters, value); !errors.Is(err, ErrInvalidQuery) {
			t.Fatal("tampered/oversized cursor accepted")
		}
	}
}
