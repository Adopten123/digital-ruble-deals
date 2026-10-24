package service

import (
	"context"
	"strings"
	"testing"
)

type blockMemoryRepository struct {
	memoryRepository
	commands []BlockCommand
}

func (r *blockMemoryRepository) Block(_ context.Context, c BlockCommand) (Result, error) {
	r.commands = append(r.commands, c)
	return Result{Status: 201}, nil
}
func TestBlockerValidationNormalizesReasonWithoutChangingScope(t *testing.T) {
	r := &blockMemoryRepository{}
	s := New(r, make([]byte, 32))
	a := Actor{UserID: "u", OrganizationID: "o"}
	_, e := s.RequestCancellation(context.Background(), a, "deal", "blocker-key-00001", ReasonInput{DealVersion: 2, Reason: "  Обсудить отмену  "})
	if e != nil {
		t.Fatal(e)
	}
	first := r.commands[0]
	if first.Reason != "Обсудить отмену" || first.Actor != a || first.Operation != "REQUEST_CANCELLATION" || first.Invalid != nil {
		t.Fatal("invalid normalized blocker", first)
	}
	_, _ = s.RequestCancellation(context.Background(), a, "deal", "blocker-key-00001", ReasonInput{DealVersion: 2, Reason: "Обсудить отмену"})
	if r.commands[1].Hash != first.Hash {
		t.Fatal("equivalent reasons changed fingerprint")
	}
	for _, in := range []ReasonInput{{DealVersion: 0, Reason: "x"}, {DealVersion: 2, Reason: " "}, {DealVersion: 2, Reason: strings.Repeat("я", 2001)}, {DealVersion: 2, Reason: "control\x00character"}} {
		_, _ = s.Dispute(context.Background(), a, "deal", "blocker-key-00001", in)
		if r.commands[len(r.commands)-1].Invalid == nil {
			t.Fatal("invalid blocking reason accepted", in)
		}
	}
	_, _ = s.AcceptCancellation(context.Background(), a, "deal", "request", "blocker-key-00001", CancelAcceptInput{DealVersion: 2})
	last := r.commands[len(r.commands)-1]
	if last.RequestID != "request" || last.Operation != "ACCEPT_CANCELLATION" || last.Invalid != nil {
		t.Fatal("decision lost its target")
	}
}
