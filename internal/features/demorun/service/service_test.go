package service_test

import (
	"context"
	"errors"
	demo "github.com/Adopten123/digital-ruble-deals/internal/features/demorun/service"
	"testing"
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
