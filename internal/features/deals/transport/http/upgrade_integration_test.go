package dealshttp_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Adopten123/digital-ruble-deals/internal/core/documentfile"
	corepg "github.com/Adopten123/digital-ruble-deals/internal/core/repository/postgres"
	"github.com/Adopten123/digital-ruble-deals/internal/core/security"
	"github.com/Adopten123/digital-ruble-deals/internal/features/bootstrap"
	deals "github.com/Adopten123/digital-ruble-deals/internal/features/deals/service"
	edo "github.com/Adopten123/digital-ruble-deals/internal/features/edo/service"
	edohttp "github.com/Adopten123/digital-ruble-deals/internal/features/edo/transport/http"
	"github.com/Adopten123/digital-ruble-deals/internal/testutil"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http/httptest"
)

// Unlike testutil.Database, this helper intentionally stops at the deployed 006
// schema. It never migrates public: every query runs in a unique disposable
// schema and cleanup drops only that exact, safely quoted schema.
func old006Database(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL absent: PostgreSQL upgrade check skipped")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	id, err := security.ID()
	if err != nil {
		admin.Close()
		t.Fatal(err)
	}
	schema := "upgrade_test_" + strings.ReplaceAll(strings.ToLower(id), "-", "_")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanup, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	dir := t.TempDir()
	for _, name := range []string{"001_foundation.sql", "002_deal_drafts.sql", "003_deal_agreement.sql", "004_deal_reserves.sql", "005_guard_search_path.sql", "006_test_signatures.sql"} {
		raw, err := os.ReadFile(filepath.Join(testutil.MigrationsDir(), name))
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err = corepg.Migrate(ctx, corepg.UnitOfWork{Pool: pool}, dir); err != nil {
		t.Fatal(err)
	}
	var version string
	if err = pool.QueryRow(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil || version != "006_test_signatures.sql" {
		t.Fatal("fixture did not stop at 006", version, err)
	}
	var later *string
	if err = pool.QueryRow(ctx, `SELECT to_regclass('event_inbox')::text`).Scan(&later); err != nil || later != nil {
		t.Fatal("new inbox existed before upgrade", later, err)
	}
	return pool
}

// Every old table is compared, including bytea response bodies and immutable
// ledger rows. The one newly added nullable audit column is excluded on both
// sides; schema_migrations compares only the six already applied records.
func old006Facts(t *testing.T, pool *pgxpool.Pool) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, table := range []string{"users", "organizations", "memberships", "sessions", "accounts", "ledger_batches", "ledger_entries", "bootstrap_runs", "deals", "deal_versions", "documents", "document_versions", "deal_agreements", "document_signatures", "reserves", "idempotency_keys", "operator_command_results", "audit_events", "outbox", "edo_outbox", "schema_migrations"} {
		expression := "to_jsonb(x)"
		filter := ""
		if table == "audit_events" {
			expression = "to_jsonb(x)-'actor_source'"
		}
		if table == "schema_migrations" {
			filter = " WHERE version<'007'"
		}
		var value string
		if err := pool.QueryRow(context.Background(), "SELECT COALESCE(jsonb_agg("+expression+" ORDER BY ("+expression+")::text),'[]'::jsonb)::text FROM "+table+" x"+filter).Scan(&value); err != nil {
			t.Fatal(table, err)
		}
		out[table] = value
	}
	return out
}

type old006Deal struct {
	ID, DocumentID, Hash, ReserveID string
	Version                         int64
	SignatureInput                  deals.SignInput
	FirstResponse                   []byte
	EventRaw                        []byte
}

func amountStringForUpgrade(n int64) string { return strconv.FormatInt(n, 10) }

// These are legitimate 006 records, built through its actual FK, context,
// immutable-fact and deferred-publication guards. Current deal repositories
// cannot build the old fixture because their SELECTs already reference 007.
func seed006Deal(t *testing.T, f *fixture, id string, amount int64, ready bool) old006Deal {
	t.Helper()
	ctx := context.Background()
	var customerAccount string
	if err := f.pool.QueryRow(ctx, `SELECT id FROM accounts WHERE organization_id=$1`, f.customer.org).Scan(&customerAccount); err != nil {
		t.Fatal(err)
	}
	old := old006Deal{ID: id, DocumentID: id + "-document", ReserveID: id + "-reserve", Version: 2}
	deadline := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Microsecond)
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO deals(id) VALUES($1)`, id)
	exec(`INSERT INTO documents(id,deal_id) VALUES($1,$2)`, old.DocumentID, id)
	for version := int64(1); version <= 2; version++ {
		title := "Условия до обновления"
		if version == 2 {
			title = "Окончательные условия"
		}
		exec(`INSERT INTO deal_versions(deal_id,deal_version,customer_party_id,supplier_party_id,title,description,amount_kopecks,currency,deadline_at,created_by) VALUES($1,$2,$3,$4,$5,'Учебная историческая версия',$8,'RUB',$6,$7)`, id, version, f.customer.org, f.supplier.org, title, deadline, f.customer.user, amount)
		pdf, err := documentfile.Generate(documentfile.Act{DealID: id, DealVersion: version, Title: title, Description: "Учебная историческая версия", CustomerName: "Демо Заказчик", SupplierName: "Демо Типография", AmountKopecks: amountStringForUpgrade(amount), Currency: "RUB", DeadlineAt: deadline.Format(time.RFC3339Nano)})
		if err != nil {
			t.Fatal(err)
		}
		key, err := f.files.Put(pdf)
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(pdf)
		old.Hash = hex.EncodeToString(hash[:])
		exec(`INSERT INTO document_versions(document_id,document_version,deal_id,deal_version,sha256,storage_key,file_name,size_bytes,content_type,created_by) VALUES($1,$2,$3,$2,$4,$5,'upgrade-act.pdf',$6,'application/pdf',$7)`, old.DocumentID, version, id, old.Hash, key, len(pdf), f.customer.user)
		for _, party := range []*client{f.customer, f.supplier} {
			exec(`INSERT INTO deal_agreements(id,deal_id,deal_version,organization_id,user_id,document_id,document_version,document_sha256) VALUES($1,$2,$3,$4,$5,$6,$3,$7)`, id+"-agreement-"+party.org+"-"+amountStringForUpgrade(version), id, version, party.org, party.user, old.DocumentID, old.Hash)
		}
	}
	exec(`UPDATE deals SET deal_version=2,current_document_id=$2,current_document_version=2,status='AGREED',row_version=2 WHERE id=$1`, id, old.DocumentID)
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	// Reservation has its own committed FUNDED phase, as required by 004.
	tx, err = f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	exec(`INSERT INTO reserves(id,deal_id,deal_version,account_id,customer_party_id,supplier_party_id,document_id,document_version,document_sha256,amount_kopecks,currency) VALUES($1,$2,2,$3,$4,$5,$6,2,$7,$8,'RUB')`, old.ReserveID, id, customerAccount, f.customer.org, f.supplier.org, old.DocumentID, old.Hash, amount)
	exec(`UPDATE accounts SET reserved_kopecks=reserved_kopecks+$2 WHERE id=$1`, customerAccount, amount)
	exec(`UPDATE deals SET status='FUNDED',first_reserved_at=clock_timestamp(),row_version=row_version+1 WHERE id=$1`, id)
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if !ready {
		return old
	}
	// Preserve both a historical exact command result and the persisted 006
	// signature pair / immutable source bytes used by the new dispatcher.
	old.SignatureInput = deals.SignInput{DealVersion: 2, DocumentVersion: 2, DocumentSHA256: old.Hash}
	for index, party := range []*client{f.customer, f.supplier} {
		tx, err = f.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		var now time.Time
		if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
			t.Fatal(err)
		}
		now = now.UTC()
		signature := deals.Signature{ID: id + "-signature-" + party.org, OrganisationID: party.org, UserID: party.user, DealVersion: 2, DocumentID: old.DocumentID, DocumentVersion: 2, DocumentSHA256: old.Hash, CreatedAt: now}
		exec(`INSERT INTO document_signatures(id,deal_id,deal_version,organization_id,user_id,document_id,document_version,document_sha256,created_at) VALUES($1,$2,2,$3,$4,$5,2,$6,$7)`, signature.ID, id, party.org, party.user, old.DocumentID, old.Hash, now)
		if index == 0 {
			old.FirstResponse, _ = json.Marshal(map[string]any{"signature": signature, "deal": map[string]any{"id": id, "deal_version": 2, "status": "FUNDED", "amount_kopecks": amountStringForUpgrade(amount), "signatures": []deals.Signature{signature}}})
			body, _ := json.Marshal(old.SignatureInput)
			hash := sha256.Sum256(body)
			exec(`INSERT INTO idempotency_keys(organization_id,operation,resource,key,request_hash,status_code,response_body) VALUES($1,'SIGN_DOCUMENT',$2,'upgrade-first-signature-key',$3,201,$4)`, party.org, "/documents/"+old.DocumentID, hex.EncodeToString(hash[:]), old.FirstResponse)
		} else {
			customerSig := id + "-signature-" + f.customer.org
			supplierSig := signature.ID
			event := edo.Event{SchemaVersion: 1, EventID: id + "-old-event", Source: edo.Source, EventType: "document.accepted_by_both", DealID: id, DealVersion: 2, DocumentID: old.DocumentID, DocumentVersion: 2, DocumentSHA256: old.Hash, CustomerPartyID: f.customer.org, SupplierPartyID: f.supplier.org, AmountKopecks: amountStringForUpgrade(amount), Currency: "RUB", SignedByCustomer: true, SignedBySupplier: true, CustomerSignatureID: &customerSig, SupplierSignatureID: &supplierSig, OccurredAt: now}
			old.EventRaw, _ = json.Marshal(event)
			exec(`INSERT INTO audit_events(id,deal_id,deal_version,type,actor_user_id,actor_organization_id,request_id,payload,created_at) VALUES($1,$2,2,'EDO_EVENT_READY',$3,$4,'upgrade-old-request',$5,$6)`, event.EventID, id, party.user, party.org, old.EventRaw, now)
			exec(`INSERT INTO outbox(id,event_id,type,deal_id,payload,created_at) VALUES($1,$2,'EDO_EVENT_READY',$3,$4,$5)`, id+"-old-delivery", event.EventID, id, old.EventRaw, now)
			exec(`INSERT INTO edo_outbox(event_id,deal_id,deal_version,document_id,document_version,document_sha256,customer_signature_id,supplier_signature_id,occurred_at,raw_body) VALUES($1,$2,2,$3,2,$4,$5,$6,$7,$8)`, event.EventID, id, old.DocumentID, old.Hash, customerSig, supplierSig, now, old.EventRaw)
			exec(`UPDATE deals SET status='READY_TO_SETTLE',row_version=row_version+1,updated_at=$2 WHERE id=$1`, id, now)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	return old
}

func TestUpgrade006PreservesHistoricalFactsAndPaysOldReadyDealOnce(t *testing.T) {
	f := &fixture{pool: old006Database(t)}
	var err error
	f.files, err = documentfile.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.files.Close() })
	if err = bootstrap.Run(context.Background(), f.pool); err != nil {
		t.Fatal(err)
	}
	f.server = httptest.NewServer(f.handler(t))
	t.Cleanup(f.server.Close)
	f.customer = f.session(t, "customer@demo.poaktu.local")
	f.supplier = f.session(t, "supplier@demo.poaktu.local")
	f.operator = f.session(t, "operator@demo.poaktu.local")
	ready := seed006Deal(t, f, "upgrade-ready-deal", 30000000, true)
	funded := seed006Deal(t, f, "upgrade-funded-deal", 10000000, false)
	before := old006Facts(t, f.pool)
	assertBalance(t, f, f.customer.org, 50000000, 40000000)
	if err = corepg.Migrate(context.Background(), corepg.UnitOfWork{Pool: f.pool}, testutil.MigrationsDir()); err != nil {
		t.Fatal(err)
	}
	if after := old006Facts(t, f.pool); !reflect.DeepEqual(before, after) {
		for table, old := range before {
			if old != after[table] {
				t.Errorf("upgrade changed existing %s rows", table)
			}
		}
		t.Fatal("007/008 changed 006 facts")
	}
	for _, table := range []string{"deal_disputes", "cancellation_requests", "cancellation_decisions", "settlements", "event_inbox", "edo_deliveries", "edo_command_results"} {
		if count(t, f.pool, table) != 0 {
			t.Fatal("upgrade invented new domain facts", table)
		}
	}
	// A second migration/bootstrap is an upgrade restart, never a reseed.
	if err = corepg.Migrate(context.Background(), corepg.UnitOfWork{Pool: f.pool}, testutil.MigrationsDir()); err != nil {
		t.Fatal(err)
	}
	if err = bootstrap.Run(context.Background(), f.pool); err != nil {
		t.Fatal(err)
	}
	if after := old006Facts(t, f.pool); !reflect.DeepEqual(before, after) {
		t.Fatal("repeat migration/bootstrap changed old facts")
	}
	_, reply := f.request(t, f.customer, "POST", "/api/v1/documents/"+ready.DocumentID+"/sign", "upgrade-first-signature-key", encode(ready.SignatureInput), 201)
	if !bytes.Equal(reply, ready.FirstResponse) {
		t.Fatal("upgrade changed historical idempotent response bytes")
	}
	_, reply = f.request(t, f.customer, "GET", "/api/v1/deals/"+ready.ID, "", "", 200)
	card := dealDTO(t, reply)
	if card.Status != "READY_TO_SETTLE" || len(card.Signatures) != 2 || card.Reserve.Status != "ACTIVE" {
		t.Fatal("old ready deal did not load after upgrade", string(reply))
	}
	svc, server := edoServer(t, f, f.pool)
	sender, err := edohttp.NewSender(server.URL + "/internal/edo/events")
	if err != nil {
		t.Fatal(err)
	}
	worker := edo.NewWorker(svc, sender)
	if worked, err := worker.DeliverOnce(context.Background()); err != nil || !worked {
		t.Fatal("new worker did not discover old immutable EDO basis", worked, err)
	}
	response := receive(t, server, svc, ready.EventRaw, 200)
	if !strings.Contains(string(response), `"result":"settled"`) {
		t.Fatal("old event did not keep its paid receipt", string(response))
	}
	if worked, err := worker.DeliverOnce(context.Background()); err != nil || worked {
		t.Fatal("old source basis created another job after completion", worked, err)
	}
	if count(t, f.pool, "settlements") != 1 || count(t, f.pool, "event_inbox") != 1 {
		t.Fatal("upgrade paid old source more than once")
	}
	assertBalance(t, f, f.customer.org, 20000000, 10000000)
	assertBalance(t, f, f.supplier.org, 30000000, 0)
	var state string
	if err = f.pool.QueryRow(context.Background(), `SELECT status FROM reserves WHERE deal_id=$1`, funded.ID).Scan(&state); err != nil || state != "ACTIVE" {
		t.Fatal("upgrade payout touched the other funded reserve", state, err)
	}
	_, reply = f.request(t, f.customer, "POST", "/api/v1/documents/"+ready.DocumentID+"/sign", "upgrade-first-signature-key", encode(ready.SignatureInput), 201)
	if !bytes.Equal(reply, ready.FirstResponse) {
		t.Fatal("payment changed the old signature result")
	}
	assertMonetaryFacts(t, f)
}
