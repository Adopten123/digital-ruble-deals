package postgres

import (
	"github.com/jackc/pgx/v5"
	"strings"
)

type tableSpec struct {
	name    string
	columns []string
}

func table(name, columns string) tableSpec {
	return tableSpec{name: name, columns: strings.Fields(columns)}
}

// The manifest deliberately lists fields instead of SELECT * or to_jsonb(row).
// Added tables/columns are outside v1 until a new reviewed format includes them.
var manifest = []tableSpec{
	table("users", "id system_role created_at"),
	table("organizations", "id name kind is_demo created_at"),
	table("memberships", "user_id organization_id role created_at"),
	table("accounts", "id organization_id system_purpose currency total_kopecks reserved_kopecks created_at"),
	table("ledger_batches", "id operation_key kind reason created_at"),
	table("ledger_entries", "id batch_id account_id delta_kopecks created_at"),
	table("bootstrap_runs", "name completed_at"),
	table("schema_migrations", "version checksum applied_at"),
	table("deals", "id deal_version row_version status first_reserved_at created_at updated_at current_document_id current_document_version"),
	table("deal_versions", "deal_id deal_version customer_party_id supplier_party_id title description amount_kopecks currency deadline_at created_by created_at"),
	table("idempotency_keys", "organization_id operation resource key request_hash status_code response_body created_at"),
	table("audit_events", "id deal_id deal_version type actor_user_id actor_organization_id request_id payload created_at actor_source"),
	table("outbox", "id event_id type deal_id payload created_at"),
	table("documents", "id deal_id"),
	table("document_versions", "document_id document_version deal_id deal_version sha256 storage_key file_name size_bytes content_type created_by created_at"),
	table("deal_agreements", "id deal_id deal_version organization_id user_id document_id document_version document_sha256 created_at"),
	table("operator_command_results", "user_id operation resource key request_hash status_code response_body created_at"),
	table("reserves", "id deal_id deal_version account_id customer_party_id supplier_party_id document_id document_version document_sha256 amount_kopecks currency status created_at"),
	table("document_signatures", "id deal_id deal_version organization_id user_id document_id document_version document_sha256 created_at"),
	table("edo_outbox", "event_id deal_id deal_version document_id document_version document_sha256 customer_signature_id supplier_signature_id occurred_at raw_body"),
	table("deal_disputes", "id deal_id deal_version organization_id user_id reason created_at"),
	table("cancellation_requests", "id deal_id deal_version initiator_organization_id initiator_user_id reason created_at"),
	table("cancellation_decisions", "request_id organization_id user_id decision reason created_at"),
	table("event_inbox", "source event_id raw_body_hash raw_body status status_code response_body created_at completed_at"),
	table("edo_command_results", "user_id key request_hash status_code response_body created_at"),
	table("edo_deliveries", "id event_id deal_id template raw_body automatic_basis attempts next_attempt_at lease_id lease_until http_status response_body created_at completed_at"),
	table("settlements", "id deal_id deal_version reserve_id batch_id source event_id document_id document_version document_sha256 customer_signature_id supplier_signature_id amount_kopecks currency created_at"),
	table("notifications", "id event_id recipient_user_id recipient_organization_id semantic_key created_at"),
}
var exclusions = []string{
	"sessions", "users.email", "users.password_hash", "notifications.read_at", "outbox.delivered_at",
	"unlisted tables and columns", "unreferenced PDF files", "database and operating-system configuration",
}

func fieldExpression(name string) string {
	quoted := pgx.Identifier{name}.Sanitize()
	switch name {
	case "created_at", "updated_at", "first_reserved_at", "deadline_at", "completed_at", "applied_at", "occurred_at", "next_attempt_at", "lease_until":
		return `pg_catalog.to_char(` + quoted + ` AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"')`
	case "raw_body", "response_body":
		return `pg_catalog.encode(` + quoted + `,'hex')`
	default:
		return quoted
	}
}
