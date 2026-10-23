-- Only reservation and balanced operator test-fund movements; no settlement.
ALTER TABLE accounts DROP CONSTRAINT accounts_check1;
ALTER TABLE accounts ADD CONSTRAINT account_balance_policy CHECK (
 (organization_id IS NOT NULL AND system_purpose IS NULL AND total_kopecks>=reserved_kopecks)
 OR (organization_id IS NULL AND system_purpose='DEMO_ISSUANCE' AND reserved_kopecks=0)
 OR (organization_id IS NULL AND system_purpose='DEMO_DEBITS' AND total_kopecks>=0 AND reserved_kopecks=0)
);
ALTER TABLE ledger_batches DROP CONSTRAINT ledger_batches_kind_check;
ALTER TABLE ledger_batches ADD CONSTRAINT ledger_kind CHECK(kind IN ('DEMO_SEED','DEMO_CREDIT','DEMO_DEBIT'));
ALTER TABLE audit_events ALTER COLUMN actor_organization_id DROP NOT NULL;
ALTER TABLE outbox ALTER COLUMN deal_id DROP NOT NULL;
CREATE TABLE operator_command_results (
 user_id text NOT NULL REFERENCES users(id), operation text NOT NULL,resource text NOT NULL,
 key text NOT NULL CHECK(char_length(key) BETWEEN 16 AND 128), request_hash text NOT NULL CHECK(length(request_hash)=64),
 status_code integer NOT NULL CHECK(status_code BETWEEN 200 AND 499),response_body bytea NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),PRIMARY KEY(user_id,operation,resource,key)
);
CREATE TRIGGER immutable_operator_results BEFORE UPDATE OR DELETE ON operator_command_results FOR EACH ROW EXECUTE FUNCTION draft_fact_immutable();
CREATE TABLE reserves (
 id text PRIMARY KEY, deal_id text NOT NULL UNIQUE, deal_version bigint NOT NULL,
 account_id text NOT NULL REFERENCES accounts(id), customer_party_id text NOT NULL REFERENCES organizations(id),supplier_party_id text NOT NULL REFERENCES organizations(id),
 document_id text NOT NULL,document_version bigint NOT NULL,document_sha256 text NOT NULL,
 amount_kopecks bigint NOT NULL CHECK(amount_kopecks>0),currency text NOT NULL CHECK(currency='RUB'),
 status text NOT NULL DEFAULT 'ACTIVE' CHECK(status='ACTIVE'),created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(deal_id,deal_version) REFERENCES deal_versions(deal_id,deal_version),
 FOREIGN KEY(document_id,document_version,deal_id,deal_version,document_sha256) REFERENCES document_versions(document_id,document_version,deal_id,deal_version,sha256)
);
CREATE INDEX reserves_account_idx ON reserves(account_id);
CREATE TRIGGER immutable_reserves BEFORE UPDATE OR DELETE ON reserves FOR EACH ROW EXECUTE FUNCTION draft_fact_immutable();
CREATE FUNCTION check_reserve_context() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS(SELECT 1 FROM deals d JOIN deal_versions v ON v.deal_id=d.id AND v.deal_version=d.deal_version JOIN accounts a ON a.id=NEW.account_id
 WHERE d.id=NEW.deal_id AND d.deal_version=NEW.deal_version AND d.current_document_id=NEW.document_id AND d.current_document_version=NEW.document_version
 AND v.customer_party_id=NEW.customer_party_id AND v.supplier_party_id=NEW.supplier_party_id AND v.amount_kopecks=NEW.amount_kopecks AND v.currency=NEW.currency AND a.organization_id=v.customer_party_id
 AND (SELECT count(DISTINCT ag.organization_id) FROM deal_agreements ag WHERE ag.deal_id=d.id AND ag.deal_version=d.deal_version AND ag.document_id=NEW.document_id AND ag.document_version=NEW.document_version AND ag.document_sha256=NEW.document_sha256 AND ag.organization_id IN(v.customer_party_id,v.supplier_party_id))=2) THEN
 RAISE EXCEPTION 'reserve must match exact current agreed context' USING ERRCODE='23514'; END IF; RETURN NEW;
END $$;
CREATE TRIGGER valid_reserve_context BEFORE INSERT ON reserves FOR EACH ROW EXECUTE FUNCTION check_reserve_context();
CREATE FUNCTION check_reserve_published_state() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS(SELECT 1 FROM deals d WHERE d.id=NEW.deal_id AND d.deal_version=NEW.deal_version AND d.status='FUNDED' AND d.first_reserved_at IS NOT NULL) THEN RAISE EXCEPTION 'reserve and funded state must publish together' USING ERRCODE='23514';END IF;RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER reserve_published_state AFTER INSERT ON reserves DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_reserve_published_state();
CREATE OR REPLACE FUNCTION agreement_runtime_transition() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF current_user='poaktu_runtime' THEN
  IF OLD.first_reserved_at IS NOT NULL OR NEW.status NOT IN ('DRAFT','AGREED','FUNDED') THEN RAISE EXCEPTION 'unsupported runtime deal transition'; END IF;
  IF NEW.status='FUNDED' THEN
   IF OLD.status<>'AGREED' OR NEW.first_reserved_at IS NULL OR NEW.deal_version<>OLD.deal_version
    OR NOT EXISTS(SELECT 1 FROM reserves r WHERE r.deal_id=NEW.id AND r.deal_version=NEW.deal_version AND r.status='ACTIVE') THEN RAISE EXCEPTION 'funded deal requires reserve'; END IF;
  ELSIF NEW.first_reserved_at IS DISTINCT FROM OLD.first_reserved_at THEN RAISE EXCEPTION 'reserve timestamp requires funded state'; END IF;
 END IF;RETURN NEW;
END $$;
CREATE FUNCTION check_account_projection() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target text;actual_total numeric;actual_reserved numeric;
BEGIN
 IF TG_TABLE_NAME='accounts' THEN target:=NEW.id;ELSE target:=NEW.account_id;END IF;
 SELECT COALESCE(sum(delta_kopecks::numeric),0) INTO actual_total FROM ledger_entries WHERE account_id=target;
 SELECT COALESCE(sum(amount_kopecks::numeric),0) INTO actual_reserved FROM reserves WHERE account_id=target AND status='ACTIVE';
 IF NOT EXISTS(SELECT 1 FROM accounts WHERE id=target AND total_kopecks::numeric=actual_total AND reserved_kopecks::numeric=actual_reserved) THEN RAISE EXCEPTION 'account projections disagree with immutable facts' USING ERRCODE='23514';END IF;RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER account_projection AFTER INSERT OR UPDATE ON accounts DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_account_projection();
CREATE CONSTRAINT TRIGGER ledger_projection AFTER INSERT ON ledger_entries DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_account_projection();
CREATE CONSTRAINT TRIGGER reserve_projection AFTER INSERT ON reserves DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_account_projection();
CREATE FUNCTION check_operator_ledger_direction() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE batch text;kind text;
BEGIN
 IF current_user<>'poaktu_runtime' THEN RETURN NULL;END IF;
 IF TG_TABLE_NAME='ledger_batches' THEN batch:=NEW.id;ELSE batch:=NEW.batch_id;END IF;
 SELECT b.kind INTO kind FROM ledger_batches b WHERE b.id=batch;
 IF kind NOT IN ('DEMO_CREDIT','DEMO_DEBIT') OR NOT EXISTS(
 SELECT 1 FROM ledger_entries p JOIN accounts pa ON pa.id=p.account_id JOIN ledger_entries n ON n.batch_id=p.batch_id JOIN accounts na ON na.id=n.account_id
 WHERE p.batch_id=batch AND p.delta_kopecks>0 AND n.delta_kopecks<0
 AND ((kind='DEMO_CREDIT' AND pa.organization_id IS NOT NULL AND na.system_purpose='DEMO_ISSUANCE') OR(kind='DEMO_DEBIT' AND pa.system_purpose='DEMO_DEBITS' AND na.organization_id IS NOT NULL))) THEN RAISE EXCEPTION 'runtime cannot post this ledger movement' USING ERRCODE='23514';END IF;RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER operator_batch_direction AFTER INSERT ON ledger_batches DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_operator_ledger_direction();
CREATE CONSTRAINT TRIGGER operator_entries_direction AFTER INSERT ON ledger_entries DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_operator_ledger_direction();
