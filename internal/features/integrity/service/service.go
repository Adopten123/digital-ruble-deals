// Package service defines independent, signed snapshots of the declared A19 coverage.
package service

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"time"

	"github.com/Adopten123/digital-ruble-deals/internal/core/integrityformat"
	"github.com/Adopten123/digital-ruble-deals/internal/core/integritylock"
)

const CheckpointFormat = "poaktu-integrity-checkpoint-v1"
const MaxCheckpointBytes = 256 << 10

var ErrInvalidCheckpoint = errors.New("invalid or untrusted integrity checkpoint")
var ErrCannotCheckpoint = errors.New("current state cannot be checkpointed")
var ErrUnavailable = errors.New("integrity snapshot unavailable")
var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var logPattern = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)

type TableDigest struct {
	Name         string   `json:"name"`
	RelationKind string   `json:"relation_kind"`
	Columns      []string `json:"columns"`
	Types        []string `json:"types"`
	Rows         int64    `json:"rows"`
	Hash         string   `json:"hash"`
}
type Pending struct {
	Notifications int64 `json:"notifications"`
	Inbox         int64 `json:"inbox"`
	Deliveries    int64 `json:"deliveries"`
	OrphanEDO     int64 `json:"orphan_edo"`
}

func (p Pending) Empty() bool {
	return p.Notifications == 0 && p.Inbox == 0 && p.Deliveries == 0 && p.OrphanEDO == 0
}

type Snapshot struct {
	FormatVersion int           `json:"format_version"`
	LogID         string        `json:"log_id"`
	Sequence      int64         `json:"sequence"`
	HeadHash      string        `json:"head_hash"`
	LegacyCount   int64         `json:"legacy_count"`
	HeadCreatedAt time.Time     `json:"head_created_at"`
	CapturedAt    time.Time     `json:"captured_at"`
	AuditCount    int64         `json:"audit_count"`
	ChainCount    int64         `json:"chain_count"`
	ChainValid    bool          `json:"chain_valid"`
	ChainIssues   []string      `json:"chain_issues"`
	Tables        []TableDigest `json:"tables"`
	Exclusions    []string      `json:"exclusions"`
	CoverageHash  string        `json:"coverage_hash"`
	StateHash     string        `json:"state_hash"`
	PDFCount      int64         `json:"pdf_count"`
	PDFHash       string        `json:"pdf_hash"`
	PDFAvailable  bool          `json:"pdf_available"`
	PDFValid      bool          `json:"pdf_valid"`
	Pending       Pending       `json:"pending"`
}
type Repository interface {
	Capture(context.Context) (Snapshot, error)
}
type Service struct{ repo Repository }

func New(repo Repository) *Service { return &Service{repo: repo} }
func (s *Service) Capture(ctx context.Context) (Snapshot, error) {
	if s == nil || s.repo == nil {
		return Snapshot{}, ErrUnavailable
	}
	return s.repo.Capture(ctx)
}

type Checkpoint struct {
	Format    string   `json:"format"`
	KeyID     string   `json:"key_id"`
	Snapshot  Snapshot `json:"snapshot"`
	Signature string   `json:"signature"`
}
type unsignedCheckpoint struct {
	Format   string   `json:"format"`
	KeyID    string   `json:"key_id"`
	Snapshot Snapshot `json:"snapshot"`
}

func signingBytes(c Checkpoint) ([]byte, error) {
	raw, err := json.Marshal(unsignedCheckpoint{c.Format, c.KeyID, c.Snapshot})
	if err != nil {
		return nil, err
	}
	wire, err := integrityformat.CanonicalJSON(raw)
	if err != nil {
		return nil, err
	}
	return append([]byte("poaktu:integrity:checkpoint-signature:v1\x00"), wire...), nil
}
func PublicKeyFingerprint(key ed25519.PublicKey) string {
	sum := sha256.Sum256(key)
	return hex.EncodeToString(sum[:])
}
func CheckpointFingerprint(c Checkpoint) string {
	raw, err := json.Marshal(c)
	if err != nil {
		return ""
	}
	wire, err := integrityformat.CanonicalJSON(raw)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(wire)
	return hex.EncodeToString(sum[:])
}

func validSnapshot(s Snapshot) bool {
	if s.FormatVersion != integrityformat.Version || !logPattern.MatchString(s.LogID) || s.Sequence < 0 || s.LegacyCount < 0 || s.LegacyCount > s.Sequence || s.AuditCount != s.Sequence || s.ChainCount != s.Sequence || !s.ChainValid || len(s.ChainIssues) != 0 || s.CapturedAt.IsZero() || s.HeadCreatedAt.IsZero() || !s.PDFAvailable || !s.PDFValid || s.PDFCount < 0 || !s.Pending.Empty() {
		return false
	}
	for _, hash := range []string{s.HeadHash, s.CoverageHash, s.StateHash, s.PDFHash} {
		if !digestPattern.MatchString(hash) {
			return false
		}
	}
	if len(s.Tables) == 0 {
		return false
	}
	seen := map[string]bool{}
	for _, table := range s.Tables {
		if table.Name == "" || seen[table.Name] || table.Rows < 0 || !digestPattern.MatchString(table.Hash) || len(table.Columns) == 0 || len(table.Columns) != len(table.Types) {
			return false
		}
		seen[table.Name] = true
	}
	return true
}
func CreateCheckpoint(s Snapshot, key ed25519.PrivateKey) (Checkpoint, error) {
	if !validSnapshot(s) {
		return Checkpoint{}, ErrCannotCheckpoint
	}
	if len(key) != ed25519.PrivateKeySize || !bytes.Equal(ed25519.NewKeyFromSeed(key[:ed25519.SeedSize]), key) {
		return Checkpoint{}, ErrInvalidCheckpoint
	}
	c := Checkpoint{Format: CheckpointFormat, KeyID: PublicKeyFingerprint(key.Public().(ed25519.PublicKey)), Snapshot: s}
	raw, err := signingBytes(c)
	if err != nil {
		return Checkpoint{}, ErrInvalidCheckpoint
	}
	c.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, raw))
	return c, nil
}

// ParseCheckpoint rejects unknown fields, duplicate names at every nesting level,
// trailing JSON, unsupported formats and oversized documents before verification.
func ParseCheckpoint(raw []byte) (Checkpoint, error) {
	if len(raw) == 0 || len(raw) > MaxCheckpointBytes {
		return Checkpoint{}, ErrInvalidCheckpoint
	}
	if _, err := integrityformat.CanonicalJSON(raw); err != nil {
		return Checkpoint{}, ErrInvalidCheckpoint
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	var c Checkpoint
	if d.Decode(&c) != nil {
		return Checkpoint{}, ErrInvalidCheckpoint
	}
	var trailing any
	if d.Decode(&trailing) != io.EOF {
		return Checkpoint{}, ErrInvalidCheckpoint
	}
	if c.Format != CheckpointFormat || !digestPattern.MatchString(c.KeyID) || !validSnapshot(c.Snapshot) {
		return Checkpoint{}, ErrInvalidCheckpoint
	}
	sig, err := base64.RawURLEncoding.DecodeString(c.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize || base64.RawURLEncoding.EncodeToString(sig) != c.Signature {
		return Checkpoint{}, ErrInvalidCheckpoint
	}
	return c, nil
}

type Status string

const (
	MATCH               Status = "MATCH"
	CHAIN_BROKEN        Status = "CHAIN_BROKEN"
	MISMATCH            Status = "MISMATCH"
	UNVERIFIABLE        Status = "UNVERIFIABLE"
	QUIESCENCE_REQUIRED Status = "QUIESCENCE_REQUIRED"
)

type Report struct {
	Status                Status     `json:"status"`
	Message               string     `json:"message"`
	LogID                 string     `json:"log_id,omitempty"`
	CheckpointFingerprint string     `json:"checkpoint_fingerprint,omitempty"`
	ObservedAt            time.Time  `json:"observed_at"`
	CheckpointAt          *time.Time `json:"checkpoint_at,omitempty"`
	Differences           []string   `json:"differences"`
}

func Unverifiable(err error) Report {
	if errors.Is(err, integritylock.ErrBusy) {
		return Report{Status: QUIESCENCE_REQUIRED, Message: "Остановите API и фоновые обработчики перед проверкой.", ObservedAt: time.Now().UTC(), Differences: []string{"active_application"}}
	}
	return Report{Status: UNVERIFIABLE, Message: "Проверку нельзя подтвердить: эталон, ключ или покрытые данные недоступны либо некорректны.", ObservedAt: time.Now().UTC(), Differences: []string{"unavailable_or_untrusted_input"}}
}
func Verify(s Snapshot, c Checkpoint, pinned ed25519.PublicKey) Report {
	report := Report{Status: UNVERIFIABLE, Message: "Эталон или закреплённый ключ не подтверждены.", ObservedAt: s.CapturedAt, Differences: []string{}}
	if len(pinned) != ed25519.PublicKeySize || c.Format != CheckpointFormat || !validSnapshot(c.Snapshot) || c.KeyID != PublicKeyFingerprint(pinned) {
		return report
	}
	signature, err := base64.RawURLEncoding.DecodeString(c.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return report
	}
	raw, err := signingBytes(c)
	if err != nil || !ed25519.Verify(pinned, raw, signature) {
		return report
	}
	report.LogID = s.LogID
	report.CheckpointFingerprint = CheckpointFingerprint(c)
	report.CheckpointAt = &c.Snapshot.CapturedAt
	if !s.ChainValid {
		report.Status = CHAIN_BROKEN
		report.Message = "Проверка структуры или хешей цепочки не пройдена."
		report.Differences = append(report.Differences, "audit_chain")
		return report
	}
	if !s.PDFAvailable {
		report.Message = "Не удалось прочитать все закреплённые PDF; совпадение не подтверждено."
		report.Differences = append(report.Differences, "pdf_unavailable")
		return report
	}
	old := c.Snapshot
	if s.FormatVersion != old.FormatVersion {
		report.Differences = append(report.Differences, "format")
	}
	if s.LogID != old.LogID {
		report.Differences = append(report.Differences, "log_id")
	}
	if s.Sequence != old.Sequence || s.HeadHash != old.HeadHash || s.LegacyCount != old.LegacyCount || !s.HeadCreatedAt.Equal(old.HeadCreatedAt) || s.AuditCount != old.AuditCount || s.ChainCount != old.ChainCount {
		report.Differences = append(report.Differences, "audit_checkpoint")
	}
	if s.CoverageHash != old.CoverageHash {
		report.Differences = append(report.Differences, "coverage")
	}
	if s.StateHash != old.StateHash {
		report.Differences = append(report.Differences, "critical_state")
	}
	if s.PDFHash != old.PDFHash || s.PDFCount != old.PDFCount || !s.PDFValid {
		report.Differences = append(report.Differences, "pdf_bytes")
	}
	if !s.Pending.Empty() {
		report.Differences = append(report.Differences, "pending_work")
	}
	if len(report.Differences) > 0 {
		report.Status = MISMATCH
		report.Message = "Данные отличаются от указанной точки. Причиной могут быть и обычные действия после неё; отчёт не устанавливает виновника."
		return report
	}
	report.Status = MATCH
	report.Message = "Покрытые данные совпадают с указанной независимой контрольной точкой."
	return report
}
