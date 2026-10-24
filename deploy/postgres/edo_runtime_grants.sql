-- Apply after the common runtime grants and migrations 001-008. Only the new
-- EDO receipt/delivery columns are mutable; source bytes and final facts are not.
GRANT SELECT ON event_inbox,edo_deliveries,edo_command_results,settlements TO poaktu_runtime;
GRANT INSERT(source,event_id,raw_body_hash,raw_body) ON event_inbox TO poaktu_runtime;
GRANT UPDATE(status,status_code,response_body,completed_at) ON event_inbox TO poaktu_runtime;
GRANT INSERT(id,event_id,deal_id,template,raw_body,automatic_basis,created_at) ON edo_deliveries TO poaktu_runtime;
GRANT UPDATE(attempts,next_attempt_at,lease_id,lease_until,http_status,response_body,completed_at) ON edo_deliveries TO poaktu_runtime;
GRANT INSERT(user_id,key,request_hash,status_code,response_body) ON edo_command_results TO poaktu_runtime;
GRANT INSERT(id,deal_id,deal_version,reserve_id,batch_id,source,event_id,document_id,document_version,document_sha256,customer_signature_id,supplier_signature_id,amount_kopecks,currency,created_at) ON settlements TO poaktu_runtime;
GRANT INSERT(actor_source) ON audit_events TO poaktu_runtime;
