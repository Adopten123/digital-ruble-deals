package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"

	corepg "github.com/Adopten123/digital-ruble-deals/internal/core/repository/postgres"
	"github.com/Adopten123/digital-ruble-deals/internal/core/security"
	deals "github.com/Adopten123/digital-ruble-deals/internal/features/deals/service"
	"github.com/Adopten123/digital-ruble-deals/internal/features/edo/service"
	"github.com/jackc/pgx/v5"
)

func amountString(n int64) string { return strconv.FormatInt(n, 10) }

type querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func operator(ctx context.Context, q querier, id string) error {
	var valid bool
	if e := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND system_role='DEMO_OPERATOR')`, id).Scan(&valid); e != nil {
		return e
	}
	if !valid {
		return service.ErrForbidden
	}
	return nil
}
func (r *Repository) Queue(ctx context.Context, c service.Command) (out deals.Result, err error) {
	err = (corepg.UnitOfWork{Pool: r.pool}).WithinTx(ctx, func(tx pgx.Tx) error {
		if e := operator(ctx, tx, c.Actor.UserID); e != nil {
			return e
		}
		scope, _ := json.Marshal([]string{"operator", c.Actor.UserID, "SEND_EDO", "/demo/edo/events", c.Key})
		if _, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, string(scope)); e != nil {
			return e
		}
		var hash string
		e := tx.QueryRow(ctx, `SELECT request_hash,status_code,response_body FROM edo_command_results WHERE user_id=$1 AND key=$2`, c.Actor.UserID, c.Key).Scan(&hash, &out.Status, &out.Body)
		if e == nil {
			if hash != c.Hash {
				out = (deals.Failure{Status: 409, Code: "IDEMPOTENCY_CONFLICT", Message: "Ключ уже использован с другим телом"}).Result(c.Actor.RequestID)
			}
			return nil
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		persist := func(eventID string) error {
			id, e := security.ID()
			if e != nil {
				return e
			}
			payload, _ := json.Marshal(map[string]any{"operation": "SEND_EDO", "deal_id": c.Input.DealID, "template": c.Input.Template, "event_id": eventID, "result": json.RawMessage(out.Body)})
			if _, e = tx.Exec(ctx, `INSERT INTO audit_events(id,type,actor_user_id,request_id,payload) VALUES($1,'EDO_DELIVERY_REQUESTED',$2,$3,$4)`, id, c.Actor.UserID, c.Actor.RequestID, payload); e != nil {
				return e
			}
			_, e = tx.Exec(ctx, `INSERT INTO edo_command_results(user_id,key,request_hash,status_code,response_body) VALUES($1,$2,$3,$4,$5)`, c.Actor.UserID, c.Key, c.Hash, out.Status, out.Body)
			return e
		}
		reject := func(status int, code, message string) error {
			out = (deals.Failure{Status: status, Code: code, Message: message}).Result(c.Actor.RequestID)
			return persist("")
		}
		if c.Invalid != nil {
			return reject(c.Invalid.Status, c.Invalid.Code, c.Invalid.Message)
		}
		var locked string
		e = tx.QueryRow(ctx, `SELECT id FROM deals WHERE id=$1 FOR UPDATE`, c.Input.DealID).Scan(&locked)
		if errors.Is(e, pgx.ErrNoRows) {
			return reject(404, "RESOURCE_NOT_FOUND", "Сделка не найдена")
		}
		if e != nil {
			return e
		}
		var raw []byte
		var event service.Event
		if c.Input.Template == "replay" || c.Input.Template == "same_id_different_body" {
			e = tx.QueryRow(ctx, `SELECT raw_body FROM edo_outbox WHERE event_id=$1 AND deal_id=$2`, *c.Input.ReplayEventID, c.Input.DealID).Scan(&raw)
			if errors.Is(e, pgx.ErrNoRows) {
				e = tx.QueryRow(ctx, `SELECT raw_body FROM edo_deliveries WHERE event_id=$1 AND deal_id=$2 ORDER BY created_at,id LIMIT 1`, *c.Input.ReplayEventID, c.Input.DealID).Scan(&raw)
			}
			if errors.Is(e, pgx.ErrNoRows) {
				return reject(404, "RESOURCE_NOT_FOUND", "Исходное событие этой сделки не найдено")
			}
			if e != nil {
				return e
			}
			event, e = service.Parse(raw)
			if e != nil {
				return e
			}
			if c.Input.Template == "same_id_different_body" {
				event.AmountKopecks = differentAmount(event.AmountKopecks)
				raw, e = json.Marshal(event)
				if e != nil {
					return e
				}
			}
		} else {
			e = tx.QueryRow(ctx, `SELECT d.id,d.deal_version,v.customer_party_id,v.supplier_party_id,v.amount_kopecks::text,v.currency,COALESCE(d.current_document_id,''),COALESCE(d.current_document_version,0),COALESCE(dv.sha256,''),clock_timestamp() FROM deals d JOIN deal_versions v ON v.deal_id=d.id AND v.deal_version=d.deal_version LEFT JOIN document_versions dv ON dv.document_id=d.current_document_id AND dv.document_version=d.current_document_version WHERE d.id=$1`, c.Input.DealID).Scan(&event.DealID, &event.DealVersion, &event.CustomerPartyID, &event.SupplierPartyID, &event.AmountKopecks, &event.Currency, &event.DocumentID, &event.DocumentVersion, &event.DocumentSHA256, &event.OccurredAt)
			if e != nil {
				return e
			}
			if event.DocumentID == "" {
				return reject(422, "DOCUMENT_REQUIRED", "Для события нужен текущий PDF")
			}
			event.OccurredAt = event.OccurredAt.UTC()
			event.SchemaVersion = 1
			event.Source = service.Source
			event.EventType = "document.accepted_by_both"
			event.EventID, e = security.ID()
			if e != nil {
				return e
			}
			rows, e := tx.Query(ctx, `SELECT id,organization_id FROM document_signatures WHERE deal_id=$1 AND deal_version=$2 AND document_id=$3 AND document_version=$4 AND document_sha256=$5`, event.DealID, event.DealVersion, event.DocumentID, event.DocumentVersion, event.DocumentSHA256)
			if e != nil {
				return e
			}
			for rows.Next() {
				var id, org string
				if e = rows.Scan(&id, &org); e != nil {
					rows.Close()
					return e
				}
				if org == event.CustomerPartyID {
					event.CustomerSignatureID = &id
					event.SignedByCustomer = true
				}
				if org == event.SupplierPartyID {
					copyID := id
					event.SupplierSignatureID = &copyID
					event.SignedBySupplier = true
				}
			}
			e = rows.Err()
			rows.Close()
			if e != nil {
				return e
			}
			switch c.Input.Template {
			case "single_signature":
				if event.CustomerSignatureID != nil {
					event.SupplierSignatureID = nil
					event.SignedBySupplier = false
				}
			case "wrong_amount":
				event.AmountKopecks = differentAmount(event.AmountKopecks)
			case "wrong_parties":
				event.CustomerPartyID, event.SupplierPartyID = event.SupplierPartyID, event.CustomerPartyID
			case "wrong_document":
				event.DocumentID = "wrong-document-" + event.EventID
			case "wrong_document_version":
				event.DocumentVersion++
			case "wrong_document_hash":
				if event.DocumentSHA256 == strings.Repeat("0", 64) {
					event.DocumentSHA256 = strings.Repeat("1", 64)
				} else {
					event.DocumentSHA256 = strings.Repeat("0", 64)
				}
			case "wrong_deal_version":
				event.DealVersion++
			}
			raw, e = json.Marshal(event)
			if e != nil {
				return e
			}
		}
		job, e := security.ID()
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO edo_deliveries(id,event_id,deal_id,template,raw_body) VALUES($1,$2,$3,$4,$5)`, job, event.EventID, c.Input.DealID, c.Input.Template, raw); e != nil {
			return e
		}
		out.Status = 202
		out.Body, e = json.Marshal(map[string]string{"job_id": job, "event_id": event.EventID})
		if e != nil {
			return e
		}
		return persist(event.EventID)
	})
	if err != nil {
		return deals.Result{}, err
	}
	return out, nil
}
func differentAmount(raw string) string {
	n, _ := strconv.ParseInt(raw, 10, 64)
	if n == math.MaxInt64 {
		return amountString(n - 1)
	}
	return amountString(n + 1)
}

const deliveryColumns = `id,event_id,deal_id,template,CASE WHEN completed_at IS NULL THEN 'PENDING' ELSE 'DELIVERED' END,attempts,http_status,response_body,created_at,completed_at,raw_body,COALESCE(lease_id,'')`

type scanner interface{ Scan(...any) error }

func scanDelivery(row scanner) (out service.Delivery, err error) {
	var result []byte
	err = row.Scan(&out.JobID, &out.EventID, &out.DealID, &out.Template, &out.Status, &out.Attempts, &out.HTTPStatus, &result, &out.CreatedAt, &out.CompletedAt, &out.Raw, &out.LeaseID)
	if len(result) > 0 {
		out.Result = json.RawMessage(result)
		var v struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if json.Unmarshal(result, &v) == nil && v.Error.Code != "" {
			out.ErrorCode = &v.Error.Code
		}
	}
	return out, err
}
func (r *Repository) Get(ctx context.Context, a service.Actor, eventID string) (service.Delivery, error) {
	if e := operator(ctx, r.pool, a.UserID); e != nil {
		return service.Delivery{}, e
	}
	out, e := scanDelivery(r.pool.QueryRow(ctx, `SELECT `+deliveryColumns+` FROM edo_deliveries WHERE event_id=$1 ORDER BY created_at DESC,id DESC LIMIT 1`, eventID))
	if errors.Is(e, pgx.ErrNoRows) {
		return service.Delivery{}, service.ErrNotFound
	}
	return out, e
}
func (r *Repository) Claim(ctx context.Context) (out *service.Delivery, err error) {
	err = (corepg.UnitOfWork{Pool: r.pool}).WithinTx(ctx, func(tx pgx.Tx) error {
		// The immutable basis is discoverable even when the process died before
		// it managed to create a mutable delivery record.
		if _, e := tx.Exec(ctx, `INSERT INTO edo_deliveries(id,event_id,deal_id,template,raw_body,automatic_basis,created_at) SELECT o.id,e.event_id,e.deal_id,'valid',e.raw_body,e.event_id,o.created_at FROM edo_outbox e JOIN outbox o ON o.event_id=e.event_id ORDER BY e.event_id ON CONFLICT DO NOTHING`); e != nil {
			return e
		}
		job, e := scanDelivery(tx.QueryRow(ctx, `SELECT `+deliveryColumns+` FROM edo_deliveries WHERE completed_at IS NULL AND next_attempt_at<=clock_timestamp() AND (lease_until IS NULL OR lease_until<=clock_timestamp()) ORDER BY created_at,id FOR UPDATE SKIP LOCKED LIMIT 1`))
		if errors.Is(e, pgx.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		job.LeaseID, e = security.ID()
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `UPDATE edo_deliveries SET attempts=attempts+1,lease_id=$2,lease_until=clock_timestamp()+interval '15 seconds' WHERE id=$1`, job.JobID, job.LeaseID); e != nil {
			return e
		}
		out = &job
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
func (r *Repository) Complete(ctx context.Context, job service.Delivery, status int, body []byte, terminal bool) error {
	if terminal {
		_, e := r.pool.Exec(ctx, `UPDATE edo_deliveries SET http_status=$3,response_body=$4,completed_at=clock_timestamp(),lease_id=NULL,lease_until=NULL WHERE id=$1 AND lease_id=$2 AND completed_at IS NULL`, job.JobID, job.LeaseID, status, body)
		return e
	}
	_, e := r.pool.Exec(ctx, `UPDATE edo_deliveries SET lease_id=NULL,lease_until=NULL,next_attempt_at=clock_timestamp()+interval '1 second' WHERE id=$1 AND lease_id=$2 AND completed_at IS NULL`, job.JobID, job.LeaseID)
	return e
}
