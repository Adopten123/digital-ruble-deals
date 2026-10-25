package postgres

import (
	"context"
	"errors"
	"time"

	corepg "github.com/Adopten123/digital-ruble-deals/internal/core/repository/postgres"
	"github.com/Adopten123/digital-ruble-deals/internal/core/security"
	audit "github.com/Adopten123/digital-ruble-deals/internal/features/audit/service"
	"github.com/jackc/pgx/v5"
)

type job struct {
	id, kind string
	dealID   *string
	record   audit.Record
}
type recipient struct{ user, organization string }

var dealTypes = map[string]bool{"DEAL_CREATED": true, "AGREE_DEAL": true, "DEAL_REWORKED": true, "DEAL_RESERVED": true, "DOCUMENT_SIGNED": true, "DEAL_SETTLED": true, "DISPUTE_OPENED": true, "CANCELLATION_REQUESTED": true, "CANCELLATION_ACCEPTED": true, "CANCELLATION_REJECTED": true}

func (r *Repository) Deliver(ctx context.Context, limit int) (processed int, err error) {
	if limit < 1 || limit > 50 {
		return 0, errors.New("invalid notification batch limit")
	}
	err = (corepg.UnitOfWork{Pool: r.pool}).WithinTx(ctx, func(tx pgx.Tx) error {
		// The entire delivery is local to PostgreSQL: insert and acknowledgement
		// commit together. A lease/network phase would add no useful guarantee.
		rows, e := tx.Query(ctx, `SELECT o.id,o.type,o.deal_id,a.id,a.deal_id,a.deal_version,a.type,a.actor_user_id,a.actor_organization_id,a.actor_source,a.request_id,a.created_at,a.payload FROM outbox o JOIN audit_events a ON a.id=o.event_id WHERE o.delivered_at IS NULL ORDER BY o.created_at,o.id FOR UPDATE OF o SKIP LOCKED LIMIT $1`, limit)
		if e != nil {
			return e
		}
		jobs := []job{}
		for rows.Next() {
			var j job
			if e = rows.Scan(&j.id, &j.kind, &j.dealID, &j.record.ID, &j.record.DealID, &j.record.DealVersion, &j.record.Type, &j.record.ActorUserID, &j.record.ActorOrganizationID, &j.record.ActorSource, &j.record.RequestID, &j.record.CreatedAt, &j.record.Payload); e != nil {
				rows.Close()
				return e
			}
			jobs = append(jobs, j)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		for _, j := range jobs {
			if j.kind != j.record.Type || (j.dealID == nil) != (j.record.DealID == nil) || (j.dealID != nil && *j.dealID != *j.record.DealID) {
				return errors.New("outbox audit metadata mismatch")
			}
			people, key, e := recipients(ctx, tx, j.record)
			if e != nil {
				return e
			}
			for _, person := range people {
				id, e := security.ID()
				if e != nil {
					return e
				}
				if _, e = tx.Exec(ctx, `INSERT INTO notifications(id,event_id,recipient_user_id,recipient_organization_id,semantic_key) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, id, j.record.ID, person.user, person.organization, key); e != nil {
					return e
				}
			}
			// This is acknowledgement of application-notification processing, not EDO
			// receipt or money movement. The specialized EDO queue remains independent.
			if _, e = tx.Exec(ctx, `UPDATE outbox SET delivered_at=clock_timestamp() WHERE id=$1 AND delivered_at IS NULL`, j.id); e != nil {
				return e
			}
		}
		processed = len(jobs)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return processed, nil
}
func recipients(ctx context.Context, tx pgx.Tx, event audit.Record) ([]recipient, string, error) {
	key := "event:" + event.ID
	people := []recipient{}
	var rows pgx.Rows
	var err error
	if dealTypes[event.Type] {
		if event.DealID == nil || event.DealVersion == nil {
			return people, key, nil
		}
		if event.Type == "AGREE_DEAL" {
			if event.ActorOrganizationID == nil {
				return people, key, nil
			}
			var agreementID, userID string
			var createdAt time.Time
			err = tx.QueryRow(ctx, `SELECT id,user_id,created_at FROM deal_agreements WHERE deal_id=$1 AND deal_version=$2 AND organization_id=$3`, *event.DealID, *event.DealVersion, *event.ActorOrganizationID).Scan(&agreementID, &userID, &createdAt)
			if errors.Is(err, pgx.ErrNoRows) {
				return people, key, nil
			}
			if err != nil {
				return nil, "", err
			}
			// A repeat command can have another actor or a later snapshot. If its
			// job wins SKIP LOCKED, it must not steal attribution from the original fact.
			if event.ActorUserID == nil || *event.ActorUserID != userID || !event.CreatedAt.Equal(createdAt) {
				return people, key, nil
			}
			key = "agreement:" + agreementID
		}
		rows, err = tx.Query(ctx, `SELECT DISTINCT m.user_id,m.organization_id FROM deal_versions h JOIN deals d ON d.id=h.deal_id JOIN deal_versions c ON c.deal_id=d.id AND c.deal_version=d.deal_version JOIN memberships m ON (m.organization_id=h.customer_party_id OR m.organization_id=h.supplier_party_id) AND (m.organization_id=c.customer_party_id OR m.organization_id=c.supplier_party_id) JOIN users u ON u.id=m.user_id WHERE h.deal_id=$1 AND h.deal_version=$2 AND m.role='ORG_OWNER' AND u.system_role='USER' ORDER BY m.user_id,m.organization_id`, *event.DealID, *event.DealVersion)
	} else if event.Type == "DEMO_CREDITED" || event.Type == "DEMO_DEBITED" {
		// The saved command carries only an account reference for addressing. Do not
		// publish the private balance snapshot or infer amount from today's account.
		accountID := audit.Project(event).Details.AccountID
		if event.DealID != nil || accountID == "" {
			return people, key, nil
		}
		rows, err = tx.Query(ctx, `SELECT m.user_id,m.organization_id FROM accounts a JOIN memberships m ON m.organization_id=a.organization_id JOIN users u ON u.id=m.user_id WHERE a.id=$1 AND m.role='ORG_OWNER' AND u.system_role='USER' ORDER BY m.user_id,m.organization_id`, accountID)
	} else {
		return people, key, nil
	}
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	for rows.Next() {
		var p recipient
		if err = rows.Scan(&p.user, &p.organization); err != nil {
			return nil, "", err
		}
		people = append(people, p)
	}
	return people, key, rows.Err()
}
