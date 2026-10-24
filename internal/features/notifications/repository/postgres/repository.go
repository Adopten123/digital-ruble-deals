package postgres

import (
	"context"
	"time"

	corepg "github.com/Adopten123/digital-ruble-deals/internal/core/repository/postgres"
	"github.com/Adopten123/digital-ruble-deals/internal/features/notifications/service"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// The same visibility predicate serves list, unread count and read mutation.
// The recipient is personal; organization and both deal rounds are rechecked.
const visibility = ` n.recipient_user_id=$1 AND n.recipient_organization_id=$2
 AND EXISTS(SELECT 1 FROM memberships m JOIN users u ON u.id=m.user_id WHERE m.user_id=$1 AND m.organization_id=$2 AND m.role='ORG_OWNER' AND u.system_role='USER')
 AND (
  (a.deal_id IS NOT NULL
   AND EXISTS(SELECT 1 FROM deals d JOIN deal_versions c ON c.deal_id=d.id AND c.deal_version=d.deal_version WHERE d.id=a.deal_id AND ($2=c.customer_party_id OR $2=c.supplier_party_id))
   AND EXISTS(SELECT 1 FROM deal_versions h WHERE h.deal_id=a.deal_id AND h.deal_version=a.deal_version AND ($2=h.customer_party_id OR $2=h.supplier_party_id)))
  OR (a.deal_id IS NULL AND a.type IN ('DEMO_CREDITED','DEMO_DEBITED')
   AND EXISTS(SELECT 1 FROM accounts acc WHERE acc.id=a.payload#>>'{account,id}' AND acc.organization_id=$2))
 ) `
const joined = ` FROM notifications n JOIN audit_events a ON a.id=n.event_id `

func (r *Repository) List(ctx context.Context, q service.Query) (service.Result, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return service.Result{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	out := service.Result{Items: []service.Record{}}
	var allowed bool
	if q.Actor.Operator {
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND system_role='DEMO_OPERATOR')`, q.Actor.UserID).Scan(&allowed); err != nil {
			return out, err
		}
		if !allowed {
			return out, service.ErrForbidden
		}
		return out, tx.Commit(ctx)
	}
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM memberships m JOIN users u ON u.id=m.user_id WHERE m.user_id=$1 AND m.organization_id=$2 AND m.role='ORG_OWNER' AND u.system_role='USER')`, q.Actor.UserID, q.Actor.OrganizationID).Scan(&allowed); err != nil {
		return out, err
	}
	if !allowed {
		return out, service.ErrForbidden
	}
	if err = tx.QueryRow(ctx, `SELECT count(*)`+joined+`WHERE `+visibility+` AND n.read_at IS NULL`, q.Actor.UserID, q.Actor.OrganizationID).Scan(&out.UnreadCount); err != nil {
		return out, err
	}
	var before, highwater *time.Time
	var beforeID, highwaterID string
	if q.Before != nil {
		before = &q.Before.Time
		beforeID = q.Before.ID
	}
	if q.Highwater != nil {
		highwater = &q.Highwater.Time
		highwaterID = q.Highwater.ID
	}
	rows, err := tx.Query(ctx, `SELECT n.id,n.created_at,n.read_at,a.id,a.deal_id,a.deal_version,a.type,a.actor_user_id,a.actor_organization_id,a.actor_source,a.request_id,a.created_at,a.payload`+joined+` WHERE `+visibility+`
 AND ($3::boolean IS NULL OR (n.read_at IS NULL)=$3)
 AND ($4::timestamptz IS NULL OR (n.created_at,n.id)<($4::timestamptz,$5::text))
 AND ($6::timestamptz IS NULL OR (n.created_at,n.id)<=($6::timestamptz,$7::text))
 ORDER BY n.created_at DESC,n.id DESC LIMIT $8`, q.Actor.UserID, q.Actor.OrganizationID, q.Filters.Unread, before, beforeID, highwater, highwaterID, q.Filters.Limit)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var item service.Record
		if err = rows.Scan(&item.ID, &item.CreatedAt, &item.ReadAt, &item.Event.ID, &item.Event.DealID, &item.Event.DealVersion, &item.Event.Type, &item.Event.ActorUserID, &item.Event.ActorOrganizationID, &item.Event.ActorSource, &item.Event.RequestID, &item.Event.CreatedAt, &item.Event.Payload); err != nil {
			rows.Close()
			return out, err
		}
		out.Items = append(out.Items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}
func (r *Repository) Read(ctx context.Context, actor service.Actor, id string) error {
	return (corepg.UnitOfWork{Pool: r.pool}).WithinTx(ctx, func(tx pgx.Tx) error {
		result, err := tx.Exec(ctx, `UPDATE notifications n SET read_at=COALESCE(n.read_at,clock_timestamp()) FROM audit_events a WHERE a.id=n.event_id AND `+visibility+` AND n.id=$3`, actor.UserID, actor.OrganizationID, id)
		if err != nil {
			return err
		}
		if result.RowsAffected() == 0 {
			return service.ErrNotFound
		}
		return nil
	})
}
