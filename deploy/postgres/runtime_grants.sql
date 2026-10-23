GRANT USAGE ON SCHEMA public TO poaktu_runtime;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO poaktu_runtime;
GRANT INSERT (id, email, password_hash) ON users TO poaktu_runtime;
GRANT INSERT (id, name, kind) ON organizations TO poaktu_runtime;
GRANT INSERT (user_id, organization_id, role) ON memberships TO poaktu_runtime;
GRANT INSERT (id, organization_id) ON accounts TO poaktu_runtime;
GRANT INSERT (token_hash, user_id, expires_at) ON sessions TO poaktu_runtime;
GRANT UPDATE (revoked_at) ON sessions TO poaktu_runtime;
GRANT INSERT (id, created_at, updated_at) ON deals TO poaktu_runtime;
GRANT UPDATE (deal_version,row_version,status,current_document_id,current_document_version,updated_at,first_reserved_at) ON deals TO poaktu_runtime;
GRANT UPDATE (total_kopecks,reserved_kopecks) ON accounts TO poaktu_runtime;
GRANT INSERT (id,deal_id,deal_version,account_id,customer_party_id,supplier_party_id,document_id,document_version,document_sha256,amount_kopecks,currency,created_at) ON reserves TO poaktu_runtime;
GRANT INSERT (user_id,operation,resource,key,request_hash,status_code,response_body) ON operator_command_results TO poaktu_runtime;
GRANT INSERT (id,operation_key,kind,reason) ON ledger_batches TO poaktu_runtime;
GRANT INSERT (id,batch_id,account_id,delta_kopecks) ON ledger_entries TO poaktu_runtime;
GRANT INSERT (id,deal_id) ON documents TO poaktu_runtime;
GRANT INSERT (document_id,document_version,deal_id,deal_version,sha256,storage_key,file_name,size_bytes,content_type,created_by,created_at) ON document_versions TO poaktu_runtime;
GRANT INSERT (id,deal_id,deal_version,organization_id,user_id,document_id,document_version,document_sha256,created_at) ON deal_agreements TO poaktu_runtime;
GRANT INSERT (id,deal_id,deal_version,organization_id,user_id,document_id,document_version,document_sha256,created_at) ON document_signatures TO poaktu_runtime;
GRANT INSERT (event_id,deal_id,deal_version,document_id,document_version,document_sha256,customer_signature_id,supplier_signature_id,occurred_at,raw_body) ON edo_outbox TO poaktu_runtime;
GRANT INSERT (deal_id, deal_version, customer_party_id, supplier_party_id, title, description, amount_kopecks, currency, deadline_at, created_by, created_at) ON deal_versions TO poaktu_runtime;
GRANT INSERT (organization_id, operation, resource, key, request_hash, status_code, response_body) ON idempotency_keys TO poaktu_runtime;
GRANT INSERT (id, deal_id, deal_version, type, actor_user_id, actor_organization_id, request_id, payload, created_at) ON audit_events TO poaktu_runtime;
GRANT INSERT (id, event_id, type, deal_id, payload, created_at) ON outbox TO poaktu_runtime;
-- Balance projections and two-entry operator movements are guarded by deferred
-- aggregate/direction constraints; no DDL, bootstrap or operator creation.
-- Extend these explicit permissions together with later features and their tests.
