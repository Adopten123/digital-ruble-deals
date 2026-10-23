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
)

// edoEvent is the complete, immutable wire body for the later EDO dispatcher.
// Transport timestamp/HMAC and delivery state are deliberately absent here.
type edoEvent struct {
	SchemaVersion       int       `json:"schema_version"`
	EventID             string    `json:"event_id"`
	Source              string    `json:"source"`
	EventType           string    `json:"event_type"`
	DealID              string    `json:"deal_id"`
	DealVersion         int64     `json:"deal_version"`
	DocumentID          string    `json:"document_id"`
	DocumentVersion     int64     `json:"document_version"`
	DocumentSHA256      string    `json:"document_sha256"`
	CustomerPartyID     string    `json:"customer_party_id"`
	SupplierPartyID     string    `json:"supplier_party_id"`
	AmountKopecks       string    `json:"amount_kopecks"`
	Currency            string    `json:"currency"`
	SignedByCustomer    bool      `json:"signed_by_customer"`
	SignedBySupplier    bool      `json:"signed_by_supplier"`
	CustomerSignatureID string    `json:"customer_signature_id"`
	SupplierSignatureID string    `json:"supplier_signature_id"`
	OccurredAt          time.Time `json:"occurred_at"`
}

func (r *Repository) Sign(ctx context.Context, cmd service.SignCommand) (out service.Result, err error) {
	err = (corepg.UnitOfWork{Pool: r.pool}).WithinTx(ctx, func(tx pgx.Tx) error {
		// Resolve and authorize the document before observing any old command.
		var dealID string
		// documents are immutable and identify one aggregate for their lifetime.
		e := tx.QueryRow(ctx, `SELECT doc.deal_id FROM documents doc JOIN deals d ON d.id=doc.deal_id JOIN deal_versions v ON v.deal_id=d.id AND v.deal_version=d.deal_version WHERE doc.id=$3 AND `+scope, cmd.Actor.UserID, cmd.Actor.OrganizationID, cmd.DocumentID).Scan(&dealID)
		if errors.Is(e, pgx.ErrNoRows) {
			return service.ErrNotFound
		}
		if e != nil {
			return e
		}
		resource := "/documents/" + cmd.DocumentID
		lock, _ := json.Marshal([]string{cmd.Actor.OrganizationID, "SIGN_DOCUMENT", resource, cmd.Key})
		if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, string(lock)); e != nil {
			return e
		}
		var lockedID string
		if e = tx.QueryRow(ctx, `SELECT id FROM deals WHERE id=$1 FOR UPDATE`, dealID).Scan(&lockedID); e != nil {
			return e
		}
		// Re-read the version and membership after the wait using a fresh snapshot.
		d, e := scan(tx.QueryRow(ctx, `SELECT `+columns+joins+` WHERE `+scope+` AND d.id=$3`, cmd.Actor.UserID, cmd.Actor.OrganizationID, dealID))
		if errors.Is(e, pgx.ErrNoRows) {
			return service.ErrNotFound
		}
		if e != nil {
			return e
		}
		var oldHash string
		e = tx.QueryRow(ctx, `SELECT request_hash,status_code,response_body FROM idempotency_keys WHERE organization_id=$1 AND operation='SIGN_DOCUMENT' AND resource=$2 AND key=$3`, cmd.Actor.OrganizationID, resource, cmd.Key).Scan(&oldHash, &out.Status, &out.Body)
		if e == nil {
			if oldHash != cmd.Hash {
				out = service.Failure{Status: 409, Code: "IDEMPOTENCY_CONFLICT", Message: "Этот ключ уже использован с другим телом запроса"}.Result(cmd.Actor.RequestID)
			}
			return nil
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		persistResult := func() error {
			_, e := tx.Exec(ctx, `INSERT INTO idempotency_keys(organization_id,operation,resource,key,request_hash,status_code,response_body) VALUES($1,'SIGN_DOCUMENT',$2,$3,$4,$5,$6)`, cmd.Actor.OrganizationID, resource, cmd.Key, cmd.Hash, out.Status, out.Body)
			return e
		}
		var now time.Time
		clock := func() error { e := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); now = now.UTC(); return e }
		if e = clock(); e != nil {
			return e
		}
		publish := func(eventID, event string, payload []byte, notification bool) error {
			if _, e := tx.Exec(ctx, `INSERT INTO audit_events(id,deal_id,deal_version,type,actor_user_id,actor_organization_id,request_id,payload,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, eventID, d.ID, d.DealVersion, event, cmd.Actor.UserID, cmd.Actor.OrganizationID, cmd.Actor.RequestID, payload, now); e != nil {
				return e
			}
			if !notification {
				return nil
			}
			jobID, e := security.ID()
			if e != nil {
				return e
			}
			_, e = tx.Exec(ctx, `INSERT INTO outbox(id,event_id,type,deal_id,payload,created_at) VALUES($1,$2,$3,$4,$5,$6)`, jobID, eventID, event, d.ID, payload, now)
			return e
		}
		reject := func(status int, code, message string) error {
			out = service.Failure{Status: status, Code: code, Message: message}.Result(cmd.Actor.RequestID)
			eventID, e := security.ID()
			if e != nil {
				return e
			}
			payload, e := json.Marshal(map[string]any{"operation": "SIGN_DOCUMENT", "result": json.RawMessage(out.Body)})
			if e != nil {
				return e
			}
			if e = publish(eventID, "SIGN_DOCUMENT_REJECTED", payload, false); e != nil {
				return e
			}
			return persistResult()
		}
		if cmd.Invalid != nil {
			return reject(cmd.Invalid.Status, cmd.Invalid.Code, cmd.Invalid.Message)
		}
		if cmd.Input.DealVersion != d.DealVersion {
			return reject(409, "VERSION_CONFLICT", "Условия сделки изменились. Обновите карточку")
		}
		if d.Document == nil || d.Document.ID != cmd.DocumentID || d.Document.DealID != d.ID || d.Document.DealVersion != d.DealVersion || d.Document.DocumentVersion != cmd.Input.DocumentVersion || d.Document.SHA256 != cmd.Input.DocumentSHA256 {
			return reject(409, "DOCUMENT_MISMATCH", "Документ изменился. Проверьте актуальную версию PDF")
		}
		var rawStatus string
		var validReserve bool
		if e = tx.QueryRow(ctx, `SELECT d.status,EXISTS(SELECT 1 FROM reserves r JOIN accounts a ON a.id=r.account_id WHERE r.deal_id=d.id AND r.deal_version=d.deal_version AND r.status='ACTIVE' AND r.customer_party_id=$2 AND r.supplier_party_id=$3 AND a.organization_id=$2 AND r.document_id=$4 AND r.document_version=$5 AND r.document_sha256=$6 AND r.amount_kopecks=$7::bigint AND r.currency=$8) FROM deals d WHERE d.id=$1`, d.ID, d.CustomerPartyID, d.SupplierPartyID, d.Document.ID, d.Document.DocumentVersion, d.Document.SHA256, d.AmountKopecks, d.Currency).Scan(&rawStatus, &validReserve); e != nil {
			return e
		}
		if rawStatus != "FUNDED" && rawStatus != "READY_TO_SETTLE" {
			return reject(409, "INVALID_STATE", "Тестовая подпись доступна после резервирования")
		}
		if !validReserve {
			return reject(422, "RESERVE_REQUIRED", "Нужен полный активный резерв текущих условий и PDF")
		}
		consents := map[string]bool{}
		for _, a := range d.Agreements {
			if a.DealVersion == d.DealVersion && a.DocumentID == d.Document.ID && a.DocumentVersion == d.Document.DocumentVersion && a.DocumentSHA256 == d.Document.SHA256 {
				consents[a.OrganisationID] = true
			}
		}
		if !consents[d.CustomerPartyID] || !consents[d.SupplierPartyID] {
			return reject(422, "CONSENTS_MISSING", "Нужны два согласия текущих условий и PDF")
		}
		respond := func(signature service.Signature) error {
			out.Status = 201
			var e error
			out.Body, e = json.Marshal(map[string]any{"signature": signature, "deal": d})
			if e != nil {
				return e
			}
			return persistResult()
		}
		for _, signature := range d.Signatures {
			if signature.OrganisationID == cmd.Actor.OrganizationID {
				// Returning an existing fact neither re-signs nor requires new PDF
				// access. It remains possible after the deadline, without new jobs.
				return respond(signature)
			}
		}
		var storage string
		if e = tx.QueryRow(ctx, `SELECT storage_key FROM document_versions WHERE document_id=$1 AND document_version=$2`, d.Document.ID, d.Document.DocumentVersion).Scan(&storage); e != nil {
			return e
		}
		if cmd.VerifyDocument == nil {
			return errors.New("document verifier unavailable")
		}
		if e = cmd.VerifyDocument(*d.Document, storage); e != nil {
			return e
		}
		// No account is changed or locked by this stage. The aggregate lock
		// serializes all its immutable signatures/reserve, and this clock is
		// sampled after both the lock wait and the potentially slow file read.
		if e = clock(); e != nil {
			return e
		}
		if !d.DeadlineAt.After(now) {
			return reject(422, "DEADLINE_PASSED", "Срок сделки уже наступил")
		}
		if rawStatus == "READY_TO_SETTLE" || len(d.Signatures) >= 2 {
			return reject(409, "INVALID_STATE", "Документ уже подписан обеими сторонами")
		}
		id, e := security.ID()
		if e != nil {
			return e
		}
		signature := service.Signature{ID: id, OrganisationID: cmd.Actor.OrganizationID, UserID: cmd.Actor.UserID, DealVersion: d.DealVersion, DocumentID: d.Document.ID, DocumentVersion: d.Document.DocumentVersion, DocumentSHA256: d.Document.SHA256, CreatedAt: now}
		if _, e = tx.Exec(ctx, `INSERT INTO document_signatures(id,deal_id,deal_version,organization_id,user_id,document_id,document_version,document_sha256,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id, d.ID, d.DealVersion, signature.OrganisationID, signature.UserID, signature.DocumentID, signature.DocumentVersion, signature.DocumentSHA256, now); e != nil {
			return e
		}
		d.Signatures = append(d.Signatures, signature)
		d.UpdatedAt = now
		if len(d.Signatures) == 2 {
			d.Status = "READY_TO_SETTLE"
			eventID, e := security.ID()
			if e != nil {
				return e
			}
			var customerSignature, supplierSignature string
			for _, sig := range d.Signatures {
				if sig.OrganisationID == d.CustomerPartyID {
					customerSignature = sig.ID
				}
				if sig.OrganisationID == d.SupplierPartyID {
					supplierSignature = sig.ID
				}
			}
			event := edoEvent{SchemaVersion: 1, EventID: eventID, Source: "edo-emulator", EventType: "document.accepted_by_both", DealID: d.ID, DealVersion: d.DealVersion, DocumentID: d.Document.ID, DocumentVersion: d.Document.DocumentVersion, DocumentSHA256: d.Document.SHA256, CustomerPartyID: d.CustomerPartyID, SupplierPartyID: d.SupplierPartyID, AmountKopecks: d.AmountKopecks, Currency: d.Currency, SignedByCustomer: true, SignedBySupplier: true, CustomerSignatureID: customerSignature, SupplierSignatureID: supplierSignature, OccurredAt: now}
			raw, e := json.Marshal(event)
			if e != nil {
				return e
			}
			if e = publish(eventID, "EDO_EVENT_READY", raw, true); e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `INSERT INTO edo_outbox(event_id,deal_id,deal_version,document_id,document_version,document_sha256,customer_signature_id,supplier_signature_id,occurred_at,raw_body) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, eventID, d.ID, d.DealVersion, d.Document.ID, d.Document.DocumentVersion, d.Document.SHA256, customerSignature, supplierSignature, now, raw); e != nil {
				return e
			}
		} else {
			d.Status = "FUNDED"
		}
		if _, e = tx.Exec(ctx, `UPDATE deals SET status=$2,row_version=row_version+1,updated_at=$3 WHERE id=$1`, d.ID, d.Status, now); e != nil {
			return e
		}
		eventID, e := security.ID()
		if e != nil {
			return e
		}
		payload, e := json.Marshal(map[string]any{"signature": signature, "deal": d})
		if e != nil {
			return e
		}
		if e = publish(eventID, "DOCUMENT_SIGNED", payload, true); e != nil {
			return e
		}
		return respond(signature)
	})
	if err != nil {
		return service.Result{}, err
	}
	return out, nil
}
