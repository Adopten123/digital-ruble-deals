-- A caller with TEMP privilege must not replace the tables read by a guard.
-- Explicitly put pg_temp last; omitting it would implicitly search it first.
-- Preserve SECURITY INVOKER and bind to the schema where migrations are run,
-- including isolated random schemas used by integration tests.
DO $$
DECLARE
    trusted_schema text := pg_catalog.current_schema();
    guard_name text;
BEGIN
    IF trusted_schema IS NULL OR trusted_schema IN ('pg_catalog', 'information_schema')
        OR trusted_schema LIKE 'pg_temp_%' THEN
        RAISE EXCEPTION 'migrations require a trusted application schema';
    END IF;
    FOREACH guard_name IN ARRAY ARRAY[
        'check_ledger_batch_balance', 'ledger_immutable', 'draft_fact_immutable',
        'agreement_runtime_transition', 'check_reserve_context',
        'check_reserve_published_state', 'check_account_projection',
        'check_operator_ledger_direction'
    ] LOOP
        EXECUTE pg_catalog.format(
            'ALTER FUNCTION %I.%I() SET search_path TO pg_catalog, %I, pg_temp',
            trusted_schema, guard_name, trusted_schema
        );
    END LOOP;
END $$;
