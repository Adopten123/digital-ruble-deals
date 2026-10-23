package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"time"

	"github.com/Adopten123/digital-ruble-deals/internal/core/documentfile"
	corepg "github.com/Adopten123/digital-ruble-deals/internal/core/repository/postgres"
	"github.com/Adopten123/digital-ruble-deals/internal/core/security"
	"github.com/Adopten123/digital-ruble-deals/internal/features/deals/service"
	"github.com/Adopten123/digital-ruble-deals/internal/features/settlement/ledger"
	"github.com/jackc/pgx/v5"
)

func (r *Repository) Mutate(ctx context.Context, cmd service.Mutation) (out service.Result, err error) {
	err = (corepg.UnitOfWork{Pool: r.pool}).WithinTx(ctx, func(tx pgx.Tx) error {
		// Scope the resource before looking up an old outcome: former participants cannot replay it.
		var allowed bool
		if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 `+joins+` WHERE `+scope+` AND d.id=$3)`, cmd.Actor.UserID, cmd.Actor.OrganizationID, cmd.ID).Scan(&allowed); e != nil {
			return e
		}
		if !allowed {
			return service.ErrNotFound
		}
		resource := "/deals/" + cmd.ID
		lock, _ := json.Marshal([]string{cmd.Actor.OrganizationID, cmd.Operation, resource, cmd.Key})
		if _, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, string(lock)); e != nil {
			return e
		}
		// Lock only the aggregate row, then read its version and scope in a fresh statement.
		// A joined SELECT FOR UPDATE can otherwise retain a pre-wait version snapshot.
		var lockedID string
		if e := tx.QueryRow(ctx, `SELECT id FROM deals WHERE id=$1 FOR UPDATE`, cmd.ID).Scan(&lockedID); errors.Is(e, pgx.ErrNoRows) {
			return service.ErrNotFound
		} else if e != nil {
			return e
		}
		d, e := scan(tx.QueryRow(ctx, `SELECT `+columns+joins+` WHERE `+scope+` AND d.id=$3`, cmd.Actor.UserID, cmd.Actor.OrganizationID, cmd.ID))
		if errors.Is(e, pgx.ErrNoRows) {
			return service.ErrNotFound
		}
		if e != nil {
			return e
		}
		var oldHash string
		e = tx.QueryRow(ctx, `SELECT request_hash,status_code,response_body FROM idempotency_keys WHERE organization_id=$1 AND operation=$2 AND resource=$3 AND key=$4`, cmd.Actor.OrganizationID, cmd.Operation, resource, cmd.Key).Scan(&oldHash, &out.Status, &out.Body)
		if e == nil {
			if oldHash != cmd.Hash {
				out = service.Failure{Status: 409, Code: "IDEMPOTENCY_CONFLICT", Message: "Этот ключ уже использован с другим телом запроса"}.Result(cmd.Actor.RequestID)
			}
			return nil
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		var reserved bool
		var rawStatus string
		if e = tx.QueryRow(ctx, `SELECT first_reserved_at IS NOT NULL,status FROM deals WHERE id=$1`, d.ID).Scan(&reserved, &rawStatus); e != nil {
			return e
		}
		persist := func(success bool, event string, now time.Time) error {
			payload := out.Body
			if !success {
				payload, _ = json.Marshal(map[string]any{"operation": cmd.Operation, "result": json.RawMessage(out.Body)})
			} else if cmd.Operation == "REWORK_DEAL" {
				payload, _ = json.Marshal(map[string]any{"deal": json.RawMessage(out.Body), "reason": cmd.Reason})
			}
			eventID, e := security.ID()
			if e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `INSERT INTO audit_events(id,deal_id,deal_version,type,actor_user_id,actor_organization_id,request_id,payload,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, eventID, d.ID, d.DealVersion, event, cmd.Actor.UserID, cmd.Actor.OrganizationID, cmd.Actor.RequestID, payload, now); e != nil {
				return e
			}
			if success {
				jobID, e := security.ID()
				if e != nil {
					return e
				}
				if _, e = tx.Exec(ctx, `INSERT INTO outbox(id,event_id,type,deal_id,payload,created_at) VALUES($1,$2,$3,$4,$5,$6)`, jobID, eventID, event, d.ID, payload, now); e != nil {
					return e
				}
			}
			_, e = tx.Exec(ctx, `INSERT INTO idempotency_keys(organization_id,operation,resource,key,request_hash,status_code,response_body) VALUES($1,$2,$3,$4,$5,$6,$7)`, cmd.Actor.OrganizationID, cmd.Operation, resource, cmd.Key, cmd.Hash, out.Status, out.Body)
			return e
		}
		var now time.Time
		clock := func() error { e := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); now = now.UTC(); return e }
		if e = clock(); e != nil {
			return e
		}
		reject := func(status int, code, message string) error {
			out = service.Failure{Status: status, Code: code, Message: message}.Result(cmd.Actor.RequestID)
			return persist(false, cmd.Operation+"_REJECTED", now)
		}
		customerOnly := cmd.Operation == "PATCH_DEAL" || cmd.Operation == "UPLOAD_DOCUMENT" || cmd.Operation == "GENERATE_DOCUMENT" || cmd.Operation == "RESERVE_DEAL"
		if customerOnly && cmd.Actor.OrganizationID != d.CustomerPartyID {
			return reject(403, "FORBIDDEN", "Это действие доступно заказчику")
		}
		if cmd.Invalid != nil {
			out = cmd.Invalid.Result(cmd.Actor.RequestID)
			return persist(false, cmd.Operation+"_REJECTED", now)
		}
		if cmd.ExpectedVersion != d.DealVersion {
			return reject(409, "VERSION_CONFLICT", "Условия сделки изменились. Обновите карточку")
		}
		if !d.DeadlineAt.After(now) {
			return reject(422, "DEADLINE_PASSED", "Срок сделки уже наступил")
		}
		if reserved || (rawStatus != "DRAFT" && rawStatus != "AGREED") {
			return reject(409, "INVALID_STATE", "Состояние сделки не допускает это действие")
		}
		changed := false
		event := cmd.Operation
		switch cmd.Operation {
		case "RESERVE_DEAL":
			if rawStatus != "AGREED" {
				return reject(409, "INVALID_STATE", "Резервирование доступно после двух согласий")
			}
			if d.Document == nil {
				return reject(409, "DOCUMENT_REQUIRED", "Нет актуального PDF")
			}
			if d.Document.DealID != d.ID || d.Document.DealVersion != d.DealVersion {
				return reject(409, "DOCUMENT_MISMATCH", "PDF не соответствует условиям")
			}
			consent := map[string]bool{}
			for _, a := range d.Agreements {
				if a.DealVersion == d.DealVersion && a.DocumentID == d.Document.ID && a.DocumentVersion == d.Document.DocumentVersion && a.DocumentSHA256 == d.Document.SHA256 {
					consent[a.OrganisationID] = true
				}
			}
			if !consent[d.CustomerPartyID] || !consent[d.SupplierPartyID] {
				return reject(422, "CONSENTS_MISSING", "Нужны два согласия текущих условий и PDF")
			}
			var storage, accountID string
			if e = tx.QueryRow(ctx, `SELECT storage_key FROM document_versions WHERE document_id=$1 AND document_version=$2`, d.Document.ID, d.Document.DocumentVersion).Scan(&storage); e != nil {
				return e
			}
			if cmd.VerifyDocument == nil {
				return errors.New("document verifier unavailable")
			}
			if e = cmd.VerifyDocument(*d.Document, storage); e != nil {
				return e
			}
			if e = tx.QueryRow(ctx, `SELECT id FROM accounts WHERE organization_id=$1`, d.CustomerPartyID).Scan(&accountID); e != nil {
				return e
			}
			accounts, e := ledger.Lock(ctx, tx, accountID)
			if e != nil {
				return e
			}
			account := accounts[accountID]
			var existing string
			// Reserves are immutable at this stage; the held aggregate lock and
			// unique deal_id serialize insertion without granting UPDATE facts.
			e = tx.QueryRow(ctx, `SELECT id FROM reserves WHERE deal_id=$1`, d.ID).Scan(&existing)
			if e == nil {
				return reject(409, "INVALID_STATE", "Средства уже зарезервированы")
			}
			if !errors.Is(e, pgx.ErrNoRows) {
				return e
			}
			if e = clock(); e != nil {
				return e
			}
			if !d.DeadlineAt.After(now) {
				return reject(422, "DEADLINE_PASSED", "Срок сделки уже наступил")
			}
			amount, _ := strconv.ParseInt(d.AmountKopecks, 10, 64)
			e = ledger.CanReserve(account, amount)
			if errors.Is(e, ledger.ErrInsufficient) {
				return reject(422, "INSUFFICIENT_AVAILABLE_FUNDS", "Недостаточно доступных тестовых средств")
			}
			if e != nil {
				return e
			}
			id, e := security.ID()
			if e != nil {
				return e
			}
			r := service.Reserve{ID: id, DealID: d.ID, DealVersion: d.DealVersion, AccountID: accountID, AmountKopecks: d.AmountKopecks, Currency: d.Currency, Status: "ACTIVE", CreatedAt: now}
			if _, e = tx.Exec(ctx, `INSERT INTO reserves(id,deal_id,deal_version,account_id,customer_party_id,supplier_party_id,document_id,document_version,document_sha256,amount_kopecks,currency,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, id, d.ID, d.DealVersion, accountID, d.CustomerPartyID, d.SupplierPartyID, d.Document.ID, d.Document.DocumentVersion, d.Document.SHA256, amount, d.Currency, now); e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `UPDATE accounts SET reserved_kopecks=reserved_kopecks+$2 WHERE id=$1`, accountID, amount); e != nil {
				return e
			}
			d.Reserve = &r
			d.Status = "FUNDED"
			event = "DEAL_RESERVED"
		case "PATCH_DEAL":
			if rawStatus != "DRAFT" {
				return reject(409, "INVALID_STATE", "Редактирование доступно только в черновике")
			}
			p := cmd.Patch
			before, _ := json.Marshal([]any{d.SupplierPartyID, d.Title, d.Description, d.AmountKopecks, d.Currency, d.DeadlineAt})
			if p.SupplierPartyID != nil {
				d.SupplierPartyID = *p.SupplierPartyID
			}
			if p.Title != nil {
				d.Title = *p.Title
			}
			if p.Description != nil {
				d.Description = *p.Description
			}
			if p.AmountKopecks != nil {
				d.AmountKopecks = *p.AmountKopecks
			}
			if p.Currency != nil {
				d.Currency = *p.Currency
			}
			if p.DeadlineAt != nil {
				d.DeadlineAt, _ = time.Parse(time.RFC3339Nano, *p.DeadlineAt)
			}
			if d.SupplierPartyID == d.CustomerPartyID {
				return reject(400, "INVALID_REQUEST", "Стороны сделки должны различаться")
			}
			after, _ := json.Marshal([]any{d.SupplierPartyID, d.Title, d.Description, d.AmountKopecks, d.Currency, d.DeadlineAt})
			changed = string(before) != string(after)
			if !d.DeadlineAt.After(now) {
				return reject(422, "DEADLINE_PASSED", "Срок сделки уже наступил")
			}
			e = tx.QueryRow(ctx, `SELECT id,name,kind FROM organizations WHERE id=$1`, d.SupplierPartyID).Scan(&d.Supplier.ID, &d.Supplier.Name, &d.Supplier.Kind)
			if errors.Is(e, pgx.ErrNoRows) {
				return reject(404, "RESOURCE_NOT_FOUND", "Контрагент не найден")
			}
			if e != nil {
				return e
			}
		case "REWORK_DEAL":
			if rawStatus != "AGREED" {
				return reject(409, "INVALID_STATE", "Возврат на доработку доступен после двух согласий")
			}
			changed = true
		case "UPLOAD_DOCUMENT", "GENERATE_DOCUMENT":
			changed = true
		case "AGREE_DEAL":
			if d.Document == nil {
				return reject(409, "DOCUMENT_REQUIRED", "Сначала загрузите или сформируйте актуальный PDF")
			}
			a := cmd.Agreement
			if a.DocumentID != d.Document.ID || a.DocumentVersion != d.Document.DocumentVersion || a.DocumentSHA256 != d.Document.SHA256 {
				return reject(409, "DOCUMENT_MISMATCH", "Документ изменился. Откройте актуальный PDF")
			}
			found := false
			for _, a := range d.Agreements {
				if a.OrganisationID == cmd.Actor.OrganizationID {
					found = true
				}
			}
			if !found {
				id, e := security.ID()
				if e != nil {
					return e
				}
				a := service.Agreement{ID: id, OrganisationID: cmd.Actor.OrganizationID, UserID: cmd.Actor.UserID, DealVersion: d.DealVersion, DocumentID: d.Document.ID, DocumentVersion: d.Document.DocumentVersion, DocumentSHA256: d.Document.SHA256, CreatedAt: now}
				if _, e = tx.Exec(ctx, `INSERT INTO deal_agreements(id,deal_id,deal_version,organization_id,user_id,document_id,document_version,document_sha256,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id, d.ID, d.DealVersion, a.OrganisationID, a.UserID, a.DocumentID, a.DocumentVersion, a.DocumentSHA256, now); e != nil {
					return e
				}
				d.Agreements = append(d.Agreements, a)
			}
			if len(d.Agreements) == 2 {
				d.Status = "AGREED"
			} else {
				d.Status = "DRAFT"
			}
		default:
			return errors.New("unsupported deal command")
		}
		if changed {
			if d.DealVersion == math.MaxInt64 {
				return reject(409, "VERSION_CONFLICT", "Исчерпан диапазон версий сделки")
			}
			d.DealVersion++
			d.Status = "DRAFT"
			d.Document = nil
			d.Agreements = []service.Agreement{}
			var prepared service.PreparedDocument
			if cmd.Prepare != nil {
				prepared, e = cmd.Prepare(d, d.DealVersion)
				if errors.Is(e, documentfile.ErrInvalid) || errors.Is(e, documentfile.ErrTooLarge) {
					d.DealVersion--
					return reject(422, "INVALID_DOCUMENT", "Допустим безопасный PDF размером до 5 MiB")
				}
				if e != nil {
					return e
				}
				// PDF validation/generation/storage may take time; the commit context cannot outlive the deadline.
				if e = clock(); e != nil {
					return e
				}
				if !d.DeadlineAt.After(now) {
					d.DealVersion--
					return reject(422, "DEADLINE_PASSED", "Срок сделки уже наступил")
				}
			}
			amount, _ := strconv.ParseInt(d.AmountKopecks, 10, 64)
			if _, e = tx.Exec(ctx, `INSERT INTO deal_versions(deal_id,deal_version,customer_party_id,supplier_party_id,title,description,amount_kopecks,currency,deadline_at,created_by,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, d.ID, d.DealVersion, d.CustomerPartyID, d.SupplierPartyID, d.Title, d.Description, amount, d.Currency, d.DeadlineAt, cmd.Actor.UserID, now); e != nil {
				return e
			}
			if cmd.Prepare != nil {
				var docID string
				var version int64
				e = tx.QueryRow(ctx, `SELECT id FROM documents WHERE deal_id=$1`, d.ID).Scan(&docID)
				if errors.Is(e, pgx.ErrNoRows) {
					docID, e = security.ID()
					if e != nil {
						return e
					}
					if _, e = tx.Exec(ctx, `INSERT INTO documents(id,deal_id) VALUES($1,$2)`, docID, d.ID); e != nil {
						return e
					}
				} else if e != nil {
					return e
				}
				if e = tx.QueryRow(ctx, `SELECT COALESCE(MAX(document_version),0)+1 FROM document_versions WHERE document_id=$1`, docID).Scan(&version); e != nil {
					return e
				}
				doc := service.Document{ID: docID, DealID: d.ID, DealVersion: d.DealVersion, DocumentVersion: version, SHA256: prepared.SHA256, FileName: prepared.FileName, SizeBytes: prepared.Size, ContentType: "application/pdf", CreatedAt: now}
				if _, e = tx.Exec(ctx, `INSERT INTO document_versions(document_id,document_version,deal_id,deal_version,sha256,storage_key,file_name,size_bytes,content_type,created_by,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'application/pdf',$9,$10)`, doc.ID, doc.DocumentVersion, doc.DealID, doc.DealVersion, doc.SHA256, prepared.Key, doc.FileName, doc.SizeBytes, cmd.Actor.UserID, now); e != nil {
					return e
				}
				d.Document = &doc
			}
		}
		d.UpdatedAt = now
		var docID *string
		var docVersion *int64
		if d.Document != nil {
			docID = &d.Document.ID
			docVersion = &d.Document.DocumentVersion
		}
		if _, e = tx.Exec(ctx, `UPDATE deals SET deal_version=$2,row_version=row_version+1,status=$3,current_document_id=$4,current_document_version=$5,updated_at=$6,first_reserved_at=CASE WHEN $7 THEN $6 ELSE first_reserved_at END WHERE id=$1`, d.ID, d.DealVersion, d.Status, docID, docVersion, now, cmd.Operation == "RESERVE_DEAL"); e != nil {
			return e
		}
		if cmd.Operation == "RESERVE_DEAL" {
			out.Status = 201
			out.Body, e = json.Marshal(map[string]any{"reserve": d.Reserve, "deal": d})
		} else if cmd.Prepare != nil {
			out.Status = 201
			out.Body, e = json.Marshal(map[string]any{"deal": d, "document": d.Document})
		} else {
			out.Status = 200
			out.Body, e = json.Marshal(d)
		}
		if e != nil {
			return e
		}
		if cmd.Operation == "REWORK_DEAL" {
			event = "DEAL_REWORKED"
		}
		return persist(true, event, now)
	})
	if err != nil {
		return service.Result{}, err
	}
	return out, nil
}

func (r *Repository) GetDocument(ctx context.Context, a service.Actor, id string, version *int64) (out service.StoredDocument, err error) {
	err = r.pool.QueryRow(ctx, `SELECT dv.document_id,dv.deal_id,dv.deal_version,dv.document_version,dv.sha256,dv.file_name,dv.size_bytes,dv.content_type,dv.created_at,dv.storage_key
 FROM document_versions dv JOIN documents doc ON doc.id=dv.document_id JOIN deals d ON d.id=doc.deal_id
 JOIN deal_versions v ON v.deal_id=d.id AND v.deal_version=d.deal_version
 JOIN deal_versions historic ON historic.deal_id=dv.deal_id AND historic.deal_version=dv.deal_version
 WHERE `+scope+` AND ($2=historic.customer_party_id OR $2=historic.supplier_party_id)
 AND dv.document_id=$3 AND dv.document_version=COALESCE($4::bigint,(SELECT MAX(current.document_version) FROM document_versions current WHERE current.document_id=dv.document_id))
 ORDER BY dv.document_version DESC LIMIT 1`, a.UserID, a.OrganizationID, id, version).Scan(&out.Metadata.ID, &out.Metadata.DealID, &out.Metadata.DealVersion, &out.Metadata.DocumentVersion, &out.Metadata.SHA256, &out.Metadata.FileName, &out.Metadata.SizeBytes, &out.Metadata.ContentType, &out.Metadata.CreatedAt, &out.Key)
	if errors.Is(err, pgx.ErrNoRows) {
		return service.StoredDocument{}, service.ErrNotFound
	}
	out.Metadata.CreatedAt = out.Metadata.CreatedAt.UTC()
	return out, err
}
