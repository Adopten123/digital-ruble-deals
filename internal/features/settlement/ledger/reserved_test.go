package ledger

import (
	"errors"
	"math"
	"testing"
)

func TestReservedPaymentUsesItsReservationAndProtectsOthers(t *testing.T) {
	from := Account{ID: "customer", OrganizationID: "org-c", Total: 50000000, Reserved: 40000000}
	to := Account{ID: "supplier", OrganizationID: "org-s"}
	if err := CheckReservedTransfer(from, to, 30000000); err != nil {
		t.Fatal("own reserve was incorrectly charged against available", err)
	}
	if err := CheckReservedTransfer(from, to, 45000000); !errors.Is(err, ErrInsufficient) {
		t.Fatal("reservation was exceeded", err)
	}
	to.Total = math.MaxInt64 - 1
	if err := CheckReservedTransfer(from, to, 30000000); !errors.Is(err, ErrOverflow) {
		t.Fatal("recipient overflow accepted", err)
	}
}
