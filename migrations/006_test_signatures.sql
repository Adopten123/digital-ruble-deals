-- Test signatures and immutable EDO source bytes; no settlement or delivery.
CREATE TABLE document_signatures (
 id text PRIMARY KEY, deal_id text NOT NULL, deal_version bigint NOT NULL,
 organization_id text NOT NULL REFERENCES organizations(id), user_id text NOT NULL REFERENCES users(id),
 document_id text NOT NULL, document_version bigint NOT NULL, document_sha256 text NOT NULL,
 created_at timestamptz NOT NULL,
 UNIQUE(deal_id,deal_version,document_id,document_version,organization_id),
 UNIQUE(id,deal_id,deal_version,document_id,document_version,document_sha256),
 FOREIGN KEY(document_id,document_version,deal_id,deal_version,document_sha256)
  REFERENCES document_versions(document_id,document_version,deal_id,deal_version,sha256)
);
CREATE TRIGGER immutable_signatures BEFORE UPDATE OR DELETE ON document_signatures FOR EACH ROW EXECUTE FUNCTION draft_fact_immutable();
CREATE TABLE edo_outbox (
 event_id text PRIMARY KEY REFERENCES outbox(event_id), deal_id text NOT NULL UNIQUE,
 deal_version bigint NOT NULL, document_id text NOT NULL, document_version bigint NOT NULL, document_sha256 text NOT NULL,
 customer_signature_id text NOT NULL, supplier_signature_id text NOT NULL,
 occurred_at timestamptz NOT NULL, raw_body bytea NOT NULL CHECK(octet_length(raw_body) BETWEEN 1 AND 65536),
 CHECK(customer_signature_id<>supplier_signature_id),
 FOREIGN KEY(document_id,document_version,deal_id,deal_version,document_sha256)
  REFERENCES document_versions(document_id,document_version,deal_id,deal_version,sha256),
 FOREIGN KEY(customer_signature_id,deal_id,deal_version,document_id,document_version,document_sha256)
  REFERENCES document_signatures(id,deal_id,deal_version,document_id,document_version,document_sha256),
 FOREIGN KEY(supplier_signature_id,deal_id,deal_version,document_id,document_version,document_sha256)
  REFERENCES document_signatures(id,deal_id,deal_version,document_id,document_version,document_sha256)
);
CREATE TRIGGER immutable_edo_source BEFORE UPDATE OR DELETE ON edo_outbox FOR EACH ROW EXECUTE FUNCTION draft_fact_immutable();
CREATE UNIQUE INDEX one_edo_job_per_deal ON outbox(deal_id) WHERE type='EDO_EVENT_READY';

CREATE FUNCTION check_signature_context() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 -- The aggregate is always locked before its subordinate immutable facts.
 PERFORM 1 FROM deals WHERE id=NEW.deal_id FOR UPDATE;
 IF NOT EXISTS(SELECT 1 FROM deals d
  JOIN deal_versions v ON v.deal_id=d.id AND v.deal_version=d.deal_version
  JOIN reserves r ON r.deal_id=d.id AND r.deal_version=d.deal_version
  JOIN accounts a ON a.id=r.account_id
  WHERE d.id=NEW.deal_id AND d.deal_version=NEW.deal_version AND d.status='FUNDED' AND d.first_reserved_at IS NOT NULL
  AND d.current_document_id=NEW.document_id AND d.current_document_version=NEW.document_version
  AND NEW.organization_id IN(v.customer_party_id,v.supplier_party_id)
  AND EXISTS(SELECT 1 FROM memberships m JOIN users u ON u.id=m.user_id WHERE m.user_id=NEW.user_id AND m.organization_id=NEW.organization_id AND m.role='ORG_OWNER' AND u.system_role='USER')
  AND r.status='ACTIVE' AND r.document_id=NEW.document_id AND r.document_version=NEW.document_version AND r.document_sha256=NEW.document_sha256
  AND r.customer_party_id=v.customer_party_id AND r.supplier_party_id=v.supplier_party_id AND a.organization_id=v.customer_party_id
  AND r.amount_kopecks=v.amount_kopecks AND r.currency=v.currency AND v.deadline_at>clock_timestamp() AND NEW.created_at<v.deadline_at
  AND (SELECT count(DISTINCT ag.organization_id) FROM deal_agreements ag WHERE ag.deal_id=d.id AND ag.deal_version=d.deal_version
   AND ag.document_id=NEW.document_id AND ag.document_version=NEW.document_version AND ag.document_sha256=NEW.document_sha256
   AND ag.organization_id IN(v.customer_party_id,v.supplier_party_id))=2
 ) THEN RAISE EXCEPTION 'signature must match current funded context and its own organisation' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER valid_signature_context BEFORE INSERT ON document_signatures FOR EACH ROW EXECUTE FUNCTION check_signature_context();

CREATE FUNCTION check_edo_outbox_context() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE body jsonb; expected jsonb; expected_time timestamptz;
BEGIN
 body:=convert_from(NEW.raw_body,'UTF8')::jsonb;
 SELECT jsonb_build_object('schema_version',1,'event_id',NEW.event_id,'source','edo-emulator','event_type','document.accepted_by_both',
  'deal_id',d.id,'deal_version',d.deal_version,'document_id',NEW.document_id,'document_version',NEW.document_version,'document_sha256',NEW.document_sha256,
  'customer_party_id',v.customer_party_id,'supplier_party_id',v.supplier_party_id,'amount_kopecks',v.amount_kopecks::text,'currency',v.currency,
  'signed_by_customer',true,'signed_by_supplier',true,'customer_signature_id',c.id,'supplier_signature_id',s.id)
 INTO expected FROM deals d JOIN deal_versions v ON v.deal_id=d.id AND v.deal_version=d.deal_version
 JOIN document_signatures c ON c.id=NEW.customer_signature_id AND c.organization_id=v.customer_party_id
 JOIN document_signatures s ON s.id=NEW.supplier_signature_id AND s.organization_id=v.supplier_party_id
 JOIN reserves r ON r.deal_id=d.id AND r.deal_version=d.deal_version
 JOIN accounts a ON a.id=r.account_id
 WHERE d.id=NEW.deal_id AND d.deal_version=NEW.deal_version AND d.status='FUNDED' AND d.first_reserved_at IS NOT NULL
 AND d.current_document_id=NEW.document_id AND d.current_document_version=NEW.document_version
 AND r.status='ACTIVE' AND r.document_id=NEW.document_id AND r.document_version=NEW.document_version AND r.document_sha256=NEW.document_sha256
 AND r.customer_party_id=v.customer_party_id AND r.supplier_party_id=v.supplier_party_id AND a.organization_id=v.customer_party_id
 AND r.amount_kopecks=v.amount_kopecks AND r.currency=v.currency
 AND c.created_at<=NEW.occurred_at AND s.created_at<=NEW.occurred_at;
 IF expected IS NULL OR jsonb_typeof(body)<>'object' OR (body-'occurred_at')<>expected OR jsonb_typeof(body->'occurred_at')<>'string'
 THEN RAISE EXCEPTION 'EDO bytes must match both exact persisted signatures' USING ERRCODE='23514'; END IF;
 expected_time:=(body->>'occurred_at')::timestamptz;
 IF expected_time IS DISTINCT FROM NEW.occurred_at OR NOT EXISTS(SELECT 1 FROM outbox o WHERE o.event_id=NEW.event_id AND o.type='EDO_EVENT_READY' AND o.deal_id=NEW.deal_id AND o.payload=body AND o.created_at=NEW.occurred_at AND o.delivered_at IS NULL)
 THEN RAISE EXCEPTION 'EDO source and its pending outbox job must publish together' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER valid_edo_outbox_context BEFORE INSERT ON edo_outbox FOR EACH ROW EXECUTE FUNCTION check_edo_outbox_context();

CREATE FUNCTION check_signature_published_state() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE signatures bigint; state text;
BEGIN
 SELECT d.status,count(s.id) INTO state,signatures FROM deals d LEFT JOIN document_signatures s ON s.deal_id=d.id
  AND s.deal_version=d.deal_version AND s.document_id=d.current_document_id AND s.document_version=d.current_document_version
 WHERE d.id=NEW.deal_id GROUP BY d.status;
 IF (signatures=1 AND state='FUNDED') OR (signatures=2 AND state='READY_TO_SETTLE' AND EXISTS(SELECT 1 FROM edo_outbox e WHERE e.deal_id=NEW.deal_id)) THEN RETURN NULL; END IF;
 RAISE EXCEPTION 'two signatures, ready state and immutable EDO basis must publish together' USING ERRCODE='23514';
END $$;
CREATE CONSTRAINT TRIGGER signatures_published_state AFTER INSERT ON document_signatures DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_signature_published_state();
CREATE CONSTRAINT TRIGGER edo_published_state AFTER INSERT ON edo_outbox DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_signature_published_state();

CREATE OR REPLACE FUNCTION agreement_runtime_transition() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF current_user='poaktu_runtime' THEN
  IF OLD.first_reserved_at IS NOT NULL THEN
   IF OLD.status NOT IN ('FUNDED','READY_TO_SETTLE') OR NEW.status NOT IN ('FUNDED','READY_TO_SETTLE')
    OR (OLD.status='READY_TO_SETTLE' AND NEW.status<>'READY_TO_SETTLE')
    OR NEW.id IS DISTINCT FROM OLD.id OR NEW.deal_version IS DISTINCT FROM OLD.deal_version
    OR NEW.current_document_id IS DISTINCT FROM OLD.current_document_id OR NEW.current_document_version IS DISTINCT FROM OLD.current_document_version
    OR NEW.first_reserved_at IS DISTINCT FROM OLD.first_reserved_at OR NEW.created_at IS DISTINCT FROM OLD.created_at
   THEN RAISE EXCEPTION 'funded conditions and reserve are immutable'; END IF;
   IF NOT EXISTS(SELECT 1 FROM reserves r JOIN deal_versions v ON v.deal_id=NEW.id AND v.deal_version=NEW.deal_version JOIN accounts a ON a.id=r.account_id
    WHERE r.deal_id=NEW.id AND r.deal_version=NEW.deal_version AND r.status='ACTIVE' AND r.customer_party_id=v.customer_party_id AND r.supplier_party_id=v.supplier_party_id
    AND a.organization_id=v.customer_party_id AND r.amount_kopecks=v.amount_kopecks AND r.currency=v.currency AND r.document_id=NEW.current_document_id AND r.document_version=NEW.current_document_version)
   THEN RAISE EXCEPTION 'funded deal requires full active reserve'; END IF;
   IF NEW.status='READY_TO_SETTLE' THEN
    IF (SELECT count(DISTINCT s.organization_id) FROM document_signatures s JOIN deal_versions v ON v.deal_id=NEW.id AND v.deal_version=NEW.deal_version
     WHERE s.deal_id=NEW.id AND s.deal_version=NEW.deal_version AND s.document_id=NEW.current_document_id AND s.document_version=NEW.current_document_version
      AND s.organization_id IN(v.customer_party_id,v.supplier_party_id))<>2 OR NOT EXISTS(SELECT 1 FROM edo_outbox e WHERE e.deal_id=NEW.id AND e.deal_version=NEW.deal_version AND e.document_id=NEW.current_document_id AND e.document_version=NEW.current_document_version)
    THEN RAISE EXCEPTION 'ready deal requires two signatures and immutable EDO basis'; END IF;
   END IF;
  ELSE
   IF NEW.status NOT IN ('DRAFT','AGREED','FUNDED') THEN RAISE EXCEPTION 'unsupported runtime deal transition'; END IF;
   IF NEW.status='FUNDED' THEN
    IF OLD.status<>'AGREED' OR NEW.first_reserved_at IS NULL OR NEW.deal_version<>OLD.deal_version
     OR NOT EXISTS(SELECT 1 FROM reserves r WHERE r.deal_id=NEW.id AND r.deal_version=NEW.deal_version AND r.status='ACTIVE') THEN RAISE EXCEPTION 'funded deal requires reserve'; END IF;
   ELSIF NEW.first_reserved_at IS DISTINCT FROM OLD.first_reserved_at THEN RAISE EXCEPTION 'reserve timestamp requires funded state'; END IF;
  END IF;
 END IF;
 RETURN NEW;
END $$;
-- New and replaced functions keep the TEMP-resistant invoker search path.
DO $$
DECLARE trusted_schema text:=pg_catalog.current_schema(); guard_name text;
BEGIN
 IF trusted_schema IS NULL OR trusted_schema IN ('pg_catalog','information_schema') OR trusted_schema LIKE 'pg_temp_%' THEN RAISE EXCEPTION 'migrations require a trusted application schema'; END IF;
 FOREACH guard_name IN ARRAY ARRAY['agreement_runtime_transition','check_signature_context','check_edo_outbox_context','check_signature_published_state'] LOOP
  EXECUTE pg_catalog.format('ALTER FUNCTION %I.%I() SET search_path TO pg_catalog, %I, pg_temp',trusted_schema,guard_name,trusted_schema);
 END LOOP;
END $$;
