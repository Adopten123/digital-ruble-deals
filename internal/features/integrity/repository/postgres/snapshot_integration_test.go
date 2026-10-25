package postgres_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"github.com/Adopten123/digital-ruble-deals/internal/core/documentfile"
	"github.com/Adopten123/digital-ruble-deals/internal/core/integritylock"
	repo "github.com/Adopten123/digital-ruble-deals/internal/features/integrity/repository/postgres"
	svc "github.com/Adopten123/digital-ruble-deals/internal/features/integrity/service"
	"github.com/Adopten123/digital-ruble-deals/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func ownerSQL(t *testing.T, p *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	var schema string
	if err := p.QueryRow(context.Background(), `SELECT current_schema()`).Scan(&schema); err != nil || !strings.HasPrefix(schema, "test_") {
		t.Fatal("owner mutation requires an isolated test schema", err)
	}
	if _, err := p.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}
func capture(t *testing.T, r *repo.Repository) svc.Snapshot {
	t.Helper()
	s, err := r.Capture(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func checkpoint(t *testing.T, s svc.Snapshot) (svc.Checkpoint, ed25519.PublicKey) {
	t.Helper()
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{11}, 32))
	c, err := svc.CreateCheckpoint(s, key)
	if err != nil {
		t.Fatalf("checkpoint: %v; snapshot %+v", err, s)
	}
	return c, key.Public().(ed25519.PublicKey)
}
func snapshotFixture(t *testing.T) (*pgxpool.Pool, *repo.Repository) {
	t.Helper()
	p := testutil.Database(t)
	chainActor(t, p)
	if err := chainInsert(context.Background(), p, "first"); err != nil {
		t.Fatal(err)
	}
	ownerSQL(t, p, `INSERT INTO organizations(id,name,kind) VALUES('customer','Customer','COMPANY'),('supplier','Supplier','COMPANY'); INSERT INTO accounts(id,organization_id) VALUES('account','customer'); BEGIN; INSERT INTO deals(id) VALUES('deal'); INSERT INTO deal_versions(deal_id,deal_version,customer_party_id,supplier_party_id,title,description,amount_kopecks,currency,deadline_at,created_by) VALUES('deal',1,'customer','supplier','Test','Terms',100,'RUB','2099-01-01','actor'); COMMIT;`)
	return p, repo.New(p, nil)
}
func TestCaptureStableCompleteAndReadOnly(t *testing.T) {
	p, r := snapshotFixture(t)
	a := capture(t, r)
	if len(a.Tables) != 28 || a.AuditCount != 1 || !a.ChainValid {
		t.Fatalf("incomplete snapshot %+v", a)
	}
	c, pub := checkpoint(t, a)
	b := capture(t, r)
	if got := svc.Verify(b, c, pub); got.Status != svc.MATCH {
		t.Fatal(got)
	}
	// Explicitly excluded identity credentials do not claim covered-state protection.
	ownerSQL(t, p, `UPDATE users SET email='changed@example.test',password_hash='changed' WHERE id='actor'`)
	if got := svc.Verify(capture(t, r), c, pub); got.Status != svc.MATCH {
		t.Fatal(got)
	}
	release, err := integritylock.HoldShared(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.Capture(context.Background()); err != integritylock.ErrBusy {
		t.Fatalf("live API not refused: %v", err)
	}
	release()
	if got := svc.Verify(capture(t, r), c, pub); got.Status != svc.MATCH {
		t.Fatal(got)
	}
}
func TestIndependentVerifierDetectsOwnerMutations(t *testing.T) {
	cases := []struct {
		name, sql string
		want      svc.Status
	}{
		{"audit_payload", `ALTER TABLE audit_events DISABLE TRIGGER USER; UPDATE audit_events SET payload='{"changed":true}' WHERE id='first'`, svc.CHAIN_BROKEN},
		{"account_balance", `ALTER TABLE accounts DISABLE TRIGGER USER; UPDATE accounts SET total_kopecks=1 WHERE id='account'`, svc.MISMATCH},
		{"deal_terms", `ALTER TABLE deal_versions DISABLE TRIGGER USER; UPDATE deal_versions SET amount_kopecks=101 WHERE deal_id='deal'`, svc.MISMATCH},
		{"role", `UPDATE users SET system_role='DEMO_OPERATOR' WHERE id='actor'`, svc.MISMATCH},
		{"missing_link", `ALTER TABLE audit_chain_entries DISABLE TRIGGER USER; DELETE FROM audit_chain_entries WHERE sequence=1`, svc.CHAIN_BROKEN},
		{"missing_table", `ALTER TABLE settlements RENAME TO hidden_settlements`, svc.UNVERIFIABLE},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, r := snapshotFixture(t)
			c, pub := checkpoint(t, capture(t, r))
			ownerSQL(t, p, tc.sql)
			s, err := r.Capture(context.Background())
			report := svc.Unverifiable(err)
			if err == nil {
				report = svc.Verify(s, c, pub)
			}
			if report.Status != tc.want {
				t.Fatalf("%s: %+v", tc.name, report)
			}
		})
	}
}
func TestRehashedHistoryAndRollbackNeedIndependentLatestReference(t *testing.T) {
	t.Run("rehashed", func(t *testing.T) {
		p, r := snapshotFixture(t)
		c, pub := checkpoint(t, capture(t, r))
		ownerSQL(t, p, `ALTER TABLE audit_events DISABLE TRIGGER USER; ALTER TABLE audit_chain_entries DISABLE TRIGGER USER; ALTER TABLE audit_chain_head DISABLE TRIGGER USER; UPDATE audit_events SET payload='{"changed":true}' WHERE id='first'; UPDATE audit_chain_entries c SET event_hash=integrity_event_hash_v1(a) FROM audit_events a WHERE a.id=c.audit_id; UPDATE audit_chain_entries SET entry_hash=integrity_link_hash_v1(log_id,sequence,previous_hash,event_hash); UPDATE audit_chain_head SET head_hash=(SELECT entry_hash FROM audit_chain_entries WHERE sequence=1)`)
		s := capture(t, r)
		if !s.ChainValid {
			t.Fatal("test did not construct an internally consistent forged chain", s.ChainIssues)
		}
		if got := svc.Verify(s, c, pub); got.Status != svc.MISMATCH {
			t.Fatal(got)
		}
		// Replacing SQL hash helpers cannot make the Go verifier accept raw modifications.
		ownerSQL(t, p, `UPDATE audit_events SET payload='{"another":true}' WHERE id='first'; CREATE OR REPLACE FUNCTION integrity_event_hash_v1(ev audit_events) RETURNS bytea LANGUAGE sql STABLE AS $$ SELECT c.event_hash FROM audit_chain_entries c WHERE c.audit_id=ev.id $$`)
		var helperLies bool
		if err := p.QueryRow(context.Background(), `SELECT integrity_event_hash_v1(a)=c.event_hash FROM audit_events a JOIN audit_chain_entries c ON c.audit_id=a.id WHERE a.id='first'`).Scan(&helperLies); err != nil || !helperLies {
			t.Fatal("forged SQL helper fixture", err)
		}
		if got := svc.Verify(capture(t, r), c, pub); got.Status != svc.CHAIN_BROKEN {
			t.Fatal(got)
		}
	})
	t.Run("tail_truncation_and_full_rollback", func(t *testing.T) {
		p, r := snapshotFixture(t)
		old := capture(t, r)
		oldC, pub := checkpoint(t, old)
		if err := chainInsert(context.Background(), p, "second"); err != nil {
			t.Fatal(err)
		}
		forward := capture(t, r)
		if got := svc.Verify(forward, oldC, pub); got.Status != svc.MISMATCH {
			t.Fatal("ordinary continuation must be a neutral mismatch", got)
		}
		latestC, _ := checkpoint(t, forward)
		if svc.CheckpointFingerprint(oldC) == svc.CheckpointFingerprint(latestC) {
			t.Fatal("latest pin unchanged")
		}
		ownerSQL(t, p, `ALTER TABLE audit_events DISABLE TRIGGER USER; ALTER TABLE audit_chain_entries DISABLE TRIGGER USER; ALTER TABLE audit_chain_head DISABLE TRIGGER USER; DELETE FROM audit_chain_entries WHERE sequence=2; DELETE FROM audit_events WHERE id='second'; UPDATE audit_chain_head SET last_seq=1,head_hash=(SELECT entry_hash FROM audit_chain_entries WHERE sequence=1)`)
		rolled := capture(t, r)
		if !rolled.ChainValid {
			t.Fatal(rolled.ChainIssues)
		}
		if got := svc.Verify(rolled, latestC, pub); got.Status != svc.MISMATCH {
			t.Fatal(got)
		}
		// An old signed point alone cannot reveal a full rollback; the CLI requires
		// its independently retained latest fingerprint as well as the public key.
		if got := svc.Verify(rolled, oldC, pub); got.Status != svc.MATCH {
			t.Fatal(got)
		}
	})
}
func TestPDFBytesAndMissingFilesCannotProduceGreenReport(t *testing.T) {
	p, _ := snapshotFixture(t)
	dir := t.TempDir()
	key := strings.Repeat("a", 32) + ".pdf"
	body := []byte("%PDF-1.4\nfixture\n%%EOF\n")
	hash := sha256.Sum256(body)
	if err := os.WriteFile(filepath.Join(dir, key), body, 0600); err != nil {
		t.Fatal(err)
	}
	ownerSQL(t, p, `INSERT INTO documents(id,deal_id) VALUES('document','deal')`)
	ownerSQL(t, p, `INSERT INTO document_versions(document_id,document_version,deal_id,deal_version,sha256,storage_key,file_name,size_bytes,content_type,created_by) VALUES('document',1,'deal',1,$1,$2,'test.pdf',$3,'application/pdf','actor')`, hex.EncodeToString(hash[:]), key, len(body))
	files, err := documentfile.OpenReadOnlyStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()
	r := repo.New(p, files)
	c, pub := checkpoint(t, capture(t, r))
	if err = os.WriteFile(filepath.Join(dir, key), append(body, '!'), 0600); err != nil {
		t.Fatal(err)
	}
	if got := svc.Verify(capture(t, r), c, pub); got.Status != svc.MISMATCH {
		t.Fatal(got)
	}
	if err = os.Rename(filepath.Join(dir, key), filepath.Join(dir, key+".missing")); err != nil {
		t.Fatal(err)
	}
	if got := svc.Verify(capture(t, r), c, pub); got.Status != svc.UNVERIFIABLE {
		t.Fatal(got)
	}
}
