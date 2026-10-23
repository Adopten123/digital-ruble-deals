-- Public credentials for the isolated local test contour only.
-- PostgreSQL's initialization user owns migrations; the HTTP server has no DDL privileges.
DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'poaktu_runtime') THEN
        CREATE ROLE poaktu_runtime LOGIN PASSWORD 'poaktu_runtime_demo_only'
            NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
    END IF;
END $$;
