package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/Adopten123/digital-ruble-deals/internal/core/integrityformat"
	"github.com/Adopten123/digital-ruble-deals/internal/core/integritylock"
	"github.com/Adopten123/digital-ruble-deals/internal/features/integrity/service"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PDFReader interface{ Read(string) ([]byte, error) }
type Repository struct {
	pool  *pgxpool.Pool
	files PDFReader
}

func New(pool *pgxpool.Pool, files PDFReader) *Repository {
	return &Repository{pool: pool, files: files}
}
func frame(value []byte) []byte {
	out := make([]byte, 8+len(value))
	binary.BigEndian.PutUint64(out, uint64(len(value)))
	copy(out[8:], value)
	return out
}
func encodeHash(sum [32]byte) string { return hex.EncodeToString(sum[:]) }
func hashValue(domain string, value any) ([32]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return [32]byte{}, err
	}
	wire, err := integrityformat.CanonicalJSON(raw)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(append([]byte(domain), wire...)), nil
}
func addIssue(s *service.Snapshot, code string) {
	s.ChainValid = false
	for _, old := range s.ChainIssues {
		if old == code {
			return
		}
	}
	if len(s.ChainIssues) < 16 {
		s.ChainIssues = append(s.ChainIssues, code)
	}
}
func digestBytes(raw []byte) ([32]byte, bool) {
	var out [32]byte
	if len(raw) != len(out) {
		return out, false
	}
	copy(out[:], raw)
	return out, true
}

func (r *Repository) Capture(ctx context.Context) (out service.Snapshot, err error) {
	if r == nil || r.pool == nil {
		return out, service.ErrUnavailable
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return out, service.ErrUnavailable
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err = integritylock.TryExclusive(ctx, tx); err != nil {
		if errors.Is(err, integritylock.ErrBusy) {
			return out, err
		}
		return out, service.ErrUnavailable
	}
	var schema string
	if err = tx.QueryRow(ctx, `SELECT current_schema(),clock_timestamp()`).Scan(&schema, &out.CapturedAt); err != nil {
		return out, service.ErrUnavailable
	}
	// Never resolve hashing/canonical helpers in the writable application schema.
	// All queries below read raw values; Go owns the verifier's hashing logic.
	if _, err = tx.Exec(ctx, `SET LOCAL search_path TO pg_catalog,`+pgx.Identifier{schema}.Sanitize()+`,pg_temp`); err != nil {
		return out, service.ErrUnavailable
	}
	out.CapturedAt = out.CapturedAt.UTC()
	out.ChainValid = true
	out.ChainIssues = []string{}
	out.Tables = []service.TableDigest{}
	out.Exclusions = append([]string(nil), exclusions...)
	if err = r.chain(ctx, tx, schema, &out); err != nil {
		return service.Snapshot{}, service.ErrUnavailable
	}
	for _, spec := range manifest {
		table, err := captureTable(ctx, tx, schema, spec)
		if err != nil {
			return service.Snapshot{}, service.ErrUnavailable
		}
		out.Tables = append(out.Tables, table)
	}
	sort.Slice(out.Tables, func(i, j int) bool { return out.Tables[i].Name < out.Tables[j].Name })
	descriptors := make([]service.TableDigest, len(out.Tables))
	copy(descriptors, out.Tables)
	for i := range descriptors {
		descriptors[i].Rows = 0
		descriptors[i].Hash = ""
	}
	coverage, err := hashValue("poaktu:integrity:coverage:v1\x00", struct {
		Format     int                   `json:"format"`
		Tables     []service.TableDigest `json:"tables"`
		Exclusions []string              `json:"exclusions"`
	}{integrityformat.Version, descriptors, out.Exclusions})
	if err != nil {
		return service.Snapshot{}, service.ErrUnavailable
	}
	out.CoverageHash = encodeHash(coverage)
	state, err := hashValue("poaktu:integrity:state:v1\x00", struct {
		Coverage string                `json:"coverage"`
		Tables   []service.TableDigest `json:"tables"`
	}{out.CoverageHash, out.Tables})
	if err != nil {
		return service.Snapshot{}, service.ErrUnavailable
	}
	out.StateHash = encodeHash(state)
	if err = r.pdfs(ctx, tx, schema, &out); err != nil {
		return service.Snapshot{}, service.ErrUnavailable
	}
	prefix := pgx.Identifier{schema}.Sanitize() + "."
	query := `SELECT (SELECT count(*) FROM ` + prefix + `outbox WHERE delivered_at IS NULL),(SELECT count(*) FROM ` + prefix + `event_inbox WHERE status='PENDING'),(SELECT count(*) FROM ` + prefix + `edo_deliveries WHERE completed_at IS NULL),(SELECT count(*) FROM ` + prefix + `edo_outbox e WHERE NOT EXISTS(SELECT 1 FROM ` + prefix + `edo_deliveries d WHERE d.automatic_basis=e.event_id))`
	if err = tx.QueryRow(ctx, query).Scan(&out.Pending.Notifications, &out.Pending.Inbox, &out.Pending.Deliveries, &out.Pending.OrphanEDO); err != nil {
		return service.Snapshot{}, service.ErrUnavailable
	}
	if err = tx.Commit(ctx); err != nil {
		return service.Snapshot{}, service.ErrUnavailable
	}
	return out, nil
}

func (r *Repository) chain(ctx context.Context, tx pgx.Tx, schema string, out *service.Snapshot) error {
	prefix := pgx.Identifier{schema}.Sanitize() + "."
	rows, err := tx.Query(ctx, `SELECT log_id,format_version,last_seq,head_hash,legacy_count,created_at FROM `+prefix+`audit_chain_head ORDER BY singleton`)
	if err != nil {
		return err
	}
	heads := 0
	var rawHead []byte
	for rows.Next() {
		heads++
		if heads == 1 {
			if err = rows.Scan(&out.LogID, &out.FormatVersion, &out.Sequence, &rawHead, &out.LegacyCount, &out.HeadCreatedAt); err != nil {
				rows.Close()
				return err
			}
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if heads != 1 {
		return fmt.Errorf("invalid chain metadata count")
	}
	out.HeadCreatedAt = out.HeadCreatedAt.UTC()
	out.HeadHash = hex.EncodeToString(rawHead)
	if out.FormatVersion != integrityformat.Version || out.Sequence < 0 || out.LegacyCount < 0 || out.LegacyCount > out.Sequence || len(rawHead) != 32 {
		addIssue(out, "CHAIN_HEAD_INVALID")
	}
	events := map[string][32]byte{}
	rows, err = tx.Query(ctx, `SELECT id,deal_id,deal_version,type,actor_user_id,actor_organization_id,actor_source,request_id,payload,created_at FROM `+prefix+`audit_events`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var event integrityformat.Event
		if err = rows.Scan(&event.ID, &event.DealID, &event.DealVersion, &event.Type, &event.ActorUserID, &event.ActorOrganizationID, &event.ActorSource, &event.RequestID, &event.Payload, &event.CreatedAt); err != nil {
			rows.Close()
			return err
		}
		out.AuditCount++
		hash, e := integrityformat.EventHash(event)
		if e != nil {
			addIssue(out, "CHAIN_EVENT_INVALID")
			continue
		}
		events[event.ID] = hash
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	rows, err = tx.Query(ctx, `SELECT sequence,log_id,audit_id,event_hash,previous_hash,entry_hash FROM `+prefix+`audit_chain_entries ORDER BY sequence`)
	if err != nil {
		return err
	}
	previous := integrityformat.Genesis(out.LogID)
	seen := map[string]bool{}
	for rows.Next() {
		var seq int64
		var logID, eventID string
		var rawEvent, rawPrev, rawEntry []byte
		if err = rows.Scan(&seq, &logID, &eventID, &rawEvent, &rawPrev, &rawEntry); err != nil {
			rows.Close()
			return err
		}
		out.ChainCount++
		if seq != out.ChainCount || logID != out.LogID {
			addIssue(out, "CHAIN_SEQUENCE_OR_LOG_ID")
		}
		eventHash, eventOK := digestBytes(rawEvent)
		storedPrev, prevOK := digestBytes(rawPrev)
		storedEntry, entryOK := digestBytes(rawEntry)
		actual, exists := events[eventID]
		if !exists || seen[eventID] {
			addIssue(out, "CHAIN_EVENT_BIJECTION")
		}
		seen[eventID] = true
		if !eventOK || actual != eventHash {
			addIssue(out, "CHAIN_EVENT_HASH")
		}
		if !prevOK || storedPrev != previous {
			addIssue(out, "CHAIN_PREVIOUS_HASH")
		}
		actualEntry := integrityformat.LinkHash(out.LogID, seq, previous, actual)
		if !entryOK || storedEntry != actualEntry {
			addIssue(out, "CHAIN_ENTRY_HASH")
		}
		previous = actualEntry
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if out.AuditCount != out.ChainCount || len(seen) != len(events) {
		addIssue(out, "CHAIN_EVENT_BIJECTION")
	}
	if out.ChainCount != out.Sequence || !bytes.Equal(previous[:], rawHead) {
		addIssue(out, "CHAIN_HEAD_MISMATCH")
	}
	return nil
}

func captureTable(ctx context.Context, tx pgx.Tx, schema string, spec tableSpec) (service.TableDigest, error) {
	out := service.TableDigest{Name: spec.name, Columns: append([]string(nil), spec.columns...), Types: []string{}}
	rows, err := tx.Query(ctx, `SELECT a.attname,pg_catalog.format_type(a.atttypid,a.atttypmod),c.relkind::text FROM pg_catalog.pg_attribute a JOIN pg_catalog.pg_class c ON c.oid=a.attrelid JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2 AND a.attname=ANY($3::text[]) AND a.attnum>0 AND NOT a.attisdropped`, schema, spec.name, spec.columns)
	if err != nil {
		return out, err
	}
	types := map[string]string{}
	for rows.Next() {
		var name, kind, typ string
		if err = rows.Scan(&name, &typ, &kind); err != nil {
			rows.Close()
			return out, err
		}
		types[name] = typ
		out.RelationKind = kind
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if out.RelationKind != "r" {
		return out, fmt.Errorf("covered relation must be a table")
	}
	expressions := make([]string, len(spec.columns))
	for i, column := range spec.columns {
		typ, ok := types[column]
		if !ok {
			return out, fmt.Errorf("covered field unavailable")
		}
		out.Types = append(out.Types, typ)
		expressions[i] = fieldExpression(column)
	}
	rows, err = tx.Query(ctx, `SELECT pg_catalog.jsonb_build_array(`+strings.Join(expressions, ",")+`) FROM `+pgx.Identifier{schema, spec.name}.Sanitize())
	if err != nil {
		return out, err
	}
	hashes := [][32]byte{}
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return out, err
		}
		wire, e := integrityformat.CanonicalJSON(raw)
		if e != nil {
			rows.Close()
			return out, e
		}
		material := append([]byte("poaktu:integrity:row:v1\x00"), frame([]byte(spec.name))...)
		hashes = append(hashes, sha256.Sum256(append(material, wire...)))
		out.Rows++
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	sort.Slice(hashes, func(i, j int) bool { return bytes.Compare(hashes[i][:], hashes[j][:]) < 0 })
	descriptor, err := integrityformat.CanonicalJSON(mustJSON(struct {
		Name           string `json:"name"`
		Kind           string `json:"kind"`
		Columns, Types []string
	}{out.Name, out.RelationKind, out.Columns, out.Types}))
	if err != nil {
		return out, err
	}
	h := sha256.New()
	_, _ = h.Write([]byte("poaktu:integrity:table:v1\x00"))
	_, _ = h.Write(frame(descriptor))
	var count [8]byte
	binary.BigEndian.PutUint64(count[:], uint64(out.Rows))
	_, _ = h.Write(count[:])
	for _, sum := range hashes {
		_, _ = h.Write(sum[:])
	}
	out.Hash = hex.EncodeToString(h.Sum(nil))
	return out, nil
}
func mustJSON(value any) []byte { raw, _ := json.Marshal(value); return raw }
func (r *Repository) pdfs(ctx context.Context, tx pgx.Tx, schema string, out *service.Snapshot) error {
	out.PDFAvailable = true
	out.PDFValid = true
	rows, err := tx.Query(ctx, `SELECT document_id,document_version,storage_key,sha256,size_bytes FROM `+pgx.Identifier{schema, "document_versions"}.Sanitize())
	if err != nil {
		return err
	}
	defer rows.Close()
	hashes := [][32]byte{}
	for rows.Next() {
		if err = ctx.Err(); err != nil {
			return err
		}
		var id, key, expected string
		var version, size int64
		if err = rows.Scan(&id, &version, &key, &expected, &size); err != nil {
			return err
		}
		out.PDFCount++
		actual := "unavailable"
		var actualSize int64
		if r.files == nil {
			out.PDFAvailable = false
			out.PDFValid = false
		} else {
			body, e := r.files.Read(key)
			if e != nil {
				out.PDFAvailable = false
				out.PDFValid = false
			} else {
				sum := sha256.Sum256(body)
				actual = encodeHash(sum)
				actualSize = int64(len(body))
				if actual != expected || actualSize != size {
					out.PDFValid = false
				}
			}
		}
		sum, e := hashValue("poaktu:integrity:pdf-row:v1\x00", []any{id, version, actual, actualSize})
		if e != nil {
			return e
		}
		hashes = append(hashes, sum)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	sort.Slice(hashes, func(i, j int) bool { return bytes.Compare(hashes[i][:], hashes[j][:]) < 0 })
	h := sha256.New()
	_, _ = h.Write([]byte("poaktu:integrity:pdfs:v1\x00"))
	var count [8]byte
	binary.BigEndian.PutUint64(count[:], uint64(out.PDFCount))
	_, _ = h.Write(count[:])
	for _, sum := range hashes {
		_, _ = h.Write(sum[:])
	}
	out.PDFHash = hex.EncodeToString(h.Sum(nil))
	return nil
}
