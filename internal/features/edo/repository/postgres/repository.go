package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	corepg "github.com/Adopten123/digital-ruble-deals/internal/core/repository/postgres"
	"github.com/Adopten123/digital-ruble-deals/internal/core/security"
	deals "github.com/Adopten123/digital-ruble-deals/internal/features/deals/service"
	"github.com/Adopten123/digital-ruble-deals/internal/features/edo/service"
	"github.com/Adopten123/digital-ruble-deals/internal/features/settlement/ledger"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

func (r *Repository) Accept(ctx context.Context, env service.Envelope) (out *deals.Result, err error) {
	err = (corepg.UnitOfWork{Pool: r.pool}).WithinTx(ctx, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `INSERT INTO event_inbox(source,event_id,raw_body_hash,raw_body) VALUES($1,$2,$3,$4) ON CONFLICT(source,event_id) DO NOTHING`, env.Event.Source, env.Event.EventID, env.Hash, env.Raw); e != nil {
			return e
		}
		var hash, state string
		var status *int
		var body []byte
		if e := tx.QueryRow(ctx, `SELECT raw_body_hash,status,status_code,response_body FROM event_inbox WHERE source=$1 AND event_id=$2`, env.Event.Source, env.Event.EventID).Scan(&hash, &state, &status, &body); e != nil {
			return e
		}
		if hash != env.Hash {
			v := (deals.Failure{Status: 409, Code: "EVENT_ID_CONFLICT", Message: "Событие с этим ID уже принято с другим телом"}).Result(env.RequestID)
			out = &v
		} else if state == "COMPLETED" {
			out = &deals.Result{Status: *status, Body: body}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

type dealContext struct {
	ID                         string
	Version                    int64
	Status, Customer, Supplier string
	Amount                     int64
	Currency                   string
	Deadline                   time.Time
	DocumentID                 string
	DocumentVersion            int64
	Hash                       string
	Blocked                    bool
}

func (r *Repository) Process(ctx context.Context, env service.Envelope) (out deals.Result, err error) {
	err = (corepg.UnitOfWork{Pool: r.pool}).WithinTx(ctx, func(tx pgx.Tx) error {
		event := env.Event
		var hash, state string
		var status *int
		if e := tx.QueryRow(ctx, `SELECT raw_body_hash,status,status_code,response_body FROM event_inbox WHERE source=$1 AND event_id=$2 FOR UPDATE`, event.Source, event.EventID).Scan(&hash, &state, &status, &out.Body); e != nil {
			return e
		}
		if hash != env.Hash {
			out = (deals.Failure{Status: 409, Code: "EVENT_ID_CONFLICT", Message: "Событие с этим ID уже принято с другим телом"}).Result(env.RequestID)
			return nil
		}
		if state == "COMPLETED" {
			out.Status = *status
			return nil
		}
		var lockedID string
		e := tx.QueryRow(ctx, `SELECT id FROM deals WHERE id=$1 FOR UPDATE`, event.DealID).Scan(&lockedID)
		found := e == nil
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		var d dealContext
		var now time.Time
		finish := func(status int, code, message, payment string) error {
			if status == 200 {
				out.Status = 200
				out.Body, _ = json.Marshal(map[string]any{"event_id": event.EventID, "result": code, "deal_id": event.DealID, "payment_id": payment})
			} else {
				out = (deals.Failure{Status: status, Code: code, Message: message}).Result(env.RequestID)
			}
			if e := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
				return e
			}
			id, e := security.ID()
			if e != nil {
				return e
			}
			payload, e := json.Marshal(map[string]any{"event_id": event.EventID, "result": json.RawMessage(out.Body)})
			if e != nil {
				return e
			}
			var dealID any
			var version any
			if found {
				dealID = d.ID
				version = d.Version
			}
			typeName := "EDO_EVENT_REJECTED"
			if status == 200 {
				typeName = "EDO_EVENT_PROCESSED"
			}
			if _, e = tx.Exec(ctx, `INSERT INTO audit_events(id,deal_id,deal_version,type,actor_source,request_id,payload,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, id, dealID, version, typeName, event.Source, env.RequestID, payload, now); e != nil {
				return e
			}
			_, e = tx.Exec(ctx, `UPDATE event_inbox SET status='COMPLETED',status_code=$3,response_body=$4,completed_at=$5 WHERE source=$1 AND event_id=$2`, event.Source, event.EventID, out.Status, out.Body, now)
			return e
		}
		if !found {
			return finish(422, "EVENT_BINDING_MISMATCH", "Событие не соответствует существующей сделке", "")
		}
		// The aggregate was locked separately. This statement sees facts committed
		// by a dispute/cancellation while this transaction waited for the lock.
		e = tx.QueryRow(ctx, `SELECT d.id,d.deal_version,d.status,v.customer_party_id,v.supplier_party_id,v.amount_kopecks,v.currency,v.deadline_at,COALESCE(d.current_document_id,''),COALESCE(d.current_document_version,0),COALESCE(dv.sha256,''),
		 EXISTS(SELECT 1 FROM deal_disputes x WHERE x.deal_id=d.id) OR EXISTS(SELECT 1 FROM cancellation_requests c WHERE c.deal_id=d.id AND NOT EXISTS(SELECT 1 FROM cancellation_decisions cd WHERE cd.request_id=c.id)) OR EXISTS(SELECT 1 FROM cancellation_requests c JOIN cancellation_decisions cd ON cd.request_id=c.id WHERE c.deal_id=d.id AND cd.decision='REJECTED')
		 FROM deals d JOIN deal_versions v ON v.deal_id=d.id AND v.deal_version=d.deal_version LEFT JOIN document_versions dv ON dv.document_id=d.current_document_id AND dv.document_version=d.current_document_version WHERE d.id=$1`, event.DealID).Scan(&d.ID, &d.Version, &d.Status, &d.Customer, &d.Supplier, &d.Amount, &d.Currency, &d.Deadline, &d.DocumentID, &d.DocumentVersion, &d.Hash, &d.Blocked)
		if e != nil {
			return e
		}
		if d.Version != event.DealVersion || d.Customer != event.CustomerPartyID || d.Supplier != event.SupplierPartyID || d.Currency != event.Currency || event.AmountKopecks != amountString(d.Amount) || d.DocumentID != event.DocumentID || d.DocumentVersion != event.DocumentVersion || d.Hash != event.DocumentSHA256 {
			return finish(422, "EVENT_BINDING_MISMATCH", "Стороны, сумма, условия или PDF события не совпадают", "")
		}
		if !event.SignedByCustomer || !event.SignedBySupplier || event.CustomerSignatureID == nil || event.SupplierSignatureID == nil {
			return finish(422, "SIGNATURES_MISSING", "Нужны две сохранённые подписи сторон", "")
		}
		var signatures bool
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM document_signatures c JOIN document_signatures s ON s.id=$2 WHERE c.id=$1 AND c.id<>s.id AND c.organization_id=$3 AND s.organization_id=$4 AND c.deal_id=$5 AND s.deal_id=$5 AND c.deal_version=$6 AND s.deal_version=$6 AND c.document_id=$7 AND s.document_id=$7 AND c.document_version=$8 AND s.document_version=$8 AND c.document_sha256=$9 AND s.document_sha256=$9 AND c.created_at<=$10 AND s.created_at<=$10)`, *event.CustomerSignatureID, *event.SupplierSignatureID, d.Customer, d.Supplier, d.ID, d.Version, d.DocumentID, d.DocumentVersion, d.Hash, event.OccurredAt).Scan(&signatures)
		if e != nil {
			return e
		}
		if !signatures {
			return finish(422, "SIGNATURES_MISSING", "Ссылки события не подтверждают две подписи точного документа", "")
		}
		var oldPayment string
		e = tx.QueryRow(ctx, `SELECT id FROM settlements WHERE deal_id=$1`, d.ID).Scan(&oldPayment)
		if e == nil {
			return finish(200, "already_settled", "", oldPayment)
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		if d.Status != "READY_TO_SETTLE" || d.Blocked {
			return finish(422, "SETTLEMENT_BLOCKED", "Состояние, спор или отмена блокируют расчёт", "")
		}
		var consents bool
		e = tx.QueryRow(ctx, `SELECT count(DISTINCT organization_id)=2 FROM deal_agreements WHERE deal_id=$1 AND deal_version=$2 AND document_id=$3 AND document_version=$4 AND document_sha256=$5 AND organization_id IN($6,$7)`, d.ID, d.Version, d.DocumentID, d.DocumentVersion, d.Hash, d.Customer, d.Supplier).Scan(&consents)
		if e != nil {
			return e
		}
		if !consents {
			return finish(422, "CONSENTS_MISSING", "Нужны два согласия точного основания", "")
		}
		var from, to string
		e = tx.QueryRow(ctx, `SELECT c.id,s.id FROM accounts c CROSS JOIN accounts s WHERE c.organization_id=$1 AND s.organization_id=$2 AND c.currency=$3 AND s.currency=$3`, d.Customer, d.Supplier, d.Currency).Scan(&from, &to)
		if e != nil {
			return e
		}
		accounts, e := ledger.Lock(ctx, tx, from, to)
		if e != nil {
			return e
		}
		var reserveID string
		var validReserve bool
		e = tx.QueryRow(ctx, `SELECT id,(status='ACTIVE' AND deal_version=$2 AND account_id=$3 AND customer_party_id=$4 AND supplier_party_id=$5 AND document_id=$6 AND document_version=$7 AND document_sha256=$8 AND amount_kopecks=$9 AND currency=$10) FROM reserves WHERE deal_id=$1 FOR UPDATE`, d.ID, d.Version, from, d.Customer, d.Supplier, d.DocumentID, d.DocumentVersion, d.Hash, d.Amount, d.Currency).Scan(&reserveID, &validReserve)
		if errors.Is(e, pgx.ErrNoRows) {
			return finish(422, "SETTLEMENT_BLOCKED", "Нужен активный резерв полной суммы", "")
		}
		if e != nil {
			return e
		}
		if !validReserve {
			return finish(422, "SETTLEMENT_BLOCKED", "Резерв не соответствует основанию расчёта", "")
		}
		if e = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
			return e
		}
		now = now.UTC()
		if !d.Deadline.After(now) {
			return finish(422, "DEADLINE_PASSED", "Срок сделки наступил; нужен разбор сторонами", "")
		}
		if e = ledger.CheckReservedTransfer(accounts[from], accounts[to], d.Amount); errors.Is(e, ledger.ErrOverflow) {
			return finish(422, "AMOUNT_OVERFLOW", "Остаток получателя выходит за диапазон целых копеек", "")
		} else if e != nil {
			return finish(422, "SETTLEMENT_BLOCKED", "Резерв и остатки требуют проверки", "")
		}
		payment, e := security.ID()
		if e != nil {
			return e
		}
		batch, e := ledger.PostReserved(ctx, tx, "settlement:"+d.ID, from, to, d.Amount)
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO settlements(id,deal_id,deal_version,reserve_id,batch_id,source,event_id,document_id,document_version,document_sha256,customer_signature_id,supplier_signature_id,amount_kopecks,currency,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`, payment, d.ID, d.Version, reserveID, batch, event.Source, event.EventID, d.DocumentID, d.DocumentVersion, d.Hash, *event.CustomerSignatureID, *event.SupplierSignatureID, d.Amount, d.Currency, now); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `UPDATE reserves SET status='CONSUMED' WHERE id=$1`, reserveID); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `UPDATE deals SET status='SETTLED',row_version=row_version+1,updated_at=$2 WHERE id=$1`, d.ID, now); e != nil {
			return e
		}
		id, e := security.ID()
		if e != nil {
			return e
		}
		job, e := security.ID()
		if e != nil {
			return e
		}
		payload, _ := json.Marshal(map[string]any{"deal_id": d.ID, "payment_id": payment, "amount_kopecks": amountString(d.Amount), "event_id": event.EventID})
		if _, e = tx.Exec(ctx, `INSERT INTO audit_events(id,deal_id,deal_version,type,actor_source,request_id,payload,created_at) VALUES($1,$2,$3,'DEAL_SETTLED',$4,$5,$6,$7)`, id, d.ID, d.Version, event.Source, env.RequestID, payload, now); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO outbox(id,event_id,type,deal_id,payload,created_at) VALUES($1,$2,'DEAL_SETTLED',$3,$4,$5)`, job, id, d.ID, payload, now); e != nil {
			return e
		}
		return finish(200, "settled", "", payment)
	})
	if err != nil {
		return deals.Result{}, err
	}
	return out, nil
}
