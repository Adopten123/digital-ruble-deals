package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	corepg "github.com/Adopten123/digital-ruble-deals/internal/core/repository/postgres"
	"github.com/Adopten123/digital-ruble-deals/internal/core/security"
	"github.com/Adopten123/digital-ruble-deals/internal/features/deals/service"
	"github.com/Adopten123/digital-ruble-deals/internal/features/settlement/ledger"
	"github.com/jackc/pgx/v5"
)

func (r *Repository) Block(ctx context.Context, cmd service.BlockCommand) (out service.Result, err error) {
	err = (corepg.UnitOfWork{Pool: r.pool}).WithinTx(ctx, func(tx pgx.Tx) error {
		var allowed bool
		if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 `+joins+` WHERE `+scope+` AND d.id=$3)`, cmd.Actor.UserID, cmd.Actor.OrganizationID, cmd.ID).Scan(&allowed); e != nil {
			return e
		}
		if !allowed {
			return service.ErrNotFound
		}
		resource := "/deals/" + cmd.ID
		switch cmd.Operation {
		case "OPEN_DISPUTE":
			resource += "/disputes"
		case "REQUEST_CANCELLATION":
			resource += "/cancellation-requests"
		case "ACCEPT_CANCELLATION":
			resource += "/cancellation-requests/" + cmd.RequestID + "/accept"
		case "REJECT_CANCELLATION":
			resource += "/cancellation-requests/" + cmd.RequestID + "/reject"
		default:
			return errors.New("unsupported blocking command")
		}
		lock, _ := json.Marshal([]string{cmd.Actor.OrganizationID, cmd.Operation, resource, cmd.Key})
		if _, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, string(lock)); e != nil {
			return e
		}
		var lockedID string
		if e := tx.QueryRow(ctx, `SELECT id FROM deals WHERE id=$1 FOR UPDATE`, cmd.ID).Scan(&lockedID); e != nil {
			return e
		}
		read := func() (service.Deal, error) {
			return scan(tx.QueryRow(ctx, `SELECT `+columns+joins+` WHERE `+scope+` AND d.id=$3`, cmd.Actor.UserID, cmd.Actor.OrganizationID, cmd.ID))
		}
		d, e := read()
		if errors.Is(e, pgx.ErrNoRows) {
			return service.ErrNotFound
		}
		if e != nil {
			return e
		}
		var previousHash string
		e = tx.QueryRow(ctx, `SELECT request_hash,status_code,response_body FROM idempotency_keys WHERE organization_id=$1 AND operation=$2 AND resource=$3 AND key=$4`, cmd.Actor.OrganizationID, cmd.Operation, resource, cmd.Key).Scan(&previousHash, &out.Status, &out.Body)
		if e == nil {
			if previousHash != cmd.Hash {
				out = service.Failure{Status: 409, Code: "IDEMPOTENCY_CONFLICT", Message: "Этот ключ уже использован с другим телом запроса"}.Result(cmd.Actor.RequestID)
			}
			return nil
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		persist := func() error {
			_, e := tx.Exec(ctx, `INSERT INTO idempotency_keys(organization_id,operation,resource,key,request_hash,status_code,response_body) VALUES($1,$2,$3,$4,$5,$6,$7)`, cmd.Actor.OrganizationID, cmd.Operation, resource, cmd.Key, cmd.Hash, out.Status, out.Body)
			return e
		}
		var now time.Time
		clock := func() error { e := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); now = now.UTC(); return e }
		if e = clock(); e != nil {
			return e
		}
		publish := func(event string, notify bool) error {
			eventID, e := security.ID()
			if e != nil {
				return e
			}
			payload := out.Body
			if !notify {
				payload, e = json.Marshal(map[string]any{"operation": cmd.Operation, "result": json.RawMessage(out.Body)})
				if e != nil {
					return e
				}
			}
			if _, e = tx.Exec(ctx, `INSERT INTO audit_events(id,deal_id,deal_version,type,actor_user_id,actor_organization_id,request_id,payload,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, eventID, d.ID, d.DealVersion, event, cmd.Actor.UserID, cmd.Actor.OrganizationID, cmd.Actor.RequestID, payload, now); e != nil {
				return e
			}
			if notify {
				jobID, e := security.ID()
				if e != nil {
					return e
				}
				if _, e = tx.Exec(ctx, `INSERT INTO outbox(id,event_id,type,deal_id,payload,created_at) VALUES($1,$2,$3,$4,$5,$6)`, jobID, eventID, event, d.ID, payload, now); e != nil {
					return e
				}
			}
			return persist()
		}
		reject := func(status int, code, message string) error {
			out = service.Failure{Status: status, Code: code, Message: message}.Result(cmd.Actor.RequestID)
			return publish(cmd.Operation+"_REJECTED", false)
		}
		if cmd.Invalid != nil {
			return reject(cmd.Invalid.Status, cmd.Invalid.Code, cmd.Invalid.Message)
		}
		if cmd.ExpectedVersion != d.DealVersion {
			return reject(409, "VERSION_CONFLICT", "Условия сделки изменились. Обновите карточку")
		}
		if d.Status == "SETTLED" || d.Status == "CANCELLED" || d.Status == "REFUNDED" {
			return reject(409, "INVALID_STATE", "Сделка уже завершена")
		}
		var dispute *service.Dispute
		var cancellation *service.CancellationRequest
		response := func(status int) error {
			out.Status = status
			var e error
			if dispute != nil {
				out.Body, e = json.Marshal(map[string]any{"dispute": dispute, "deal": d})
			} else {
				out.Body, e = json.Marshal(map[string]any{"cancellation_request": cancellation, "deal": d})
			}
			return e
		}
		event := ""
		terminal := ""
		switch cmd.Operation {
		case "OPEN_DISPUTE":
			if d.Dispute != nil {
				dispute = d.Dispute
				if e = response(201); e != nil {
					return e
				}
				return persist()
			}
			if d.Status != "AGREED" && d.Status != "FUNDED" && d.Status != "READY_TO_SETTLE" && d.Status != "REVIEW_REQUIRED" && d.Status != "CANCELLATION_PENDING" {
				return reject(409, "INVALID_STATE", "Спор доступен после согласования или при проверке сделки")
			}
			id, e := security.ID()
			if e != nil {
				return e
			}
			dispute = &service.Dispute{ID: id, DealID: d.ID, DealVersion: d.DealVersion, OrganisationID: cmd.Actor.OrganizationID, UserID: cmd.Actor.UserID, Reason: cmd.Reason, CreatedAt: now}
			if _, e = tx.Exec(ctx, `INSERT INTO deal_disputes(id,deal_id,deal_version,organization_id,user_id,reason,created_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, d.ID, d.DealVersion, cmd.Actor.OrganizationID, cmd.Actor.UserID, cmd.Reason, now); e != nil {
				return e
			}
			event = "DISPUTE_OPENED"
		case "REQUEST_CANCELLATION":
			if d.CancellationRequest != nil {
				if d.CancellationRequest.InitiatorOrganisationID != cmd.Actor.OrganizationID {
					return reject(409, "INVALID_STATE", "Уже есть активный запрос отмены другой стороны")
				}
				cancellation = d.CancellationRequest
				if e = response(201); e != nil {
					return e
				}
				return persist()
			}
			id, e := security.ID()
			if e != nil {
				return e
			}
			cancellation = &service.CancellationRequest{ID: id, DealID: d.ID, DealVersion: d.DealVersion, InitiatorOrganisationID: cmd.Actor.OrganizationID, InitiatorUserID: cmd.Actor.UserID, Reason: cmd.Reason, Status: "PENDING", CreatedAt: now}
			if _, e = tx.Exec(ctx, `INSERT INTO cancellation_requests(id,deal_id,deal_version,initiator_organization_id,initiator_user_id,reason,created_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, d.ID, d.DealVersion, cmd.Actor.OrganizationID, cmd.Actor.UserID, cmd.Reason, now); e != nil {
				return e
			}
			event = "CANCELLATION_REQUESTED"
		case "ACCEPT_CANCELLATION", "REJECT_CANCELLATION":
			cancellation = &service.CancellationRequest{}
			var decided bool
			e = tx.QueryRow(ctx, `SELECT r.id,r.deal_id,r.deal_version,r.initiator_organization_id,r.initiator_user_id,r.reason,r.created_at,EXISTS(SELECT 1 FROM cancellation_decisions c WHERE c.request_id=r.id) FROM cancellation_requests r WHERE r.id=$1 AND r.deal_id=$2`, cmd.RequestID, d.ID).Scan(&cancellation.ID, &cancellation.DealID, &cancellation.DealVersion, &cancellation.InitiatorOrganisationID, &cancellation.InitiatorUserID, &cancellation.Reason, &cancellation.CreatedAt, &decided)
			if errors.Is(e, pgx.ErrNoRows) {
				return reject(404, "RESOURCE_NOT_FOUND", "Запрос отмены не найден")
			}
			if e != nil {
				return e
			}
			cancellation.CreatedAt = cancellation.CreatedAt.UTC()
			if cancellation.DealVersion != d.DealVersion {
				return reject(409, "VERSION_CONFLICT", "Версия запроса отмены не соответствует сделке")
			}
			if cancellation.InitiatorOrganisationID == cmd.Actor.OrganizationID {
				return reject(409, "CANCELLATION_SELF_APPROVAL", "Решение должна принять другая организация")
			}
			if decided {
				return reject(409, "INVALID_STATE", "Запрос отмены уже закрыт")
			}
			decision := "REJECTED"
			var reason *string
			if cmd.Operation == "ACCEPT_CANCELLATION" {
				decision = "ACCEPTED"
				terminal = "CANCELLED"
				if d.Reserve != nil {
					if d.Reserve.Status != "ACTIVE" {
						return reject(409, "INVALID_STATE", "Активный резерв уже завершён")
					}
					accounts, e := ledger.Lock(ctx, tx, d.Reserve.AccountID)
					if e != nil {
						return e
					}
					var status string
					if e = tx.QueryRow(ctx, `SELECT status FROM reserves WHERE id=$1 AND deal_id=$2 FOR UPDATE`, d.Reserve.ID, d.ID).Scan(&status); e != nil {
						return e
					}
					if status != "ACTIVE" {
						return reject(409, "INVALID_STATE", "Активный резерв уже завершён")
					}
					amount, e := strconv.ParseInt(d.Reserve.AmountKopecks, 10, 64)
					if e != nil {
						return e
					}
					account := accounts[d.Reserve.AccountID]
					if account.OrganizationID != d.CustomerPartyID || amount <= 0 || account.Reserved < amount {
						return errors.New("reserve projection context mismatch")
					}
					terminal = "REFUNDED"
				}
			} else {
				reason = &cmd.Reason
			}
			// Acceptance remains allowed after the deadline. Its source is the
			// two organisations' decision, never an elapsed timer.
			if e = clock(); e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `INSERT INTO cancellation_decisions(request_id,organization_id,user_id,decision,reason,created_at) VALUES($1,$2,$3,$4,$5,$6)`, cancellation.ID, cmd.Actor.OrganizationID, cmd.Actor.UserID, decision, reason, now); e != nil {
				return e
			}
			cancellation.Status = decision
			cancellation.Decision = &service.CancellationDecision{OrganisationID: cmd.Actor.OrganizationID, UserID: cmd.Actor.UserID, Decision: decision, Reason: reason, CreatedAt: now}
			if terminal == "REFUNDED" {
				if _, e = tx.Exec(ctx, `UPDATE reserves SET status='RELEASED' WHERE id=$1`, d.Reserve.ID); e != nil {
					return e
				}
				if _, e = tx.Exec(ctx, `UPDATE accounts SET reserved_kopecks=reserved_kopecks-$2::bigint WHERE id=$1`, d.Reserve.AccountID, d.Reserve.AmountKopecks); e != nil {
					return e
				}
			}
			if decision == "ACCEPTED" {
				event = "CANCELLATION_ACCEPTED"
			} else {
				event = "CANCELLATION_REJECTED"
			}
		}
		if terminal == "" {
			// Exactly the same expression serves card, list filter and state
			// publication, while independent facts retain every blocking cause.
			if _, e = tx.Exec(ctx, `UPDATE deals d SET status=(SELECT `+statusExpression+` FROM deal_versions v WHERE v.deal_id=d.id AND v.deal_version=d.deal_version),row_version=row_version+1,updated_at=$2 WHERE d.id=$1`, d.ID, now); e != nil {
				return e
			}
		} else {
			if _, e = tx.Exec(ctx, `UPDATE deals SET status=$2,row_version=row_version+1,updated_at=$3 WHERE id=$1`, d.ID, terminal, now); e != nil {
				return e
			}
		}
		d, e = read()
		if e != nil {
			return e
		}
		status := 200
		if cmd.Operation == "OPEN_DISPUTE" || cmd.Operation == "REQUEST_CANCELLATION" {
			status = 201
		}
		if e = response(status); e != nil {
			return e
		}
		return publish(event, true)
	})
	if err != nil {
		return service.Result{}, err
	}
	return out, nil
}
