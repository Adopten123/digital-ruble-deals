package postgres

import (
	"context"
	"errors"

	corepg "github.com/Adopten123/digital-ruble-deals/internal/core/repository/postgres"
	"github.com/Adopten123/digital-ruble-deals/internal/features/auth/service"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct {
	pool *pgxpool.Pool
	uow  corepg.UnitOfWork
}

func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool, uow: corepg.UnitOfWork{Pool: pool}}
}

func (r *Repository) Register(ctx context.Context, in service.Registration) (identity service.Identity, err error) {
	err = r.uow.WithinTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO users(id,email,password_hash) VALUES($1,$2,$3)`, in.UserID, in.Email, in.PasswordHash); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO organizations(id,name,kind) VALUES($1,$2,$3)`, in.OrganizationID, in.Name, in.Kind); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO memberships(user_id,organization_id,role) VALUES($1,$2,'ORG_OWNER')`, in.UserID, in.OrganizationID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO accounts(id,organization_id) VALUES($1,$2)`, in.AccountID, in.OrganizationID); err != nil {
			return err
		}
		if err := replaceSession(ctx, tx, in.PreviousSessionHash, in.Session); err != nil {
			return err
		}
		identity, err = identityQuery(ctx, tx, in.Session.Hash)
		return err
	})
	var pgerr *pgconn.PgError
	if errors.As(err, &pgerr) && pgerr.Code == "23505" && pgerr.ConstraintName == "users_email_key" {
		err = service.ErrEmailConflict
	}
	return identity, err
}

func (r *Repository) Credentials(ctx context.Context, email string) (out service.Credentials, err error) {
	err = r.pool.QueryRow(ctx, `SELECT id,password_hash FROM users WHERE email=$1`, email).Scan(&out.UserID, &out.PasswordHash)
	if errors.Is(err, pgx.ErrNoRows) {
		err = service.ErrCredentials
	}
	return out, err
}

func replaceSession(ctx context.Context, tx pgx.Tx, oldHash string, in service.Session) error {
	if oldHash != "" {
		if _, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at=now() WHERE token_hash=$1 AND revoked_at IS NULL`, oldHash); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,$3)`, in.Hash, in.UserID, in.ExpiresAt)
	return err
}

func (r *Repository) ReplaceSession(ctx context.Context, oldHash string, in service.Session) (out service.Identity, err error) {
	err = r.uow.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := replaceSession(ctx, tx, oldHash, in); err != nil {
			return err
		}
		out, err = identityQuery(ctx, tx, in.Hash)
		return err
	})
	return out, err
}

type rowQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func identityQuery(ctx context.Context, q rowQuerier, hash string) (out service.Identity, err error) {
	var orgID, name, kind, role *string
	err = q.QueryRow(ctx, `SELECT u.id,u.email,u.system_role,o.id,o.name,o.kind,m.role,s.expires_at
 FROM sessions s JOIN users u ON u.id=s.user_id
 LEFT JOIN memberships m ON m.user_id=u.id LEFT JOIN organizations o ON o.id=m.organization_id
 WHERE s.token_hash=$1 AND s.revoked_at IS NULL AND s.expires_at>now()`, hash).Scan(&out.User.ID, &out.User.Email, &out.User.SystemRole, &orgID, &name, &kind, &role, &out.SessionExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, service.ErrNoSession
	}
	if err != nil {
		return out, err
	}
	if orgID != nil {
		out.Organization = &service.Organization{ID: *orgID, Name: *name, Kind: *kind, Role: *role}
	}
	out.SessionExpiresAt = out.SessionExpiresAt.UTC()
	return out, nil
}
func (r *Repository) Identity(ctx context.Context, hash string) (service.Identity, error) {
	return identityQuery(ctx, r.pool, hash)
}
func (r *Repository) Revoke(ctx context.Context, hash string) error {
	_, err := r.pool.Exec(ctx, `UPDATE sessions SET revoked_at=now() WHERE token_hash=$1 AND revoked_at IS NULL`, hash)
	return err
}
