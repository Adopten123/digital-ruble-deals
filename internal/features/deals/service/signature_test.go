package service

import (
	"context"
	"strings"
	"testing"
)

type signatureMemoryRepository struct {
	memoryRepository
	commands []SignCommand
}

func (r *signatureMemoryRepository) Sign(_ context.Context, cmd SignCommand) (Result, error) {
	r.commands = append(r.commands, cmd)
	return Result{Status: 201}, nil
}

func TestSignatureValidationBindsExactInputAndVerifier(t *testing.T) {
	repo := &signatureMemoryRepository{}
	svc := New(repo, make([]byte, 32))
	a := Actor{UserID: "user", OrganizationID: "organisation"}
	in := SignInput{DealVersion: 2, DocumentVersion: 1, DocumentSHA256: strings.Repeat("a", 64)}
	if _, err := svc.Sign(context.Background(), a, "doc", "signature-key-001", in); err != nil {
		t.Fatal(err)
	}
	first := repo.commands[0]
	if first.Invalid != nil || first.DocumentID != "doc" || first.Input != in || first.Actor != a || first.VerifyDocument == nil {
		t.Fatal("sign command lost its exact context", first)
	}
	if _, err := svc.Sign(context.Background(), a, "doc", "signature-key-001", in); err != nil {
		t.Fatal(err)
	}
	if repo.commands[1].Hash != first.Hash {
		t.Fatal("same body changed signature fingerprint")
	}
	in.DocumentVersion++
	_, _ = svc.Sign(context.Background(), a, "doc", "signature-key-001", in)
	if repo.commands[2].Hash == first.Hash {
		t.Fatal("document version missing from request fingerprint")
	}
	for _, in := range []SignInput{{}, {DealVersion: 2, DocumentVersion: 0, DocumentSHA256: strings.Repeat("a", 64)}, {DealVersion: 2, DocumentVersion: 1, DocumentSHA256: strings.Repeat("A", 64)}, {DealVersion: 2, DocumentVersion: 1, DocumentSHA256: strings.Repeat("z", 64)}, {DealVersion: 2, DocumentVersion: 1, DocumentSHA256: strings.Repeat("a", 63)}} {
		_, _ = svc.Sign(context.Background(), a, "doc", "signature-invalid", in)
		if cmd := repo.commands[len(repo.commands)-1]; cmd.Invalid == nil || cmd.Invalid.Code != "INVALID_REQUEST" {
			t.Fatal("invalid signature binding was accepted", in)
		}
	}
}

func TestSignatureRejectsInvalidScopeAndKeyBeforeRepository(t *testing.T) {
	repo := &signatureMemoryRepository{}
	svc := New(repo, make([]byte, 32))
	in := SignInput{DealVersion: 2, DocumentVersion: 1, DocumentSHA256: strings.Repeat("a", 64)}
	if _, err := svc.Sign(context.Background(), Actor{}, "doc", "signature-key-001", in); err != ErrNotFound {
		t.Fatal("missing actor did not preserve privacy", err)
	}
	if _, err := svc.Sign(context.Background(), Actor{UserID: "u", OrganizationID: "o"}, "", "signature-key-001", in); err != ErrNotFound {
		t.Fatal("invalid document scope accepted", err)
	}
	out, err := svc.Sign(context.Background(), Actor{UserID: "u", OrganizationID: "o"}, "doc", "short", in)
	if err != nil || out.Status != 400 || len(repo.commands) != 0 {
		t.Fatal("invalid key reached repository", err, out.Status)
	}
}
