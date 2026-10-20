package postgres

import (
	"context"
	"errors"

	"github.com/Adopten123/digital-ruble-deals/internal/features/organizations/service"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }
func (r *Repository) Catalog(ctx context.Context, q service.Query) ([]service.CatalogItem, error) {
	rows, err := r.pool.Query(ctx, `SELECT id,name,kind,is_demo,created_at FROM organizations
 WHERE ($1='' OR strpos(lower(name),lower($1))>0 OR strpos(id,$1)>0)
 AND ($2::timestamptz IS NULL OR (created_at,id)<($2::timestamptz,$3::text)) ORDER BY created_at DESC,id DESC LIMIT $4`, q.Search, q.BeforeTime, q.BeforeID, q.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []service.CatalogItem{}
	for rows.Next() {
		var item service.CatalogItem
		if err := rows.Scan(&item.ID, &item.Name, &item.Kind, &item.IsDemo, &item.CreatedAt); err != nil {
			return nil, err
		}
		item.CreatedAt = item.CreatedAt.UTC()
		out = append(out, item)
	}
	return out, rows.Err()
}
func (r *Repository) Own(ctx context.Context, userID, organizationID string) (out service.OwnOrganization, err error) {
	err = r.pool.QueryRow(ctx, `SELECT o.id,o.name,o.kind,m.role,a.id,a.currency,a.total_kopecks::text,a.reserved_kopecks::text,(a.total_kopecks-a.reserved_kopecks)::text
 FROM memberships m JOIN organizations o ON o.id=m.organization_id JOIN accounts a ON a.organization_id=o.id
 WHERE m.user_id=$1 AND m.organization_id=$2 AND m.role='ORG_OWNER'`, userID, organizationID).Scan(&out.Organization.ID, &out.Organization.Name, &out.Organization.Kind, &out.Organization.Role, &out.Account.ID, &out.Account.Currency, &out.Account.Total, &out.Account.Reserved, &out.Account.Available)
	if errors.Is(err, pgx.ErrNoRows) {
		err = service.ErrForbidden
	}
	return out, err
}
