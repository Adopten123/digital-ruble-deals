-- Authenticated, durable EDO receipts and exactly one local test-fund payment.
ALTER TABLE audit_events ALTER COLUMN actor_user_id DROP NOT NULL;
ALTER TABLE audit_events ADD COLUMN actor_source text;
ALTER TABLE audit_events ADD CONSTRAINT audit_actor CHECK(num_nonnulls(actor_user_id,actor_source)=1 AND (actor_source IS NULL OR actor_source='edo-emulator'));

CREATE TABLE event_inbox (
 source text NOT NULL CHECK(source='edo-emulator'),event_id text NOT NULL,
 raw_body_hash text NOT NULL CHECK(raw_body_hash ~ '^[a-f0-9]{64}$'),raw_body bytea NOT NULL CHECK(octet_length(raw_body) BETWEEN 1 AND 65536),
 status text NOT NULL DEFAULT 'PENDING' CHECK(status IN ('PENDING','COMPLETED')),
 status_code integer,response_body bytea,created_at timestamptz NOT NULL DEFAULT clock_timestamp(),completed_at timestamptz,
 PRIMARY KEY(source,event_id),
 CHECK((status='PENDING' AND status_code IS NULL AND response_body IS NULL AND completed_at IS NULL)
  OR(status='COMPLETED' AND status_code IS NOT NULL AND status_code BETWEEN 200 AND 499 AND response_body IS NOT NULL AND completed_at IS NOT NULL))
);
CREATE FUNCTION inbox_transition() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' OR OLD.status<>'PENDING' OR NEW.status<>'COMPLETED'
  OR (to_jsonb(NEW)-ARRAY['status','status_code','response_body','completed_at']) IS DISTINCT FROM (to_jsonb(OLD)-ARRAY['status','status_code','response_body','completed_at'])
 THEN RAISE EXCEPTION 'inbox source bytes and final outcome are immutable' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER immutable_inbox BEFORE UPDATE OR DELETE ON event_inbox FOR EACH ROW EXECUTE FUNCTION inbox_transition();

CREATE TABLE edo_command_results (
 user_id text NOT NULL REFERENCES users(id),key text NOT NULL CHECK(char_length(key) BETWEEN 16 AND 128),
 request_hash text NOT NULL CHECK(request_hash ~ '^[a-f0-9]{64}$'),status_code integer NOT NULL CHECK(status_code BETWEEN 200 AND 499),
 response_body bytea NOT NULL,created_at timestamptz NOT NULL DEFAULT clock_timestamp(),PRIMARY KEY(user_id,key)
);
CREATE TRIGGER immutable_edo_commands BEFORE UPDATE OR DELETE ON edo_command_results FOR EACH ROW EXECUTE FUNCTION draft_fact_immutable();
CREATE TABLE edo_deliveries (
 id text PRIMARY KEY,event_id text NOT NULL,deal_id text NOT NULL REFERENCES deals(id),
 template text NOT NULL CHECK(template IN ('valid','single_signature','wrong_amount','wrong_parties','wrong_document','wrong_document_version','wrong_document_hash','wrong_deal_version','invalid_hmac','replay','same_id_different_body')),
 raw_body bytea NOT NULL CHECK(octet_length(raw_body) BETWEEN 1 AND 65536),automatic_basis text UNIQUE REFERENCES edo_outbox(event_id),
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts>=0),next_attempt_at timestamptz NOT NULL DEFAULT clock_timestamp(),lease_id text,lease_until timestamptz,
 http_status integer,response_body bytea,created_at timestamptz NOT NULL DEFAULT clock_timestamp(),completed_at timestamptz,
 CHECK((lease_id IS NULL)=(lease_until IS NULL)),
 CHECK((completed_at IS NULL AND http_status IS NULL AND response_body IS NULL) OR (completed_at IS NOT NULL AND http_status IS NOT NULL AND http_status BETWEEN 200 AND 499 AND response_body IS NOT NULL AND lease_id IS NULL))
);
CREATE INDEX edo_deliveries_pending ON edo_deliveries(next_attempt_at,created_at,id) WHERE completed_at IS NULL;
CREATE INDEX edo_deliveries_event ON edo_deliveries(event_id,created_at DESC,id DESC);
CREATE FUNCTION delivery_transition() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' OR OLD.completed_at IS NOT NULL
  OR (to_jsonb(NEW)-ARRAY['attempts','next_attempt_at','lease_id','lease_until','http_status','response_body','completed_at']) IS DISTINCT FROM (to_jsonb(OLD)-ARRAY['attempts','next_attempt_at','lease_id','lease_until','http_status','response_body','completed_at'])
  OR NEW.attempts<OLD.attempts OR NEW.attempts>OLD.attempts+1
 THEN RAISE EXCEPTION 'delivery source and completed result are immutable' USING ERRCODE='23514';END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER immutable_delivery_source BEFORE UPDATE OR DELETE ON edo_deliveries FOR EACH ROW EXECUTE FUNCTION delivery_transition();

ALTER TABLE reserves DROP CONSTRAINT reserve_status;
ALTER TABLE reserves ADD CONSTRAINT reserve_status CHECK(status IN ('ACTIVE','RELEASED','CONSUMED'));
ALTER TABLE ledger_batches DROP CONSTRAINT ledger_kind;
ALTER TABLE ledger_batches ADD CONSTRAINT ledger_kind CHECK(kind IN ('DEMO_SEED','DEMO_CREDIT','DEMO_DEBIT','DEAL_SETTLEMENT'));
CREATE TABLE settlements (
 id text PRIMARY KEY,deal_id text NOT NULL UNIQUE,deal_version bigint NOT NULL,
 reserve_id text NOT NULL UNIQUE REFERENCES reserves(id),batch_id text NOT NULL UNIQUE REFERENCES ledger_batches(id),
 source text NOT NULL,event_id text NOT NULL,document_id text NOT NULL,document_version bigint NOT NULL,document_sha256 text NOT NULL,
 customer_signature_id text NOT NULL,supplier_signature_id text NOT NULL,
 amount_kopecks bigint NOT NULL CHECK(amount_kopecks>0),currency text NOT NULL CHECK(currency='RUB'),created_at timestamptz NOT NULL,
 FOREIGN KEY(source,event_id) REFERENCES event_inbox(source,event_id),FOREIGN KEY(deal_id,deal_version) REFERENCES deal_versions(deal_id,deal_version),
 FOREIGN KEY(document_id,document_version,deal_id,deal_version,document_sha256) REFERENCES document_versions(document_id,document_version,deal_id,deal_version,sha256),
 FOREIGN KEY(customer_signature_id,deal_id,deal_version,document_id,document_version,document_sha256) REFERENCES document_signatures(id,deal_id,deal_version,document_id,document_version,document_sha256),
 FOREIGN KEY(supplier_signature_id,deal_id,deal_version,document_id,document_version,document_sha256) REFERENCES document_signatures(id,deal_id,deal_version,document_id,document_version,document_sha256),
 CHECK(customer_signature_id<>supplier_signature_id)
);
CREATE TRIGGER immutable_settlements BEFORE UPDATE OR DELETE ON settlements FOR EACH ROW EXECUTE FUNCTION draft_fact_immutable();
CREATE FUNCTION check_settlement_context() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE body jsonb;
BEGIN
 SELECT convert_from(raw_body,'UTF8')::jsonb INTO body FROM event_inbox WHERE source=NEW.source AND event_id=NEW.event_id AND status='PENDING';
 IF body IS NULL OR NOT EXISTS(SELECT 1 FROM deals d JOIN deal_versions v ON v.deal_id=d.id AND v.deal_version=d.deal_version
  JOIN reserves r ON r.id=NEW.reserve_id AND r.deal_id=d.id AND r.deal_version=d.deal_version
  JOIN accounts a ON a.id=r.account_id AND a.organization_id=v.customer_party_id
  JOIN document_signatures c ON c.id=NEW.customer_signature_id AND c.organization_id=v.customer_party_id
  JOIN document_signatures s ON s.id=NEW.supplier_signature_id AND s.organization_id=v.supplier_party_id
  WHERE d.id=NEW.deal_id AND d.deal_version=NEW.deal_version AND d.status='READY_TO_SETTLE' AND d.first_reserved_at IS NOT NULL
  AND d.current_document_id=NEW.document_id AND d.current_document_version=NEW.document_version
  AND r.status='ACTIVE' AND r.amount_kopecks=NEW.amount_kopecks AND r.currency=NEW.currency AND v.amount_kopecks=NEW.amount_kopecks AND v.currency=NEW.currency
  AND r.customer_party_id=v.customer_party_id AND r.supplier_party_id=v.supplier_party_id AND r.document_id=NEW.document_id AND r.document_version=NEW.document_version AND r.document_sha256=NEW.document_sha256
  AND v.deadline_at>clock_timestamp() AND NEW.created_at<v.deadline_at
  AND (SELECT count(DISTINCT organization_id) FROM deal_agreements ag WHERE ag.deal_id=d.id AND ag.deal_version=d.deal_version AND ag.document_id=NEW.document_id AND ag.document_version=NEW.document_version AND ag.document_sha256=NEW.document_sha256 AND ag.organization_id IN(v.customer_party_id,v.supplier_party_id))=2
  AND NOT EXISTS(SELECT 1 FROM deal_disputes x WHERE x.deal_id=d.id)
  AND NOT EXISTS(SELECT 1 FROM cancellation_requests cr WHERE cr.deal_id=d.id AND (NOT EXISTS(SELECT 1 FROM cancellation_decisions cd WHERE cd.request_id=cr.id) OR EXISTS(SELECT 1 FROM cancellation_decisions cd WHERE cd.request_id=cr.id AND cd.decision='REJECTED')))
  AND body->>'source'=NEW.source AND body->>'event_id'=NEW.event_id AND body->>'deal_id'=d.id AND body->>'deal_version'=d.deal_version::text
  AND body->>'customer_party_id'=v.customer_party_id AND body->>'supplier_party_id'=v.supplier_party_id AND body->>'amount_kopecks'=NEW.amount_kopecks::text AND body->>'currency'=NEW.currency
  AND body->>'document_id'=NEW.document_id AND body->>'document_version'=NEW.document_version::text AND body->>'document_sha256'=NEW.document_sha256
  AND body->>'customer_signature_id'=c.id AND body->>'supplier_signature_id'=s.id AND body->>'signed_by_customer'='true' AND body->>'signed_by_supplier'='true'
  AND (body->>'occurred_at')::timestamptz>=c.created_at AND (body->>'occurred_at')::timestamptz>=s.created_at
 ) THEN RAISE EXCEPTION 'settlement requires exact authenticated funded and unblocked signature context' USING ERRCODE='23514';END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER valid_settlement_context BEFORE INSERT ON settlements FOR EACH ROW EXECUTE FUNCTION check_settlement_context();
CREATE FUNCTION check_settlement_published() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS(SELECT 1 FROM settlements p JOIN deals d ON d.id=p.deal_id JOIN reserves r ON r.id=p.reserve_id JOIN ledger_batches b ON b.id=p.batch_id
  JOIN event_inbox e ON e.source=p.source AND e.event_id=p.event_id
  WHERE p.id=NEW.id AND d.status='SETTLED' AND d.deal_version=p.deal_version AND r.status='CONSUMED' AND b.kind='DEAL_SETTLEMENT'
  AND e.status='COMPLETED' AND e.status_code=200 AND convert_from(e.response_body,'UTF8')::jsonb->>'payment_id'=p.id
  AND EXISTS(SELECT 1 FROM ledger_entries le WHERE le.batch_id=p.batch_id AND le.account_id=r.account_id AND le.delta_kopecks=-p.amount_kopecks)
  AND EXISTS(SELECT 1 FROM ledger_entries le JOIN accounts a ON a.id=le.account_id WHERE le.batch_id=p.batch_id AND a.organization_id=r.supplier_party_id AND le.delta_kopecks=p.amount_kopecks))
 THEN RAISE EXCEPTION 'settlement, reserve, balanced payment and final inbox must publish together' USING ERRCODE='23514';END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER settlement_published AFTER INSERT ON settlements DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_settlement_published();

CREATE OR REPLACE FUNCTION reserve_status_transition() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'published reserve cannot be deleted';END IF;
 IF (to_jsonb(NEW)-'status') IS DISTINCT FROM (to_jsonb(OLD)-'status') OR OLD.status<>'ACTIVE' OR NEW.status NOT IN ('RELEASED','CONSUMED')
 THEN RAISE EXCEPTION 'reserve context is immutable and lifecycle is monotonic' USING ERRCODE='23514';END IF;
 IF NEW.status='CONSUMED' THEN
  IF NOT EXISTS(SELECT 1 FROM settlements p WHERE p.reserve_id=NEW.id AND p.deal_id=NEW.deal_id AND p.deal_version=NEW.deal_version AND p.amount_kopecks=NEW.amount_kopecks)
  THEN RAISE EXCEPTION 'reserve consumption requires its unique settlement' USING ERRCODE='23514';END IF;
 ELSE
  IF EXISTS(SELECT 1 FROM settlements p WHERE p.reserve_id=NEW.id) OR NOT EXISTS(SELECT 1 FROM cancellation_requests r JOIN cancellation_decisions c ON c.request_id=r.id JOIN deals d ON d.id=r.deal_id WHERE r.deal_id=NEW.deal_id AND r.deal_version=NEW.deal_version AND c.decision='ACCEPTED' AND d.status NOT IN ('SETTLED','CANCELLED','REFUNDED') AND c.organization_id<>r.initiator_organization_id)
  THEN RAISE EXCEPTION 'reserve release requires a second-party cancellation decision' USING ERRCODE='23514';END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE OR REPLACE FUNCTION check_reserve_resolution_published() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.status='RELEASED' AND EXISTS(SELECT 1 FROM deals d WHERE d.id=NEW.deal_id AND d.deal_version=NEW.deal_version AND d.status='REFUNDED') THEN RETURN NULL;END IF;
 IF NEW.status='CONSUMED' AND EXISTS(SELECT 1 FROM deals d JOIN settlements p ON p.deal_id=d.id AND p.reserve_id=NEW.id WHERE d.id=NEW.deal_id AND d.deal_version=NEW.deal_version AND d.status='SETTLED') THEN RETURN NULL;END IF;
 RAISE EXCEPTION 'reserve resolution and terminal deal outcome must publish together' USING ERRCODE='23514';
END $$;
CREATE OR REPLACE FUNCTION check_operator_ledger_direction() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE batch text;kind text;
BEGIN
 IF current_user<>'poaktu_runtime' THEN RETURN NULL;END IF;
 IF TG_TABLE_NAME='ledger_batches' THEN batch:=NEW.id;ELSE batch:=NEW.batch_id;END IF;
 SELECT b.kind INTO kind FROM ledger_batches b WHERE b.id=batch;
 IF kind='DEAL_SETTLEMENT' THEN
  IF NOT EXISTS(SELECT 1 FROM settlements p JOIN reserves r ON r.id=p.reserve_id JOIN ledger_entries n ON n.batch_id=p.batch_id AND n.account_id=r.account_id AND n.delta_kopecks=-p.amount_kopecks JOIN ledger_entries positive ON positive.batch_id=p.batch_id AND positive.delta_kopecks=p.amount_kopecks JOIN accounts a ON a.id=positive.account_id AND a.organization_id=r.supplier_party_id WHERE p.batch_id=batch AND r.status='CONSUMED')
  THEN RAISE EXCEPTION 'runtime settlement ledger must match its unique consumed reserve' USING ERRCODE='23514';END IF;
 ELSE
  IF kind NOT IN ('DEMO_CREDIT','DEMO_DEBIT') OR NOT EXISTS(SELECT 1 FROM ledger_entries p JOIN accounts pa ON pa.id=p.account_id JOIN ledger_entries n ON n.batch_id=p.batch_id JOIN accounts na ON na.id=n.account_id WHERE p.batch_id=batch AND p.delta_kopecks>0 AND n.delta_kopecks<0 AND ((kind='DEMO_CREDIT' AND pa.organization_id IS NOT NULL AND na.system_purpose='DEMO_ISSUANCE') OR(kind='DEMO_DEBIT' AND pa.system_purpose='DEMO_DEBITS' AND na.organization_id IS NOT NULL)))
  THEN RAISE EXCEPTION 'runtime cannot post this ledger movement' USING ERRCODE='23514';END IF;
 END IF;
 RETURN NULL;
END $$;

CREATE OR REPLACE FUNCTION agreement_runtime_transition() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE expected_block text; accepted boolean;
BEGIN
 IF current_user<>'poaktu_runtime' THEN RETURN NEW; END IF;
 IF OLD.status IN ('SETTLED','CANCELLED','REFUNDED') THEN RAISE EXCEPTION 'terminal deal cannot change'; END IF;
 IF NEW.id IS DISTINCT FROM OLD.id OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN RAISE EXCEPTION 'deal identity is immutable'; END IF;
 IF OLD.first_reserved_at IS NOT NULL AND (NEW.deal_version IS DISTINCT FROM OLD.deal_version OR NEW.current_document_id IS DISTINCT FROM OLD.current_document_id
  OR NEW.current_document_version IS DISTINCT FROM OLD.current_document_version OR NEW.first_reserved_at IS DISTINCT FROM OLD.first_reserved_at)
 THEN RAISE EXCEPTION 'funded conditions and reserve are immutable'; END IF;
 SELECT EXISTS(SELECT 1 FROM cancellation_requests r JOIN cancellation_decisions c ON c.request_id=r.id WHERE r.deal_id=OLD.id AND r.deal_version=OLD.deal_version AND c.decision='ACCEPTED') INTO accepted;
 IF NEW.status='SETTLED' THEN
  IF OLD.status<>'READY_TO_SETTLE' OR NEW.deal_version<>OLD.deal_version OR NEW.current_document_id IS DISTINCT FROM OLD.current_document_id OR NEW.current_document_version IS DISTINCT FROM OLD.current_document_version OR NEW.first_reserved_at IS DISTINCT FROM OLD.first_reserved_at
   OR EXISTS(SELECT 1 FROM deal_disputes x WHERE x.deal_id=OLD.id)
   OR EXISTS(SELECT 1 FROM cancellation_requests cr WHERE cr.deal_id=OLD.id AND (NOT EXISTS(SELECT 1 FROM cancellation_decisions cd WHERE cd.request_id=cr.id) OR EXISTS(SELECT 1 FROM cancellation_decisions cd WHERE cd.request_id=cr.id AND cd.decision='REJECTED')))
   OR NOT EXISTS(SELECT 1 FROM settlements p JOIN reserves r ON r.id=p.reserve_id WHERE p.deal_id=NEW.id AND p.deal_version=NEW.deal_version AND p.document_id=NEW.current_document_id AND p.document_version=NEW.current_document_version AND r.status='CONSUMED')
  THEN RAISE EXCEPTION 'settled outcome requires its exact unblocked payment and consumed reserve';END IF;
  RETURN NEW;
 END IF;
 IF NEW.status IN ('CANCELLED','REFUNDED') THEN
  IF NEW.deal_version<>OLD.deal_version OR NEW.current_document_id IS DISTINCT FROM OLD.current_document_id OR NEW.current_document_version IS DISTINCT FROM OLD.current_document_version OR NEW.first_reserved_at IS DISTINCT FROM OLD.first_reserved_at
   OR NOT accepted OR (NEW.status='CANCELLED' AND (OLD.first_reserved_at IS NOT NULL OR EXISTS(SELECT 1 FROM reserves WHERE deal_id=OLD.id)))
   OR (NEW.status='REFUNDED' AND NOT EXISTS(SELECT 1 FROM reserves WHERE deal_id=OLD.id AND status='RELEASED'))
  THEN RAISE EXCEPTION 'cancellation outcome requires its exact second-party decision and reserve resolution'; END IF;
  RETURN NEW;
 END IF;
 SELECT CASE WHEN v.deadline_at<=clock_timestamp() OR EXISTS(SELECT 1 FROM cancellation_requests r JOIN cancellation_decisions c ON c.request_id=r.id WHERE r.deal_id=OLD.id AND c.decision='REJECTED') THEN 'REVIEW_REQUIRED'
  WHEN EXISTS(SELECT 1 FROM cancellation_requests r WHERE r.deal_id=OLD.id AND NOT EXISTS(SELECT 1 FROM cancellation_decisions c WHERE c.request_id=r.id)) THEN 'CANCELLATION_PENDING'
  WHEN EXISTS(SELECT 1 FROM deal_disputes x WHERE x.deal_id=OLD.id) THEN 'DISPUTED' ELSE NULL END
 INTO expected_block FROM deal_versions v WHERE v.deal_id=OLD.id AND v.deal_version=OLD.deal_version;
 IF expected_block IS NOT NULL THEN
  IF NEW.status<>expected_block OR NEW.deal_version<>OLD.deal_version OR NEW.current_document_id IS DISTINCT FROM OLD.current_document_id OR NEW.current_document_version IS DISTINCT FROM OLD.current_document_version OR NEW.first_reserved_at IS DISTINCT FROM OLD.first_reserved_at
  THEN RAISE EXCEPTION 'blocking facts prohibit ordinary deal transitions'; END IF;
  RETURN NEW;
 END IF;
 IF OLD.first_reserved_at IS NOT NULL THEN
  IF OLD.status NOT IN ('FUNDED','READY_TO_SETTLE') OR NEW.status NOT IN ('FUNDED','READY_TO_SETTLE') OR (OLD.status='READY_TO_SETTLE' AND NEW.status<>'READY_TO_SETTLE') THEN RAISE EXCEPTION 'unsupported funded transition'; END IF;
  IF NOT EXISTS(SELECT 1 FROM reserves r JOIN deal_versions v ON v.deal_id=NEW.id AND v.deal_version=NEW.deal_version JOIN accounts a ON a.id=r.account_id
   WHERE r.deal_id=NEW.id AND r.deal_version=NEW.deal_version AND r.status='ACTIVE' AND r.customer_party_id=v.customer_party_id AND r.supplier_party_id=v.supplier_party_id
   AND a.organization_id=v.customer_party_id AND r.amount_kopecks=v.amount_kopecks AND r.currency=v.currency AND r.document_id=NEW.current_document_id AND r.document_version=NEW.current_document_version)
  THEN RAISE EXCEPTION 'funded deal requires full active reserve'; END IF;
  IF NEW.status='READY_TO_SETTLE' AND ((SELECT count(DISTINCT s.organization_id) FROM document_signatures s JOIN deal_versions v ON v.deal_id=NEW.id AND v.deal_version=NEW.deal_version WHERE s.deal_id=NEW.id AND s.deal_version=NEW.deal_version AND s.document_id=NEW.current_document_id AND s.document_version=NEW.current_document_version AND s.organization_id IN(v.customer_party_id,v.supplier_party_id))<>2 OR NOT EXISTS(SELECT 1 FROM edo_outbox e WHERE e.deal_id=NEW.id AND e.deal_version=NEW.deal_version AND e.document_id=NEW.current_document_id AND e.document_version=NEW.current_document_version)) THEN RAISE EXCEPTION 'ready deal requires two signatures and immutable EDO basis'; END IF;
 ELSE
  IF NEW.status NOT IN ('DRAFT','AGREED','FUNDED') THEN RAISE EXCEPTION 'unsupported runtime deal transition'; END IF;
  IF NEW.status='FUNDED' THEN
   IF OLD.status<>'AGREED' OR NEW.first_reserved_at IS NULL OR NEW.deal_version<>OLD.deal_version OR NOT EXISTS(SELECT 1 FROM reserves r WHERE r.deal_id=NEW.id AND r.deal_version=NEW.deal_version AND r.status='ACTIVE') THEN RAISE EXCEPTION 'funded deal requires reserve'; END IF;
  ELSIF NEW.first_reserved_at IS DISTINCT FROM OLD.first_reserved_at THEN RAISE EXCEPTION 'reserve timestamp requires funded state'; END IF;
 END IF;
 RETURN NEW;
END $$;

DO $$
DECLARE trusted_schema text:=pg_catalog.current_schema();guard_name text;
BEGIN
 IF trusted_schema IS NULL OR trusted_schema IN ('pg_catalog','information_schema') OR trusted_schema LIKE 'pg_temp_%' THEN RAISE EXCEPTION 'migrations require a trusted application schema';END IF;
 FOREACH guard_name IN ARRAY ARRAY['inbox_transition','delivery_transition','check_settlement_context','check_settlement_published','reserve_status_transition','check_reserve_resolution_published','check_operator_ledger_direction','agreement_runtime_transition'] LOOP
  EXECUTE pg_catalog.format('ALTER FUNCTION %I.%I() SET search_path TO pg_catalog, %I, pg_temp',trusted_schema,guard_name,trusted_schema);
 END LOOP;
END $$;

