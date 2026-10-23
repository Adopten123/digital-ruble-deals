package ledger

import (
	"errors"
	"math"
	"testing"
)

func TestTransferAndReserveIntegerLimits(t *testing.T) {
	customer := Account{ID: "customer", Total: 50000000, Reserved: 30000000}
	recipient := Account{ID: "recipient"}
	if err := CheckTransfer(customer, recipient, 20000000); err != nil {
		t.Fatal("available boundary rejected", err)
	}
	if err := CheckTransfer(customer, recipient, 20000001); !errors.Is(err, ErrInsufficient) {
		t.Fatal("reserved funds spent", err)
	}
	if err := CanReserve(customer, 20000001); !errors.Is(err, ErrInsufficient) {
		t.Fatal("second reserve consumed unavailable funds", err)
	}
	for _, amount := range []int64{0, -1, math.MinInt64} {
		if err := CheckTransfer(customer, recipient, amount); err == nil {
			t.Fatal("invalid amount accepted", amount)
		}
	}
	if err := CheckTransfer(customer, customer, 1); err == nil {
		t.Fatal("same account accepted")
	}
	issuance := Account{ID: "issuance", Purpose: "DEMO_ISSUANCE", Total: math.MinInt64 + 1}
	if err := CheckTransfer(issuance, recipient, 1); err != nil {
		t.Fatal("issuance minimum boundary rejected", err)
	}
	if err := CheckTransfer(issuance, recipient, 2); !errors.Is(err, ErrOverflow) {
		t.Fatal("issuance wrapped", err)
	}
	recipient.Total = math.MaxInt64
	if err := CheckTransfer(customer, recipient, 1); !errors.Is(err, ErrOverflow) {
		t.Fatal("recipient overflow ignored", err)
	}
}
