GRANT USAGE ON SCHEMA public TO poaktu_runtime;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO poaktu_runtime;
GRANT INSERT (id, email, password_hash) ON users TO poaktu_runtime;
GRANT INSERT (id, name, kind) ON organizations TO poaktu_runtime;
GRANT INSERT (user_id, organization_id, role) ON memberships TO poaktu_runtime;
GRANT INSERT (id, organization_id) ON accounts TO poaktu_runtime;
GRANT INSERT (token_hash, user_id, expires_at) ON sessions TO poaktu_runtime;
GRANT UPDATE (revoked_at) ON sessions TO poaktu_runtime;
-- No UPDATE of balances, ledger writes, bootstrap, migrations or operator creation.
-- Extend these explicit permissions together with later features and their tests.
