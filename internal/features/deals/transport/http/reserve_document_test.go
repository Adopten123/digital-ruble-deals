package dealshttp_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Adopten123/digital-ruble-deals/internal/core/documentfile"
	repo "github.com/Adopten123/digital-ruble-deals/internal/features/deals/repository/postgres"
	service "github.com/Adopten123/digital-ruble-deals/internal/features/deals/service"
)

func TestReserveDoesNotCommitWhenAgreedPDFBytesDisappearOrChange(t *testing.T) {
	f := setup(t)
	t.Cleanup(func() { f.balances = f.accountSnapshot(t) })
	root := t.TempDir()
	store, e := documentfile.NewStore(root)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { store.Close() })
	svc := service.New(repo.New(f.pool), make([]byte, 32), store)
	actor := service.Actor{UserID: f.customer.user, OrganizationID: f.customer.org, RequestID: "pdf-integrity-reserve"}
	d := draft(t, f)
	out, e := svc.Generate(context.Background(), actor, d.ID, "integrity-pdf-key01", service.GenerateInput{ExpectedDealVersion: 1, TemplateID: "printing-acceptance-v1"})
	if e != nil || out.Status != 201 {
		t.Fatal("generate", e, string(out.Body))
	}
	var generated struct {
		Deal service.Deal `json:"deal"`
	}
	if e = json.Unmarshal(out.Body, &generated); e != nil {
		t.Fatal(e)
	}
	d = generated.Deal
	agree(t, f, f.customer, d, "integrity-agree-c01")
	d = agree(t, f, f.supplier, d, "integrity-agree-s01")
	var key string
	if e = f.pool.QueryRow(context.Background(), `SELECT storage_key FROM document_versions WHERE document_id=$1 AND document_version=$2`, d.Document.ID, d.Document.DocumentVersion).Scan(&key); e != nil {
		t.Fatal(e)
	}
	file := filepath.Join(root, key)
	original, e := os.ReadFile(file)
	if e != nil {
		t.Fatal(e)
	}
	before := f.accountSnapshot(t)
	for i, mode := range []string{"modified", "missing"} {
		if mode == "modified" {
			changed := append([]byte(nil), original...)
			changed[0] ^= 1
			e = os.WriteFile(file, changed, 0600)
		} else {
			e = os.Remove(file)
		}
		if e != nil {
			t.Fatal(e)
		}
		out, e = svc.Reserve(context.Background(), actor, d.ID, "integrity-reserve-01", service.ReserveInput{DealVersion: d.DealVersion})
		if e == nil || out.Status != 0 {
			t.Fatalf("%s PDF permitted reserve: status %d error %v", mode, out.Status, e)
		}
		if count(t, f.pool, "reserves") != 0 || f.accountSnapshot(t) != before {
			t.Fatal("corrupt/missing PDF changed money", i)
		}
	}
	if e = os.WriteFile(file, original, 0600); e != nil {
		t.Fatal(e)
	}
	out, e = svc.Reserve(context.Background(), actor, d.ID, "integrity-reserve-01", service.ReserveInput{DealVersion: d.DealVersion})
	if e != nil || out.Status != 201 {
		t.Fatal("transient PDF error incorrectly saved a terminal result", e, string(out.Body))
	}
}
