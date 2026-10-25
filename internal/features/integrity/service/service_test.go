package service

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func reference(t *testing.T) (Snapshot, Checkpoint, ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, 32))
	hash := strings.Repeat("a", 64)
	s := Snapshot{FormatVersion: 1, LogID: "12345678-1234-1234-1234-123456789abc", HeadHash: hash, HeadCreatedAt: time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC), CapturedAt: time.Date(2026, 10, 25, 1, 0, 0, 0, time.UTC), ChainValid: true, ChainIssues: []string{}, Tables: []TableDigest{{Name: "accounts", RelationKind: "r", Columns: []string{"id"}, Types: []string{"text"}, Hash: hash}}, CoverageHash: hash, StateHash: hash, PDFHash: hash, PDFAvailable: true, PDFValid: true}
	c, err := CreateCheckpoint(s, key)
	if err != nil {
		t.Fatal(err)
	}
	return s, c, key.Public().(ed25519.PublicKey), key
}
func TestSignedCheckpointRequiresIndependentKeyAndExactContent(t *testing.T) {
	s, c, pub, _ := reference(t)
	raw, _ := json.Marshal(c)
	parsed, err := ParseCheckpoint(raw)
	if err != nil {
		t.Fatal(err)
	}
	if r := Verify(s, parsed, pub); r.Status != MATCH {
		t.Fatal(r)
	}
	other := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{9}, 32)).Public().(ed25519.PublicKey)
	for _, key := range []ed25519.PublicKey{nil, make([]byte, 31), other} {
		if r := Verify(s, c, key); r.Status != UNVERIFIABLE {
			t.Fatal(r)
		}
	}
	altered := c
	altered.Snapshot.StateHash = strings.Repeat("b", 64)
	if r := Verify(s, altered, pub); r.Status != UNVERIFIABLE {
		t.Fatal(r)
	}
	altered = c
	altered.Signature = strings.Repeat("A", 86)
	if r := Verify(s, altered, pub); r.Status != UNVERIFIABLE {
		t.Fatal(r)
	}
	formatted, _ := json.MarshalIndent(c, "", "  ")
	parsed, err = ParseCheckpoint(formatted)
	if err != nil || CheckpointFingerprint(parsed) != CheckpointFingerprint(c) {
		t.Fatal("semantic fingerprint changed", err)
	}
}
func TestCheckpointParserRejectsAmbiguousOrUnsupportedInputs(t *testing.T) {
	_, c, _, _ := reference(t)
	raw, _ := json.Marshal(c)
	cases := [][]byte{nil, []byte("null"), []byte("{}"), append(append([]byte{}, raw...), []byte(" {}")...), []byte(strings.Repeat(" ", MaxCheckpointBytes+1)), bytes.Replace(raw, []byte(`"format":`), []byte(`"extra":1,"format":`), 1), bytes.Replace(raw, []byte(`"format_version":1`), []byte(`"format_version":1,"format_version":1`), 1), bytes.Replace(raw, []byte(CheckpointFormat), []byte("future-v2"), 1), bytes.Replace(raw, []byte(`"chain_valid":true`), []byte(`"chain_valid":false`), 1)}
	for i, b := range cases {
		if _, err := ParseCheckpoint(b); err == nil {
			t.Errorf("accepted invalid input %d", i)
		}
	}
}
func TestUnsafeStatesCannotBecomeReferencesAndNeverMatch(t *testing.T) {
	s, c, pub, key := reference(t)
	cases := []struct {
		name   string
		change func(*Snapshot)
		status Status
	}{
		{"broken chain", func(s *Snapshot) { s.ChainValid = false }, CHAIN_BROKEN},
		{"missing PDFs", func(s *Snapshot) { s.PDFAvailable = false }, UNVERIFIABLE},
		{"changed PDFs", func(s *Snapshot) { s.PDFValid = false }, MISMATCH},
		{"pending notification", func(s *Snapshot) { s.Pending.Notifications = 1 }, MISMATCH},
		{"pending inbox", func(s *Snapshot) { s.Pending.Inbox = 1 }, MISMATCH},
		{"pending EDO", func(s *Snapshot) { s.Pending.Deliveries = 1 }, MISMATCH},
		{"orphan EDO", func(s *Snapshot) { s.Pending.OrphanEDO = 1 }, MISMATCH},
		{"new audit", func(s *Snapshot) { s.Sequence++; s.AuditCount++; s.ChainCount++ }, MISMATCH},
		{"state", func(s *Snapshot) { s.StateHash = strings.Repeat("b", 64) }, MISMATCH},
		{"coverage", func(s *Snapshot) { s.CoverageHash = strings.Repeat("b", 64) }, MISMATCH},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			now := s
			tc.change(&now)
			if r := Verify(now, c, pub); r.Status != tc.status {
				t.Fatal(r)
			}
			if tc.name != "new audit" && tc.name != "state" && tc.name != "coverage" {
				if _, err := CreateCheckpoint(now, key); err == nil {
					t.Fatal("signed unsafe state")
				}
			}
		})
	}
	if r := Verify(Snapshot{}, c, pub); r.Status == MATCH {
		t.Fatal(r)
	}
	bad := append(ed25519.PrivateKey{}, key...)
	bad[63] ^= 1
	if _, err := CreateCheckpoint(s, bad); err == nil {
		t.Fatal("inconsistent private key accepted")
	}
	if _, err := CreateCheckpoint(Snapshot{}, key); err == nil {
		t.Fatal("empty state accepted")
	}
}
