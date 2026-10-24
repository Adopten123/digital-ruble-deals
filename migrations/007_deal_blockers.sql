-- Blocking facts are append-only; agreeing to cancellation is a second party's
-- immutable decision, not a second click by the requester.
CREATE TABLE deal_disputes (
 id text PRIMARY KEY,deal_id text NOT NULL UNIQUE,deal_version bigint NOT NULL,
 organization_id text NOT NULL REFERENCES organizations(id),user_id text NOT NULL REFERENCES users(id),
 reason text NOT NULL CHECK(char_length(reason) BETWEEN 1 AND 2000 AND reason=btrim(reason)),created_at timestamptz NOT NULL,
 FOREIGN KEY(deal_id,deal_version) REFERENCES deal_versions(deal_id,deal_version)
);
CREATE TABLE cancellation_requests (
 id text PRIMARY KEY,deal_id text NOT NULL,deal_version bigint NOT NULL,
 initiator_organization_id text NOT NULL REFERENCES organizations(id),initiator_user_id text NOT NULL REFERENCES users(id),
 reason text NOT NULL CHECK(char_length(reason) BETWEEN 1 AND 2000 AND reason=btrim(reason)),created_at timestamptz NOT NULL,
 FOREIGN KEY(deal_id,deal_version) REFERENCES deal_versions(deal_id,deal_version)
);
CREATE INDEX cancellation_requests_deal ON cancellation_requests(deal_id,created_at,id);
CREATE TABLE cancellation_decisions (
 request_id text PRIMARY KEY REFERENCES cancellation_requests(id),organization_id text NOT NULL REFERENCES organizations(id),
 user_id text NOT NULL REFERENCES users(id),decision text NOT NULL CHECK(decision IN ('ACCEPTED','REJECTED')),
 reason text,created_at timestamptz NOT NULL,
 CHECK((decision='ACCEPTED' AND reason IS NULL) OR (decision='REJECTED' AND char_length(reason) BETWEEN 1 AND 2000 AND reason=btrim(reason) AND reason IS NOT NULL))
);
CREATE TRIGGER immutable_disputes BEFORE UPDATE OR DELETE ON deal_disputes FOR EACH ROW EXECUTE FUNCTION draft_fact_immutable();
CREATE TRIGGER immutable_cancellation_requests BEFORE UPDATE OR DELETE ON cancellation_requests FOR EACH ROW EXECUTE FUNCTION draft_fact_immutable();
CREATE TRIGGER immutable_cancellation_decisions BEFORE UPDATE OR DELETE ON cancellation_decisions FOR EACH ROW EXECUTE FUNCTION draft_fact_immutable();

CREATE FUNCTION check_blocker_context() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE actor_org text; actor_user text;
BEGIN
 PERFORM 1 FROM deals WHERE id=NEW.deal_id FOR UPDATE;
 IF TG_TABLE_NAME='deal_disputes' THEN actor_org:=NEW.organization_id;actor_user:=NEW.user_id;
 ELSE actor_org:=NEW.initiator_organization_id;actor_user:=NEW.initiator_user_id;END IF;
 IF NOT EXISTS(SELECT 1 FROM deals d JOIN deal_versions v ON v.deal_id=d.id AND v.deal_version=d.deal_version
  WHERE d.id=NEW.deal_id AND d.deal_version=NEW.deal_version AND d.status NOT IN ('SETTLED','CANCELLED','REFUNDED')
  AND actor_org IN(v.customer_party_id,v.supplier_party_id)
  AND EXISTS(SELECT 1 FROM memberships m JOIN users u ON u.id=m.user_id WHERE m.user_id=actor_user AND m.organization_id=actor_org AND m.role='ORG_OWNER' AND u.system_role='USER')
  AND (TG_TABLE_NAME='cancellation_requests' OR d.status IN ('AGREED','FUNDED','READY_TO_SETTLE','DISPUTED','CANCELLATION_PENDING','REVIEW_REQUIRED') OR v.deadline_at<=clock_timestamp()))
 THEN RAISE EXCEPTION 'blocking action must match its participant and current nonterminal deal' USING ERRCODE='23514';END IF;
 IF TG_TABLE_NAME='cancellation_requests' AND EXISTS(SELECT 1 FROM cancellation_requests r WHERE r.deal_id=NEW.deal_id AND NOT EXISTS(SELECT 1 FROM cancellation_decisions c WHERE c.request_id=r.id))
 THEN RAISE EXCEPTION 'only one pending cancellation is allowed' USING ERRCODE='23514';END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER valid_dispute_context BEFORE INSERT ON deal_disputes FOR EACH ROW EXECUTE FUNCTION check_blocker_context();
CREATE TRIGGER valid_cancellation_request_context BEFORE INSERT ON cancellation_requests FOR EACH ROW EXECUTE FUNCTION check_blocker_context();

CREATE FUNCTION check_cancellation_decision_context() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target text;
BEGIN
 SELECT deal_id INTO target FROM cancellation_requests WHERE id=NEW.request_id;
 PERFORM 1 FROM deals WHERE id=target FOR UPDATE;
 IF NOT EXISTS(SELECT 1 FROM cancellation_requests r JOIN deals d ON d.id=r.deal_id
  JOIN deal_versions v ON v.deal_id=d.id AND v.deal_version=d.deal_version
  WHERE r.id=NEW.request_id AND r.deal_version=d.deal_version AND d.status NOT IN ('SETTLED','CANCELLED','REFUNDED')
  AND NEW.organization_id<>r.initiator_organization_id AND NEW.organization_id IN(v.customer_party_id,v.supplier_party_id)
  AND EXISTS(SELECT 1 FROM memberships m JOIN users u ON u.id=m.user_id WHERE m.user_id=NEW.user_id AND m.organization_id=NEW.organization_id AND m.role='ORG_OWNER' AND u.system_role='USER'))
 THEN RAISE EXCEPTION 'cancellation requires a decision by the other current organisation' USING ERRCODE='23514';END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER valid_cancellation_decision_context BEFORE INSERT ON cancellation_decisions FOR EACH ROW EXECUTE FUNCTION check_cancellation_decision_context();

-- A forged ordinary status must never make an existing blocking fact vanish.
-- These checks also protect direct writes by the restricted runtime role.
CREATE FUNCTION check_unblocked_domain_fact() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS(SELECT 1 FROM deals d WHERE d.id=NEW.deal_id AND (d.status IN ('SETTLED','CANCELLED','REFUNDED')
  OR EXISTS(SELECT 1 FROM deal_disputes x WHERE x.deal_id=d.id)
  OR EXISTS(SELECT 1 FROM cancellation_requests r WHERE r.deal_id=d.id AND NOT EXISTS(SELECT 1 FROM cancellation_decisions c WHERE c.request_id=r.id))
  OR EXISTS(SELECT 1 FROM cancellation_requests r JOIN cancellation_decisions c ON c.request_id=r.id WHERE r.deal_id=d.id AND c.decision='REJECTED')))
 THEN RAISE EXCEPTION 'blocking facts prohibit new ordinary domain facts' USING ERRCODE='23514';END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER unblocked_deal_version BEFORE INSERT ON deal_versions FOR EACH ROW EXECUTE FUNCTION check_unblocked_domain_fact();
CREATE TRIGGER unblocked_document_version BEFORE INSERT ON document_versions FOR EACH ROW EXECUTE FUNCTION check_unblocked_domain_fact();
CREATE TRIGGER unblocked_agreement BEFORE INSERT ON deal_agreements FOR EACH ROW EXECUTE FUNCTION check_unblocked_domain_fact();
CREATE TRIGGER unblocked_reserve BEFORE INSERT ON reserves FOR EACH ROW EXECUTE FUNCTION check_unblocked_domain_fact();
CREATE TRIGGER unblocked_signature BEFORE INSERT ON document_signatures FOR EACH ROW EXECUTE FUNCTION check_unblocked_domain_fact();

-- Reserve identity/context is still immutable. Only the lifecycle may move
-- once from ACTIVE to its authorized terminal outcome.
ALTER TABLE reserves DROP CONSTRAINT reserves_status_check;
ALTER TABLE reserves ADD CONSTRAINT reserve_status CHECK(status IN ('ACTIVE','RELEASED'));
DROP TRIGGER immutable_reserves ON reserves;
CREATE FUNCTION reserve_status_transition() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'published reserve cannot be deleted'; END IF;
 IF (to_jsonb(NEW)-'status') IS DISTINCT FROM (to_jsonb(OLD)-'status') OR OLD.status<>'ACTIVE' OR NEW.status<>'RELEASED'
 THEN RAISE EXCEPTION 'reserve context is immutable and lifecycle is monotonic' USING ERRCODE='23514';END IF;
 IF NOT EXISTS(SELECT 1 FROM cancellation_requests r JOIN cancellation_decisions c ON c.request_id=r.id
  JOIN deals d ON d.id=r.deal_id WHERE r.deal_id=NEW.deal_id AND r.deal_version=NEW.deal_version AND c.decision='ACCEPTED'
  AND d.status NOT IN ('SETTLED','CANCELLED','REFUNDED') AND c.organization_id<>r.initiator_organization_id)
 THEN RAISE EXCEPTION 'reserve release requires a second-party cancellation decision' USING ERRCODE='23514';END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER immutable_reserves BEFORE UPDATE OR DELETE ON reserves FOR EACH ROW EXECUTE FUNCTION reserve_status_transition();
CREATE FUNCTION check_reserve_resolution_published() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.status='RELEASED' AND EXISTS(SELECT 1 FROM deals d WHERE d.id=NEW.deal_id AND d.deal_version=NEW.deal_version AND d.status='REFUNDED') THEN RETURN NULL; END IF;
 RAISE EXCEPTION 'reserve release and refunded outcome must publish together' USING ERRCODE='23514';
END $$;
CREATE CONSTRAINT TRIGGER reserve_resolution_published AFTER UPDATE ON reserves DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_reserve_resolution_published();
CREATE CONSTRAINT TRIGGER reserve_resolution_projection AFTER UPDATE ON reserves DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_account_projection();

CREATE FUNCTION check_blocker_published_state() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target text; state text; expected text; has_accepted boolean; has_reserve boolean; released boolean;
BEGIN
 IF TG_TABLE_NAME='cancellation_decisions' THEN SELECT deal_id INTO target FROM cancellation_requests WHERE id=NEW.request_id; ELSE target:=NEW.deal_id;END IF;
 SELECT d.status,
  CASE WHEN v.deadline_at<=clock_timestamp() OR EXISTS(SELECT 1 FROM cancellation_requests r JOIN cancellation_decisions c ON c.request_id=r.id WHERE r.deal_id=d.id AND c.decision='REJECTED') THEN 'REVIEW_REQUIRED'
   WHEN EXISTS(SELECT 1 FROM cancellation_requests r WHERE r.deal_id=d.id AND NOT EXISTS(SELECT 1 FROM cancellation_decisions c WHERE c.request_id=r.id)) THEN 'CANCELLATION_PENDING'
   WHEN EXISTS(SELECT 1 FROM deal_disputes x WHERE x.deal_id=d.id) THEN 'DISPUTED' ELSE NULL END,
  EXISTS(SELECT 1 FROM cancellation_requests r JOIN cancellation_decisions c ON c.request_id=r.id WHERE r.deal_id=d.id AND r.deal_version=d.deal_version AND c.decision='ACCEPTED'),
  EXISTS(SELECT 1 FROM reserves r WHERE r.deal_id=d.id),EXISTS(SELECT 1 FROM reserves r WHERE r.deal_id=d.id AND r.status='RELEASED')
 INTO state,expected,has_accepted,has_reserve,released FROM deals d JOIN deal_versions v ON v.deal_id=d.id AND v.deal_version=d.deal_version WHERE d.id=target;
 IF (state='CANCELLED' AND has_accepted AND NOT has_reserve) OR (state='REFUNDED' AND has_accepted AND released) OR (state NOT IN ('SETTLED','CANCELLED','REFUNDED') AND state=expected) THEN RETURN NULL; END IF;
 RAISE EXCEPTION 'blocking facts and deal outcome must publish together' USING ERRCODE='23514';
END $$;
CREATE CONSTRAINT TRIGGER dispute_published_state AFTER INSERT ON deal_disputes DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_blocker_published_state();
CREATE CONSTRAINT TRIGGER cancellation_request_published_state AFTER INSERT ON cancellation_requests DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_blocker_published_state();
CREATE CONSTRAINT TRIGGER cancellation_decision_published_state AFTER INSERT ON cancellation_decisions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_blocker_published_state();

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
 FOREACH guard_name IN ARRAY ARRAY['check_blocker_context','check_cancellation_decision_context','check_unblocked_domain_fact','reserve_status_transition','check_reserve_resolution_published','check_blocker_published_state','agreement_runtime_transition'] LOOP
  EXECUTE pg_catalog.format('ALTER FUNCTION %I.%I() SET search_path TO pg_catalog, %I, pg_temp',trusted_schema,guard_name,trusted_schema);
 END LOOP;
END $$;
