package postgres

import (
	"context"
	"time"

	"github.com/Adopten123/digital-ruble-deals/internal/features/audit/service"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }
func (r *Repository) Read(ctx context.Context, q service.Query) ([]service.Record, error) {
	// Scope validation and selection share a request-local snapshot. Historical
	// version scope additionally prevents a replacement supplier seeing old rounds.
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var allowed bool
	if q.Actor.Operator {
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND system_role='DEMO_OPERATOR')`, q.Actor.UserID).Scan(&allowed)
	} else {
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM memberships m JOIN users u ON u.id=m.user_id WHERE m.user_id=$1 AND m.organization_id=$2 AND m.role='ORG_OWNER' AND u.system_role='USER')`, q.Actor.UserID, q.Actor.OrganizationID).Scan(&allowed)
	}
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, service.ErrForbidden
	}
	if !q.Actor.Operator {
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM deals d JOIN deal_versions v ON v.deal_id=d.id AND v.deal_version=d.deal_version WHERE d.id=$1 AND ($2=v.customer_party_id OR $2=v.supplier_party_id))`, q.Filters.DealID, q.Actor.OrganizationID).Scan(&allowed); err != nil {
			return nil, err
		}
		if !allowed {
			return nil, service.ErrNotFound
		}
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

	// PostgreSQL stores microseconds; pgx truncates Go nanoseconds on encoding.
	// Round only SQL bounds inward, preserving the exact requested filters in
	// the signed cursor. A valid interval without a stored microsecond is empty.
	from, to := q.Filters.From, q.Filters.To
	if from != nil {
		t := from.Truncate(time.Microsecond)
		if t.Before(*from) {
			t = t.Add(time.Microsecond)
		}
		from = &t
	}
	if to != nil {
		t := to.Truncate(time.Microsecond)
		to = &t
	}
	rows, err := tx.Query(ctx, `SELECT a.id,a.deal_id,a.deal_version,a.type,a.actor_user_id,a.actor_organization_id,a.actor_source,a.request_id,a.created_at,a.payload
 FROM audit_events a
 WHERE ($1::boolean OR EXISTS(SELECT 1 FROM deal_versions v WHERE v.deal_id=a.deal_id AND v.deal_version=a.deal_version AND ($2=v.customer_party_id OR $2=v.supplier_party_id)))
 AND ($3='' OR a.deal_id=$3) AND ($4='' OR a.type=$4)
 AND ($5::timestamptz IS NULL OR a.created_at >= $5) AND ($6::timestamptz IS NULL OR a.created_at <= $6)
 AND ($7::timestamptz IS NULL OR (a.created_at,a.id)<($7::timestamptz,$8::text))
 AND ($9::timestamptz IS NULL OR (a.created_at,a.id)<=($9::timestamptz,$10::text))
 ORDER BY a.created_at DESC,a.id DESC LIMIT $11`, q.Actor.Operator, q.Actor.OrganizationID, q.Filters.DealID, q.Filters.Type, from, to, before, beforeID, highwater, highwaterID, q.Filters.Limit)
	if err != nil {
		return nil, err
	}
	out := []service.Record{}
	for rows.Next() {
		var record service.Record
		if err = rows.Scan(&record.ID, &record.DealID, &record.DealVersion, &record.Type, &record.ActorUserID, &record.ActorOrganizationID, &record.ActorSource, &record.RequestID, &record.CreatedAt, &record.Payload); err != nil {
			rows.Close()
			return nil, err
		}
		record.CreatedAt = record.CreatedAt.UTC()
		out = append(out, record)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}
