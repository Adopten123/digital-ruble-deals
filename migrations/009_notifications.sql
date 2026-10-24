-- Delivery and read state for the application inbox. No money or old fact changes.
CREATE TABLE notifications (
 id text PRIMARY KEY,
 event_id text NOT NULL REFERENCES audit_events(id),
 recipient_user_id text NOT NULL REFERENCES users(id),
 recipient_organization_id text NOT NULL REFERENCES organizations(id),
 semantic_key text NOT NULL CHECK (char_length(semantic_key) BETWEEN 1 AND 256),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 read_at timestamptz,
 UNIQUE(event_id,recipient_user_id),
 UNIQUE(recipient_user_id,semantic_key),
 CHECK(read_at IS NULL OR read_at>=created_at)
);
CREATE INDEX notifications_recipient_idx ON notifications(recipient_user_id,created_at DESC,id DESC);
CREATE INDEX notifications_unread_idx ON notifications(recipient_user_id,created_at DESC,id DESC) WHERE read_at IS NULL;
CREATE FUNCTION notification_state_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'delivered notifications cannot be deleted'; END IF;
 IF (to_jsonb(NEW)-'read_at') IS DISTINCT FROM (to_jsonb(OLD)-'read_at') OR (OLD.read_at IS NOT NULL AND NEW.read_at IS DISTINCT FROM OLD.read_at) THEN
  RAISE EXCEPTION 'notification identity and first read time are immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER protect_notification_state BEFORE UPDATE OR DELETE ON notifications FOR EACH ROW EXECUTE FUNCTION notification_state_guard();
CREATE FUNCTION notification_outbox_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'outbox facts cannot be deleted'; END IF;
 IF (to_jsonb(NEW)-'delivered_at') IS DISTINCT FROM (to_jsonb(OLD)-'delivered_at') OR (OLD.delivered_at IS NOT NULL AND NEW.delivered_at IS DISTINCT FROM OLD.delivered_at) OR (NEW.delivered_at IS NOT NULL AND NEW.delivered_at<NEW.created_at) THEN
  RAISE EXCEPTION 'outbox facts and first notification processing time are immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER protect_notification_outbox BEFORE UPDATE OR DELETE ON outbox FOR EACH ROW EXECUTE FUNCTION notification_outbox_guard();
-- As with earlier guards, temporary tables/functions cannot shadow trusted objects.
DO $$
DECLARE trusted_schema text := current_schema();
BEGIN
 EXECUTE format('ALTER FUNCTION %I.notification_state_guard() SET search_path = pg_catalog, %I, pg_temp',trusted_schema,trusted_schema);
 EXECUTE format('ALTER FUNCTION %I.notification_outbox_guard() SET search_path = pg_catalog, %I, pg_temp',trusted_schema,trusted_schema);
END $$;
