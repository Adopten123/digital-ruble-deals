-- A versioned sidecar seals every audit INSERT in its original transaction.
-- Published audit facts and migrations 001-009 are not rewritten.
LOCK TABLE audit_events IN SHARE ROW EXCLUSIVE MODE;

CREATE TABLE audit_chain_head (
 singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
 log_id text NOT NULL UNIQUE CHECK(log_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'),
 format_version integer NOT NULL CHECK(format_version=1),
 last_seq bigint NOT NULL CHECK(last_seq>=0),
 head_hash bytea NOT NULL CHECK(octet_length(head_hash)=32),
 legacy_count bigint NOT NULL CHECK(legacy_count>=0),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE audit_chain_entries (
 sequence bigint PRIMARY KEY CHECK(sequence>0),
 log_id text NOT NULL REFERENCES audit_chain_head(log_id),
 audit_id text NOT NULL UNIQUE REFERENCES audit_events(id),
 event_hash bytea NOT NULL CHECK(octet_length(event_hash)=32),
 previous_hash bytea NOT NULL CHECK(octet_length(previous_hash)=32),
 entry_hash bytea NOT NULL CHECK(octet_length(entry_hash)=32)
);

CREATE FUNCTION integrity_frame_v1(value text) RETURNS bytea
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS $$
 SELECT pg_catalog.int8send(pg_catalog.octet_length(pg_catalog.convert_to(value,'UTF8'))::bigint)||pg_catalog.convert_to(value,'UTF8')
$$;
CREATE FUNCTION integrity_json_depth_v1(value jsonb, depth integer) RETURNS bytea
LANGUAGE plpgsql IMMUTABLE STRICT PARALLEL SAFE AS $$
DECLARE result bytea; item record; kind text;
BEGIN
 IF depth>256 THEN RAISE EXCEPTION 'JSON nesting exceeds integrity format';END IF;
 kind:=pg_catalog.jsonb_typeof(value);
 CASE kind
 WHEN 'null' THEN RETURN pg_catalog.convert_to('n','UTF8');
 WHEN 'boolean' THEN RETURN pg_catalog.convert_to(CASE WHEN value='true'::jsonb THEN 't' ELSE 'f' END,'UTF8');
 WHEN 'string' THEN RETURN pg_catalog.convert_to('s','UTF8')||integrity_frame_v1(value#>>'{}');
 WHEN 'number' THEN RETURN pg_catalog.convert_to('d','UTF8')||integrity_frame_v1(pg_catalog.trim_scale((value#>>'{}')::numeric)::text);
 WHEN 'array' THEN
  result:=pg_catalog.convert_to('a','UTF8')||pg_catalog.int8send(pg_catalog.jsonb_array_length(value)::bigint);
  FOR item IN SELECT v FROM pg_catalog.jsonb_array_elements(value) AS x(v) LOOP
   result:=result||integrity_json_depth_v1(item.v,depth+1);
  END LOOP;
 WHEN 'object' THEN
  result:=pg_catalog.convert_to('o','UTF8')||pg_catalog.int8send((SELECT count(*) FROM pg_catalog.jsonb_each(value))::bigint);
  FOR item IN SELECT key,val FROM pg_catalog.jsonb_each(value) AS x(key,val) ORDER BY key COLLATE "C" LOOP
   result:=result||pg_catalog.convert_to('s','UTF8')||integrity_frame_v1(item.key)||integrity_json_depth_v1(item.val,depth+1);
  END LOOP;
 ELSE RAISE EXCEPTION 'unsupported integrity JSON kind';
 END CASE;
 RETURN result;
END $$;
CREATE FUNCTION integrity_json_v1(value jsonb) RETURNS bytea
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS $$ SELECT integrity_json_depth_v1(value,0) $$;
CREATE FUNCTION integrity_event_hash_v1(ev audit_events) RETURNS bytea
LANGUAGE plpgsql IMMUTABLE STRICT PARALLEL SAFE AS $$
DECLARE stamp text;
BEGIN
 IF NOT pg_catalog.isfinite(ev.created_at) OR ev.created_at<TIMESTAMPTZ '0001-01-01 00:00:00+00' OR ev.created_at>=TIMESTAMPTZ '10000-01-01 00:00:00+00' THEN
  RAISE EXCEPTION 'audit time outside integrity format';
 END IF;
 stamp:=pg_catalog.to_char(ev.created_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"');
 RETURN pg_catalog.sha256(pg_catalog.convert_to('poaktu:audit:event:v1','UTF8')||pg_catalog.decode('00','hex')||
  integrity_json_v1(pg_catalog.jsonb_build_array(ev.id,ev.deal_id,ev.deal_version,ev.type,ev.actor_user_id,ev.actor_organization_id,ev.actor_source,ev.request_id,ev.payload,stamp)));
END $$;
CREATE FUNCTION integrity_genesis_v1(log_id text) RETURNS bytea
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS $$
 SELECT pg_catalog.sha256(pg_catalog.convert_to('poaktu:audit:genesis:v1','UTF8')||pg_catalog.decode('00','hex')||integrity_frame_v1(log_id))
$$;
CREATE FUNCTION integrity_link_hash_v1(log_id text, seq bigint, previous bytea, event bytea) RETURNS bytea
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS $$
 SELECT pg_catalog.sha256(pg_catalog.convert_to('poaktu:audit:link:v1','UTF8')||pg_catalog.decode('00','hex')||integrity_frame_v1(log_id)||pg_catalog.int8send(seq)||previous||event)
$$;
CREATE FUNCTION lock_audit_chain_head() RETURNS void
LANGUAGE plpgsql SECURITY DEFINER AS $$
BEGIN
 PERFORM 1 FROM audit_chain_head WHERE singleton FOR UPDATE;
 IF NOT FOUND THEN RAISE EXCEPTION 'audit chain head unavailable';END IF;
END $$;
CREATE FUNCTION append_audit_chain_entry_v1(ev audit_events) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER AS $$
DECLARE current_head audit_chain_head; seq bigint; event_hash bytea; link bytea;
BEGIN
 SELECT * INTO STRICT current_head FROM audit_chain_head WHERE singleton FOR UPDATE;
 IF current_head.format_version<>1 OR current_head.last_seq=9223372036854775807 THEN RAISE EXCEPTION 'audit chain format or sequence unavailable';END IF;
 seq:=current_head.last_seq+1;
 event_hash:=integrity_event_hash_v1(ev);
 link:=integrity_link_hash_v1(current_head.log_id,seq,current_head.head_hash,event_hash);
 INSERT INTO audit_chain_entries(sequence,log_id,audit_id,event_hash,previous_hash,entry_hash)
 VALUES(seq,current_head.log_id,ev.id,event_hash,current_head.head_hash,link);
 UPDATE audit_chain_head SET last_seq=seq,head_hash=link WHERE singleton;
END $$;
CREATE FUNCTION append_audit_chain_trigger_v1() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER AS $$
BEGIN
 IF TG_OP<>'INSERT' OR TG_RELID<>'audit_events'::regclass THEN RAISE EXCEPTION 'invalid audit chain source';END IF;
 PERFORM append_audit_chain_entry_v1(NEW);
 RETURN NULL;
END $$;
CREATE FUNCTION audit_chain_entry_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'audit chain entries are immutable';END $$;
CREATE FUNCTION audit_chain_head_transition() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'audit chain head is immutable';END IF;
 IF (pg_catalog.to_jsonb(NEW)-'last_seq'-'head_hash') IS DISTINCT FROM (pg_catalog.to_jsonb(OLD)-'last_seq'-'head_hash')
  OR NEW.last_seq<>OLD.last_seq+1
  OR NOT EXISTS(SELECT 1 FROM audit_chain_entries e WHERE e.sequence=NEW.last_seq AND e.log_id=OLD.log_id AND e.previous_hash=OLD.head_hash AND e.entry_hash=NEW.head_hash)
 THEN RAISE EXCEPTION 'audit chain head only advances with its next entry';END IF;
 RETURN NEW;
END $$;

-- Bind every helper to the application schema, including random test schemas.
-- The definer never resolves caller TEMP tables/functions or caller search_path.
DO $$
DECLARE trusted text:=pg_catalog.current_schema(); signature text;
BEGIN
 IF trusted IS NULL OR trusted IN ('pg_catalog','information_schema') OR trusted LIKE 'pg_temp_%' THEN RAISE EXCEPTION 'migrations require a trusted application schema';END IF;
 FOREACH signature IN ARRAY ARRAY['integrity_frame_v1(text)','integrity_json_depth_v1(jsonb,integer)','integrity_json_v1(jsonb)','integrity_genesis_v1(text)','integrity_link_hash_v1(text,bigint,bytea,bytea)','lock_audit_chain_head()','append_audit_chain_trigger_v1()','audit_chain_entry_immutable()','audit_chain_head_transition()'] LOOP
  EXECUTE pg_catalog.format('ALTER FUNCTION %I.%s SET search_path TO pg_catalog,%I,pg_temp',trusted,signature,trusted);
  EXECUTE pg_catalog.format('REVOKE ALL ON FUNCTION %I.%s FROM PUBLIC',trusted,signature);
 END LOOP;
 EXECUTE pg_catalog.format('ALTER FUNCTION %I.integrity_event_hash_v1(%I.audit_events) SET search_path TO pg_catalog,%I,pg_temp',trusted,trusted,trusted);
 EXECUTE pg_catalog.format('ALTER FUNCTION %I.append_audit_chain_entry_v1(%I.audit_events) SET search_path TO pg_catalog,%I,pg_temp',trusted,trusted,trusted);
 EXECUTE pg_catalog.format('REVOKE ALL ON FUNCTION %I.integrity_event_hash_v1(%I.audit_events) FROM PUBLIC',trusted,trusted);
 EXECUTE pg_catalog.format('REVOKE ALL ON FUNCTION %I.append_audit_chain_entry_v1(%I.audit_events) FROM PUBLIC',trusted,trusted);
END $$;

INSERT INTO audit_chain_head(singleton,log_id,format_version,last_seq,head_hash,legacy_count)
 SELECT true,id,1,0,integrity_genesis_v1(id),(SELECT count(*) FROM audit_events)
 FROM (SELECT pg_catalog.gen_random_uuid()::text AS id) AS generated;
CREATE TRIGGER immutable_audit_chain_entries BEFORE UPDATE OR DELETE ON audit_chain_entries FOR EACH ROW EXECUTE FUNCTION audit_chain_entry_immutable();
CREATE TRIGGER valid_audit_chain_head BEFORE UPDATE OR DELETE ON audit_chain_head FOR EACH ROW EXECUTE FUNCTION audit_chain_head_transition();
-- This deterministic historical order is reconstruction at upgrade, not a claim
-- about original COMMIT order or historical integrity before the first anchor.
DO $$
DECLARE ev audit_events;
BEGIN
 FOR ev IN SELECT * FROM audit_events ORDER BY created_at,id COLLATE "C" LOOP
  PERFORM append_audit_chain_entry_v1(ev);
 END LOOP;
END $$;
CREATE CONSTRAINT TRIGGER audit_chain_append AFTER INSERT ON audit_events
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION append_audit_chain_trigger_v1();
