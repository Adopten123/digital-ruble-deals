GRANT USAGE ON SCHEMA public TO poaktu_runtime;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO poaktu_runtime;
GRANT INSERT (id, email, password_hash) ON users TO poaktu_runtime;
GRANT INSERT (id, name, kind) ON organizations TO poaktu_runtime;
GRANT INSERT (user_id, organization_id, role) ON memberships TO poaktu_runtime;
GRANT INSERT (id, organization_id) ON accounts TO poaktu_runtime;
GRANT INSERT (token_hash, user_id, expires_at) ON sessions TO poaktu_runtime;
GRANT UPDATE (revoked_at) ON sessions TO poaktu_runtime;
GRANT INSERT (id, created_at, updated_at) ON deals TO poaktu_runtime;
GRANT INSERT (deal_id, deal_version, customer_party_id, supplier_party_id, title, description, amount_kopecks, currency, deadline_at, created_by, created_at) ON deal_versions TO poaktu_runtime;
GRANT INSERT (organization_id, operation, resource, key, request_hash, status_code, response_body) ON idempotency_keys TO poaktu_runtime;
GRANT INSERT (id, deal_id, deal_version, type, actor_user_id, actor_organization_id, request_id, payload, created_at) ON audit_events TO poaktu_runtime;
GRANT INSERT (id, event_id, type, deal_id, payload, created_at) ON outbox TO poaktu_runtime;
-- No UPDATE of balances, ledger writes, bootstrap, migrations or operator creation.
-- Extend these explicit permissions together with later features and their tests.
