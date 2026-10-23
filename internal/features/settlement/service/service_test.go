package service

import (
	"context"
	"errors"
	deals "github.com/Adopten123/digital-ruble-deals/internal/features/deals/service"
	"strings"
	"testing"
	"time"
)

type memory struct {
	commands []Command
	items    []Account
	query    Query
}

func (m *memory) Move(_ context.Context, c Command) (deals.Result, error) {
	m.commands = append(m.commands, c)
	return deals.Result{Status: 201}, nil
}
func (m *memory) List(_ context.Context, q Query) ([]Account, error) {
	m.query = q
	return m.items, nil
}
func TestMonetaryInputCanonicalizationAndIntegerBoundaries(t *testing.T) {
	m := &memory{}
	s := New(m, make([]byte, 32))
	a := Actor{UserID: "operator"}
	for _, amount := range []string{"1", "9007199254740993", "9223372036854775807"} {
		_, e := s.Move(context.Background(), a, "account", "monetary-valid-key1", "DEMO_CREDIT", Input{AmountKopecks: amount, Reason: "  Учебный\nсценарий  "})
		if e != nil {
			t.Fatal(e)
		}
		c := m.commands[len(m.commands)-1]
		if c.Invalid != nil || c.Amount <= 0 || c.Input.Reason != "Учебный\nсценарий" {
			t.Fatal("valid integer or text lost", c)
		}
	}
	for _, amount := range []string{"0", "-1", "01", "+1", "1.0", "1e2", "9223372036854775808"} {
		_, _ = s.Move(context.Background(), a, "account", "monetary-invalid-01", "DEMO_DEBIT", Input{AmountKopecks: amount, Reason: "Причина"})
		if c := m.commands[len(m.commands)-1]; c.Invalid == nil || c.Invalid.Code != "INVALID_AMOUNT" {
			t.Fatal("bad amount accepted", amount)
		}
	}
	for _, reason := range []string{" ", strings.Repeat("я", 2001), "reason\x00"} {
		_, _ = s.Move(context.Background(), a, "account", "monetary-reason-key", "DEMO_CREDIT", Input{AmountKopecks: "1", Reason: reason})
		if m.commands[len(m.commands)-1].Invalid == nil {
			t.Fatal("bad reason accepted")
		}
	}
	before := len(m.commands)
	out, e := s.Move(context.Background(), a, "account", "bad", "DEMO_CREDIT", Input{})
	if e != nil || out.Status != 400 || len(m.commands) != before {
		t.Fatal("bad key persisted")
	}
	if _, e = s.Move(context.Background(), Actor{}, "account", "monetary-valid-key1", "DEMO_CREDIT", Input{}); !errors.Is(e, ErrForbidden) {
		t.Fatal("missing actor accepted")
	}
}
func TestAccountCursorBindsOperatorQueryAndPageSize(t *testing.T) {
	now := time.Now().UTC()
	m := &memory{items: []Account{{ID: "c", CreatedAt: now}, {ID: "b", CreatedAt: now.Add(-time.Second)}, {ID: "a", CreatedAt: now.Add(-2 * time.Second)}}}
	s := New(m, make([]byte, 32))
	a := Actor{UserID: "operator"}
	page, e := s.List(context.Background(), a, " запрос ", "", 2)
	if e != nil || page.NextCursor == nil || len(page.Items) != 2 {
		t.Fatal("first page", e, page)
	}
	m.items = nil
	if _, e = s.List(context.Background(), a, "запрос", *page.NextCursor, 2); e != nil || m.query.BeforeID != "b" {
		t.Fatal("valid cursor rejected", e)
	}
	for _, v := range []struct {
		a      Actor
		q      string
		limit  int
		cursor string
	}{{Actor{UserID: "other"}, "запрос", 2, *page.NextCursor}, {a, "other", 2, *page.NextCursor}, {a, "запрос", 3, *page.NextCursor}, {a, "запрос", 2, *page.NextCursor + "x"}} {
		if _, e = s.List(context.Background(), v.a, v.q, v.cursor, v.limit); !errors.Is(e, ErrInvalidQuery) {
			t.Fatal("cursor context ignored", v, e)
		}
	}
}
