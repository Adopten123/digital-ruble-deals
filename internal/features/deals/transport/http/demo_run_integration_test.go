package dealshttp_test

import (
	"context"
	dealrepo "github.com/Adopten123/digital-ruble-deals/internal/features/deals/repository/postgres"
	deals "github.com/Adopten123/digital-ruble-deals/internal/features/deals/service"
	demorepo "github.com/Adopten123/digital-ruble-deals/internal/features/demorun/repository/postgres"
	demo "github.com/Adopten123/digital-ruble-deals/internal/features/demorun/service"
	"reflect"
	"testing"
)

func TestDemoRepeatAfterActualSettlementDoesNotResetOrCreditAgain(t *testing.T) {
	f := reserveFixture(t)
	ctx := context.Background()
	setup := demo.New(demorepo.New(f.pool), deals.New(dealrepo.New(f.pool), make([]byte, 32)), make([]byte, 32))
	first, err := setup.Create(ctx, "paid-show", "DemoRunOnly2026!")
	if err != nil {
		t.Fatal(err)
	}
	f.customer = f.session(t, first.Identities[0].Email)
	f.supplier = f.session(t, first.Identities[1].Email)
	d, err := dealrepo.New(f.pool).Get(ctx, deals.Actor{UserID: f.customer.user, OrganizationID: f.customer.org}, first.Deals[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	d, _ = generate(t, f, d, "demo-original-pdf")
	d = agree(t, f, f.customer, d, "demo-customer-agree")
	d = agree(t, f, f.supplier, d, "demo-supplier-agree")
	_, raw := f.request(t, f.customer, "POST", "/api/v1/deals/"+d.ID+"/reserve", "demo-original-reserve", reserveBody(d), 201)
	d, _ = reserveDTO(t, raw)
	sign(t, f, f.customer, d, "demo-customer-sign")
	sign(t, f, f.supplier, d, "demo-supplier-sign")
	svc, server := edoServer(t, f, f.pool)
	receive(t, server, svc, edoBytes(t, f, d), 200)
	before := f.accountSnapshot(t)
	events := count(t, f.pool, "audit_events")
	batches := count(t, f.pool, "ledger_batches")
	again, err := setup.Create(ctx, "paid-show", "DemoRunOnly2026!")
	if err != nil || !again.Resumed || !reflect.DeepEqual(first.Deals, again.Deals) {
		t.Fatal("replay failed", err)
	}
	if before != f.accountSnapshot(t) || events != count(t, f.pool, "audit_events") || batches != count(t, f.pool, "ledger_batches") || count(t, f.pool, "settlements") != 1 {
		t.Fatal("replay reset money or history")
	}
	_, raw = f.request(t, f.customer, "GET", "/api/v1/deals/"+d.ID, "", "", 200)
	if dealDTO(t, raw).Status != "SETTLED" {
		t.Fatal("paid deal reset")
	}
	assertBalance(t, f, f.customer.org, 20000000, 0)
	assertBalance(t, f, f.supplier.org, 30000000, 0)
}
