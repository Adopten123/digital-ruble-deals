-- Foundation only: identity, organization, sessions and a balanced demo seed.
-- Deal settlement, reserves, inbox/outbox and integrity proofs are subsequent work.
CREATE TABLE users (
    id text PRIMARY KEY,
    email text NOT NULL UNIQUE CHECK (email = lower(email)),
    password_hash text NOT NULL,
    system_role text NOT NULL DEFAULT 'USER' CHECK (system_role IN ('USER', 'DEMO_OPERATOR')),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE organizations (
    id text PRIMARY KEY,
    name text NOT NULL CHECK (char_length(name) BETWEEN 2 AND 200),
    kind text NOT NULL CHECK (kind IN ('COMPANY', 'IP')),
    is_demo boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE memberships (
    user_id text PRIMARY KEY REFERENCES users(id),
    organization_id text NOT NULL REFERENCES organizations(id),
    role text NOT NULL CHECK (role = 'ORG_OWNER'),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE accounts (
    id text PRIMARY KEY,
    organization_id text UNIQUE REFERENCES organizations(id),
    system_purpose text UNIQUE,
    currency text NOT NULL DEFAULT 'RUB' CHECK (currency = 'RUB'),
    total_kopecks bigint NOT NULL DEFAULT 0,
    reserved_kopecks bigint NOT NULL DEFAULT 0 CHECK (reserved_kopecks >= 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (num_nonnulls(organization_id, system_purpose) = 1),
    CHECK ((organization_id IS NOT NULL AND system_purpose IS NULL AND total_kopecks >= reserved_kopecks)
        OR (organization_id IS NULL AND system_purpose = 'DEMO_ISSUANCE' AND reserved_kopecks = 0))
);
CREATE TABLE sessions (
    token_hash text PRIMARY KEY CHECK (length(token_hash) = 64),
    user_id text NOT NULL REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    CHECK (expires_at > created_at)
);
CREATE INDEX sessions_user_id_idx ON sessions(user_id);
CREATE INDEX sessions_expiry_idx ON sessions(expires_at);
CREATE TABLE ledger_batches (
    id text PRIMARY KEY,
    operation_key text NOT NULL UNIQUE,
    kind text NOT NULL CHECK (kind = 'DEMO_SEED'),
    reason text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE ledger_entries (
    id text PRIMARY KEY,
    batch_id text NOT NULL REFERENCES ledger_batches(id),
    account_id text NOT NULL REFERENCES accounts(id),
    delta_kopecks bigint NOT NULL CHECK (delta_kopecks <> 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (batch_id, account_id)
);
CREATE INDEX ledger_entries_account_idx ON ledger_entries(account_id);
CREATE FUNCTION check_ledger_batch_balance() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target_id text;
BEGIN
    IF TG_TABLE_NAME = 'ledger_batches' THEN target_id := NEW.id; ELSE target_id := NEW.batch_id; END IF;
    IF (SELECT count(*) FROM ledger_entries WHERE batch_id = target_id) <> 2
       OR (SELECT COALESCE(sum(delta_kopecks::numeric), 0) FROM ledger_entries WHERE batch_id = target_id) <> 0 THEN
        RAISE EXCEPTION 'ledger batch must have exactly two balanced entries' USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER balanced_batch AFTER INSERT ON ledger_batches
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_ledger_batch_balance();
CREATE CONSTRAINT TRIGGER balanced_entries AFTER INSERT ON ledger_entries
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_ledger_batch_balance();
CREATE FUNCTION ledger_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'published ledger records are immutable'; END $$;
CREATE TRIGGER immutable_entries BEFORE UPDATE OR DELETE ON ledger_entries FOR EACH ROW EXECUTE FUNCTION ledger_immutable();
CREATE TRIGGER immutable_batches BEFORE UPDATE OR DELETE ON ledger_batches FOR EACH ROW EXECUTE FUNCTION ledger_immutable();
CREATE TABLE bootstrap_runs (
    name text PRIMARY KEY,
    completed_at timestamptz NOT NULL DEFAULT now()
);
