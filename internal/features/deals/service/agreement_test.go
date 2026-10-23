package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

type agreementMemoryRepository struct {
	memoryRepository
	mutations []Mutation
	stored    StoredDocument
}

func (r *agreementMemoryRepository) Mutate(_ context.Context, m Mutation) (Result, error) {
	r.mutations = append(r.mutations, m)
	return Result{Status: 200}, nil
}
func (r *agreementMemoryRepository) GetDocument(context.Context, Actor, string, *int64) (StoredDocument, error) {
	return r.stored, nil
}
func str(value string) *string { return &value }
func TestAgreementValidationAndCanonicalPartialPatch(t *testing.T) {
	repo := &agreementMemoryRepository{}
	s := New(repo, make([]byte, 32))
	a := Actor{UserID: "u", OrganizationID: "customer"}
	_, err := s.Patch(context.Background(), a, "deal", "partial-patch-key01", PatchInput{ExpectedVersion: 2, Title: str("  Тираж  "), DeadlineAt: str("2027-01-01T15:00:00.123456999+03:00")})
	if err != nil {
		t.Fatal(err)
	}
	first := repo.mutations[0]
	if first.Invalid != nil || *first.Patch.Title != "Тираж" || *first.Patch.DeadlineAt != "2027-01-01T12:00:00.123456Z" || first.Patch.Description != nil {
		t.Fatal("partial patch normalization", first)
	}
	_, _ = s.Patch(context.Background(), a, "deal", "partial-patch-key01", PatchInput{ExpectedVersion: 2, Title: str("Тираж"), DeadlineAt: str("2027-01-01T12:00:00.123456Z")})
	if first.Hash != repo.mutations[1].Hash {
		t.Fatal("equivalent patch changed fingerprint")
	}
	for _, in := range []PatchInput{{ExpectedVersion: 0}, {ExpectedVersion: 2, AmountKopecks: str("01")}, {ExpectedVersion: 2, AmountKopecks: str("9223372036854775808")}, {ExpectedVersion: 2, Title: str(" ")}, {ExpectedVersion: 2, SupplierPartyID: str("customer")}, {ExpectedVersion: 2, DeadlineAt: str("tomorrow")}, {ExpectedVersion: 2, Description: str(strings.Repeat("я", 5001))}} {
		_, _ = s.Patch(context.Background(), a, "deal", "invalid-patch-key01", in)
		if repo.mutations[len(repo.mutations)-1].Invalid == nil {
			t.Fatal("invalid patch accepted", in)
		}
	}
	for _, reason := range []string{" ", strings.Repeat("я", 2001)} {
		_, _ = s.Rework(context.Background(), a, "deal", "invalid-rework-key1", ReworkInput{DealVersion: 2, Reason: reason})
		if repo.mutations[len(repo.mutations)-1].Invalid == nil {
			t.Fatal("invalid rework reason accepted")
		}
	}
}

type readStore struct{ data []byte }

func (s *readStore) Put([]byte) (string, error)  { return "opaque-key", nil }
func (s *readStore) Read(string) ([]byte, error) { return append([]byte(nil), s.data...), nil }
func TestDocumentReadChecksRecordedHashAndSize(t *testing.T) {
	data := []byte("fixed immutable document bytes")
	hash := sha256.Sum256(data)
	repo := &agreementMemoryRepository{stored: StoredDocument{Metadata: Document{ID: "doc", DocumentVersion: 1, SHA256: hex.EncodeToString(hash[:]), SizeBytes: int64(len(data))}, Key: "opaque-key"}}
	store := &readStore{data: data}
	s := New(repo, make([]byte, 32), store)
	a := Actor{UserID: "user", OrganizationID: "org"}
	_, b, err := s.DocumentBytes(context.Background(), a, "doc", 1)
	if err != nil || string(b) != string(data) {
		t.Fatal("intact bytes unavailable", err)
	}
	store.data = append([]byte(nil), data...)
	store.data[0] ^= 1
	_, b, err = s.DocumentBytes(context.Background(), a, "doc", 1)
	if err == nil || len(b) != 0 {
		t.Fatal("changed file bytes were served")
	}
	store.data = data
	repo.stored.Metadata.SizeBytes++
	_, b, err = s.DocumentBytes(context.Background(), a, "doc", 1)
	if err == nil || len(b) != 0 {
		t.Fatal("wrong recorded size was ignored")
	}
}
