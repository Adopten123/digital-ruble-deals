-- Immutable PDF versions and explicit agreement facts; no monetary writes.
ALTER TABLE deals ADD COLUMN current_document_id text;
ALTER TABLE deals ADD COLUMN current_document_version bigint;
CREATE TABLE documents (
    id text PRIMARY KEY,
    deal_id text NOT NULL UNIQUE REFERENCES deals(id),
    UNIQUE(id,deal_id)
);
CREATE TABLE document_versions (
    document_id text NOT NULL,
    document_version bigint NOT NULL CHECK (document_version > 0),
    deal_id text NOT NULL,
    deal_version bigint NOT NULL,
    sha256 text NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    storage_key text NOT NULL UNIQUE,
    file_name text NOT NULL CHECK (char_length(file_name) BETWEEN 1 AND 200),
    size_bytes bigint NOT NULL CHECK (size_bytes BETWEEN 1 AND 5242880),
    content_type text NOT NULL CHECK (content_type='application/pdf'),
    created_by text NOT NULL REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY(document_id,document_version),
    UNIQUE(document_id,document_version,deal_id,deal_version),
    UNIQUE(document_id,document_version,deal_id,deal_version,sha256),
    FOREIGN KEY(document_id,deal_id) REFERENCES documents(id,deal_id),
    FOREIGN KEY(deal_id,deal_version) REFERENCES deal_versions(deal_id,deal_version)
);
ALTER TABLE deals ADD CONSTRAINT active_document_version FOREIGN KEY(current_document_id,current_document_version) REFERENCES document_versions(document_id,document_version) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE deals ADD CONSTRAINT active_document_context FOREIGN KEY(current_document_id,current_document_version,id,deal_version) REFERENCES document_versions(document_id,document_version,deal_id,deal_version) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE deals ADD CONSTRAINT active_document_pair CHECK ((current_document_id IS NULL)=(current_document_version IS NULL));
CREATE TABLE deal_agreements (
    id text PRIMARY KEY,
    deal_id text NOT NULL,
    deal_version bigint NOT NULL,
    organization_id text NOT NULL REFERENCES organizations(id),
    user_id text NOT NULL REFERENCES users(id),
    document_id text NOT NULL,
    document_version bigint NOT NULL,
    document_sha256 text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE(deal_id,deal_version,organization_id),
    FOREIGN KEY(document_id,document_version,deal_id,deal_version,document_sha256) REFERENCES document_versions(document_id,document_version,deal_id,deal_version,sha256)
);
CREATE TRIGGER immutable_documents BEFORE UPDATE OR DELETE ON documents FOR EACH ROW EXECUTE FUNCTION draft_fact_immutable();
CREATE TRIGGER immutable_document_versions BEFORE UPDATE OR DELETE ON document_versions FOR EACH ROW EXECUTE FUNCTION draft_fact_immutable();
CREATE TRIGGER immutable_agreements BEFORE UPDATE OR DELETE ON deal_agreements FOR EACH ROW EXECUTE FUNCTION draft_fact_immutable();
-- A column grant on status must not expose the unimplemented monetary phases.
CREATE FUNCTION agreement_runtime_transition() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF current_user = 'poaktu_runtime' AND (OLD.first_reserved_at IS NOT NULL OR NEW.status NOT IN ('DRAFT','AGREED')) THEN
        RAISE EXCEPTION 'runtime agreement commands cannot enter monetary phases';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER restrict_runtime_agreement_transition BEFORE UPDATE ON deals FOR EACH ROW EXECUTE FUNCTION agreement_runtime_transition();
