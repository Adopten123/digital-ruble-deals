-- Drafts only. This stage never changes accounts, reserves or the ledger.
CREATE TABLE deals (
    id text PRIMARY KEY,
    deal_version bigint NOT NULL DEFAULT 1 CHECK (deal_version > 0),
    row_version bigint NOT NULL DEFAULT 1 CHECK (row_version > 0),
    status text NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT','AGREED','FUNDED','READY_TO_SETTLE','DISPUTED','CANCELLATION_PENDING','REVIEW_REQUIRED','SETTLED','CANCELLED','REFUNDED')),
    first_reserved_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE deal_versions (
    deal_id text NOT NULL REFERENCES deals(id),
    deal_version bigint NOT NULL CHECK (deal_version > 0),
    customer_party_id text NOT NULL REFERENCES organizations(id),
    supplier_party_id text NOT NULL REFERENCES organizations(id),
    title text NOT NULL CHECK (char_length(title) BETWEEN 1 AND 200 AND title = btrim(title)),
    description text NOT NULL CHECK (char_length(description) BETWEEN 1 AND 5000 AND description = btrim(description)),
    amount_kopecks bigint NOT NULL CHECK (amount_kopecks > 0),
    currency text NOT NULL CHECK (currency = 'RUB'),
    deadline_at timestamptz NOT NULL,
    created_by text NOT NULL REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (deal_id,deal_version),
    CHECK (customer_party_id <> supplier_party_id)
);
ALTER TABLE deals ADD CONSTRAINT current_deal_version FOREIGN KEY (id,deal_version)
    REFERENCES deal_versions(deal_id,deal_version) DEFERRABLE INITIALLY DEFERRED;
CREATE INDEX deals_created_idx ON deals(created_at DESC,id DESC);
CREATE INDEX deal_versions_customer_idx ON deal_versions(customer_party_id,deal_id);
CREATE INDEX deal_versions_supplier_idx ON deal_versions(supplier_party_id,deal_id);

-- Only completed outcomes are inserted, in the same transaction as their facts.
-- The scoped advisory lock serializes commands before this unique constraint.
CREATE TABLE idempotency_keys (
    organization_id text NOT NULL REFERENCES organizations(id),
    operation text NOT NULL,
    resource text NOT NULL,
    key text NOT NULL CHECK (char_length(key) BETWEEN 16 AND 128),
    request_hash text NOT NULL CHECK (length(request_hash) = 64),
    status_code integer NOT NULL CHECK (status_code BETWEEN 200 AND 499),
    response_body bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (organization_id,operation,resource,key)
);
CREATE TABLE audit_events (
    id text PRIMARY KEY,
    deal_id text REFERENCES deals(id),
    deal_version bigint,
    type text NOT NULL,
    actor_user_id text NOT NULL REFERENCES users(id),
    actor_organization_id text NOT NULL REFERENCES organizations(id),
    request_id text NOT NULL,
    payload jsonb NOT NULL CHECK (jsonb_typeof(payload) = 'object'),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY (deal_id,deal_version) REFERENCES deal_versions(deal_id,deal_version),
    CHECK ((deal_id IS NULL) = (deal_version IS NULL))
);
CREATE INDEX audit_events_deal_idx ON audit_events(deal_id,created_at,id);
-- A durable domain job, not a delivered notification. A later worker will consume it.
CREATE TABLE outbox (
    id text PRIMARY KEY,
    event_id text NOT NULL UNIQUE REFERENCES audit_events(id),
    type text NOT NULL,
    deal_id text NOT NULL REFERENCES deals(id),
    payload jsonb NOT NULL CHECK (jsonb_typeof(payload) = 'object'),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    delivered_at timestamptz
);
CREATE INDEX outbox_pending_idx ON outbox(created_at,id) WHERE delivered_at IS NULL;
CREATE FUNCTION draft_fact_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'published domain facts are immutable'; END $$;
CREATE TRIGGER immutable_deal_versions BEFORE UPDATE OR DELETE ON deal_versions FOR EACH ROW EXECUTE FUNCTION draft_fact_immutable();
CREATE TRIGGER immutable_command_results BEFORE UPDATE OR DELETE ON idempotency_keys FOR EACH ROW EXECUTE FUNCTION draft_fact_immutable();
CREATE TRIGGER immutable_audit_events BEFORE UPDATE OR DELETE ON audit_events FOR EACH ROW EXECUTE FUNCTION draft_fact_immutable();
