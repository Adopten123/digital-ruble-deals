package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	corepg "github.com/Adopten123/digital-ruble-deals/internal/core/repository/postgres"
	"github.com/Adopten123/digital-ruble-deals/internal/core/security"
	"github.com/Adopten123/digital-ruble-deals/internal/features/deals/service"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

func (r *Repository) Create(ctx context.Context, cmd service.Command) (out service.Result, err error) {
	err = (corepg.UnitOfWork{Pool: r.pool}).WithinTx(ctx, func(tx pgx.Tx) error {
		var owner bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM memberships m JOIN users u ON u.id=m.user_id WHERE m.user_id=$1 AND m.organization_id=$2 AND m.role='ORG_OWNER' AND u.system_role='USER')`, cmd.Actor.UserID, cmd.Actor.OrganizationID).Scan(&owner); err != nil {
			return err
		}
		if !owner {
			return service.ErrForbidden
		}
		scope, _ := json.Marshal([]string{cmd.Actor.OrganizationID, "CREATE_DEAL", "/deals", cmd.Key})
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, string(scope)); err != nil {
			return err
		}
		var previousHash string
		err := tx.QueryRow(ctx, `SELECT request_hash,status_code,response_body FROM idempotency_keys WHERE organization_id=$1 AND operation='CREATE_DEAL' AND resource='/deals' AND key=$2`, cmd.Actor.OrganizationID, cmd.Key).Scan(&previousHash, &out.Status, &out.Body)
		if err == nil {
			if previousHash != cmd.Hash {
				out = service.Failure{Status: 409, Code: "IDEMPOTENCY_CONFLICT", Message: "Этот ключ уже использован с другим телом запроса"}.Result(cmd.Actor.RequestID)
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		persist := func() error {
			if out.Status >= 400 {
				var response struct {
					Error struct {
						Code string `json:"code"`
					} `json:"error"`
				}
				if err := json.Unmarshal(out.Body, &response); err != nil {
					return err
				}
				payload, err := json.Marshal(map[string]any{"operation": "CREATE_DEAL", "resource": "/deals", "http_status": out.Status, "error_code": response.Error.Code})
				if err != nil {
					return err
				}
				eventID, err := security.ID()
				if err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `INSERT INTO audit_events(id,type,actor_user_id,actor_organization_id,request_id,payload) VALUES($1,'DEAL_CREATE_REJECTED',$2,$3,$4,$5)`, eventID, cmd.Actor.UserID, cmd.Actor.OrganizationID, cmd.Actor.RequestID, payload); err != nil {
					return err
				}
			}
			_, err := tx.Exec(ctx, `INSERT INTO idempotency_keys(organization_id,operation,resource,key,request_hash,status_code,response_body) VALUES($1,'CREATE_DEAL','/deals',$2,$3,$4,$5)`, cmd.Actor.OrganizationID, cmd.Key, cmd.Hash, out.Status, out.Body)
			return err
		}
		if cmd.Invalid != nil {
			out = cmd.Invalid.Result(cmd.Actor.RequestID)
			return persist()
		}
		var now time.Time
		if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
			return err
		}
		now = now.UTC()
		if !cmd.Deadline.After(now) {
			out = service.Failure{Status: 422, Code: "DEADLINE_PASSED", Message: "Срок сделки уже наступил"}.Result(cmd.Actor.RequestID)
			return persist()
		}
		var customer, supplier service.Party
		if err := tx.QueryRow(ctx, `SELECT id,name,kind FROM organizations WHERE id=$1`, cmd.Actor.OrganizationID).Scan(&customer.ID, &customer.Name, &customer.Kind); err != nil {
			return err
		}
		err = tx.QueryRow(ctx, `SELECT id,name,kind FROM organizations WHERE id=$1`, cmd.Input.SupplierPartyID).Scan(&supplier.ID, &supplier.Name, &supplier.Kind)
		if errors.Is(err, pgx.ErrNoRows) {
			out = service.Failure{Status: 404, Code: "RESOURCE_NOT_FOUND", Message: "Контрагент не найден"}.Result(cmd.Actor.RequestID)
			return persist()
		}
		if err != nil {
			return err
		}
		id, err := security.ID()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO deals(id,created_at,updated_at) VALUES($1,$2,$2)`, id, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO deal_versions(deal_id,deal_version,customer_party_id,supplier_party_id,title,description,amount_kopecks,currency,deadline_at,created_by,created_at) VALUES($1,1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, id, customer.ID, supplier.ID, cmd.Input.Title, cmd.Input.Description, cmd.Amount, cmd.Input.Currency, cmd.Deadline, cmd.Actor.UserID, now); err != nil {
			return err
		}
		deal := service.Deal{ID: id, DealVersion: 1, Status: "DRAFT", CustomerPartyID: customer.ID, SupplierPartyID: supplier.ID, Title: cmd.Input.Title, Description: cmd.Input.Description, AmountKopecks: cmd.Input.AmountKopecks, Currency: cmd.Input.Currency, DeadlineAt: cmd.Deadline, Customer: customer, Supplier: supplier, CreatedAt: now, UpdatedAt: now}
		deal.Normalize()
		out.Body, err = json.Marshal(deal)
		if err != nil {
			return err
		}
		out.Status = 201
		eventID, err := security.ID()
		if err != nil {
			return err
		}
		jobID, err := security.ID()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO audit_events(id,deal_id,deal_version,type,actor_user_id,actor_organization_id,request_id,payload,created_at) VALUES($1,$2,1,'DEAL_CREATED',$3,$4,$5,$6,$7)`, eventID, id, cmd.Actor.UserID, cmd.Actor.OrganizationID, cmd.Actor.RequestID, out.Body, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO outbox(id,event_id,type,deal_id,payload,created_at) VALUES($1,$2,'DEAL_CREATED',$3,$4,$5)`, jobID, eventID, id, out.Body, now); err != nil {
			return err
		}
		return persist()
	})
	// Even when a response was prepared, a failed/unknown commit must never report success.
	if err != nil {
		return service.Result{}, err
	}
	return out, nil
}

const columns = `d.id,d.deal_version,
 CASE WHEN d.status IN ('SETTLED','CANCELLED','REFUNDED') THEN d.status WHEN v.deadline_at <= statement_timestamp() THEN 'REVIEW_REQUIRED' ELSE d.status END,
 v.customer_party_id,v.supplier_party_id,v.title,v.description,v.amount_kopecks::text,v.currency,v.deadline_at,
 c.id,c.name,c.kind,s.id,s.name,s.kind,d.created_at,d.updated_at,
 (SELECT jsonb_build_object('id',dv.document_id,'deal_id',dv.deal_id,'deal_version',dv.deal_version,'document_version',dv.document_version,'sha256',dv.sha256,'file_name',dv.file_name,'size_bytes',dv.size_bytes,'content_type',dv.content_type,'created_at',to_char(dv.created_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"')) FROM document_versions dv WHERE dv.document_id=d.current_document_id AND dv.document_version=d.current_document_version),
 COALESCE((SELECT jsonb_agg(jsonb_build_object('id',a.id,'organisation_id',a.organization_id,'user_id',a.user_id,'deal_version',a.deal_version,'document_id',a.document_id,'document_version',a.document_version,'document_sha256',a.document_sha256,'created_at',to_char(a.created_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"')) ORDER BY a.created_at,a.id) FROM deal_agreements a WHERE a.deal_id=d.id AND a.deal_version=d.deal_version AND a.document_id=d.current_document_id AND a.document_version=d.current_document_version),'[]'::jsonb)`
const joins = ` FROM deals d JOIN deal_versions v ON v.deal_id=d.id AND v.deal_version=d.deal_version
 JOIN organizations c ON c.id=v.customer_party_id JOIN organizations s ON s.id=v.supplier_party_id `
const scope = ` EXISTS(SELECT 1 FROM memberships m JOIN users u ON u.id=m.user_id WHERE m.user_id=$1 AND m.organization_id=$2 AND m.role='ORG_OWNER' AND u.system_role='USER') AND ($2=v.customer_party_id OR $2=v.supplier_party_id) `

type scanner interface{ Scan(...any) error }

func scan(row scanner) (d service.Deal, err error) {
	var document, agreements []byte
	err = row.Scan(&d.ID, &d.DealVersion, &d.Status, &d.CustomerPartyID, &d.SupplierPartyID, &d.Title, &d.Description, &d.AmountKopecks, &d.Currency, &d.DeadlineAt, &d.Customer.ID, &d.Customer.Name, &d.Customer.Kind, &d.Supplier.ID, &d.Supplier.Name, &d.Supplier.Kind, &d.CreatedAt, &d.UpdatedAt, &document, &agreements)
	if err == nil && len(document) > 0 {
		err = json.Unmarshal(document, &d.Document)
	}
	if err == nil && len(agreements) > 0 {
		err = json.Unmarshal(agreements, &d.Agreements)
	}
	d.Normalize()
	return d, err
}
func (r *Repository) Get(ctx context.Context, actor service.Actor, id string) (service.Deal, error) {
	d, err := scan(r.pool.QueryRow(ctx, `SELECT `+columns+joins+` WHERE `+scope+` AND d.id=$3`, actor.UserID, actor.OrganizationID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return service.Deal{}, service.ErrNotFound
	}
	return d, err
}
func (r *Repository) List(ctx context.Context, q service.Query) ([]service.Deal, error) {
	var owner bool
	if err := r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM memberships m JOIN users u ON u.id=m.user_id WHERE m.user_id=$1 AND m.organization_id=$2 AND m.role='ORG_OWNER' AND u.system_role='USER')`, q.Actor.UserID, q.Actor.OrganizationID).Scan(&owner); err != nil {
		return nil, err
	}
	if !owner {
		return nil, service.ErrForbidden
	}
	rows, err := r.pool.Query(ctx, `SELECT `+columns+joins+` WHERE `+scope+`
 AND ($3='' OR strpos(lower(d.id),lower($3))>0 OR strpos(lower(v.title),lower($3))>0
   OR strpos(lower(CASE WHEN v.customer_party_id=$2 THEN s.id ELSE c.id END),lower($3))>0
   OR strpos(lower(CASE WHEN v.customer_party_id=$2 THEN s.name ELSE c.name END),lower($3))>0)
 AND ($4='' OR (CASE WHEN d.status IN ('SETTLED','CANCELLED','REFUNDED') THEN d.status WHEN v.deadline_at<=statement_timestamp() THEN 'REVIEW_REQUIRED' ELSE d.status END)=$4)
 AND ($5='' OR ($5='customer' AND v.customer_party_id=$2) OR ($5='supplier' AND v.supplier_party_id=$2))
 AND ($6::timestamptz IS NULL OR d.created_at >= $6) AND ($7::timestamptz IS NULL OR d.created_at <= $7)
 AND ($8::timestamptz IS NULL OR (d.created_at,d.id)<($8::timestamptz,$9::text))
 ORDER BY d.created_at DESC,d.id DESC LIMIT $10`, q.Actor.UserID, q.Actor.OrganizationID, q.Filters.Search, q.Filters.Status, q.Filters.Role, q.Filters.CreatedFrom, q.Filters.CreatedTo, q.BeforeTime, q.BeforeID, q.Filters.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []service.Deal{}
	for rows.Next() {
		d, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
