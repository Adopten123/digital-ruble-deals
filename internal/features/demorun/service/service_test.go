package service_test

import (
	"context"
	"errors"
	deals "github.com/Adopten123/digital-ruble-deals/internal/features/deals/service"
	demo "github.com/Adopten123/digital-ruble-deals/internal/features/demorun/service"
	"testing"
	"time"
)

func TestInvalidInputsCannotReachSetup(t *testing.T) {
	for _, id := range []string{"", "ab", "UPPER", "../outside", "a b", "show\n"} {
		if demo.ValidRunID(id) {
			t.Fatalf("accepted run id %q", id)
		}
	}
	for _, password := range []string{"short", "TwelveChars\nX", "WithNul\x00Characters"} {
		if demo.ValidPassword(password) {
			t.Fatal("accepted invalid password")
		}
	}
	if _, err := demo.New(nil, nil, make([]byte, 32)).Create(context.Background(), "valid-show", "DemoRunOnly2026!"); !errors.Is(err, demo.ErrInvalid) {
		t.Fatal(err)
	}
}

type oldSeed struct{}

func (oldSeed) Seed(context.Context, demo.SeedCommand) (demo.SeedResult, error) {
	return demo.SeedResult{SeededAt: time.Now().UTC().Add(-15 * 24 * time.Hour), Resumed: true}, nil
}

type refusedDraft struct{ code string }

func (r refusedDraft) Create(context.Context, deals.Actor, string, deals.Input) (deals.Result, error) {
	return deals.Failure{Status: 422, Code: r.code, Message: "safe refusal"}.Result("request"), nil
}
func TestExpiredOrChangedPreparationRequiresReviewInsteadOfBlindRetry(t *testing.T) {
	for _, tc := range []struct {
		code string
		want error
	}{{"DEADLINE_PASSED", demo.ErrExpired}, {"IDEMPOTENCY_CONFLICT", demo.ErrChanged}} {
		s := demo.New(oldSeed{}, refusedDraft{tc.code}, make([]byte, 32))
		if _, err := s.Create(context.Background(), "old-show", "DemoRunOnly2026!"); !errors.Is(err, tc.want) {
			t.Fatal("incorrect recovery advice", err)
		}
	}
}
