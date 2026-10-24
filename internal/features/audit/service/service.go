package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	ErrForbidden      = errors.New("audit role required")
	ErrNotFound       = errors.New("audit deal not found")
	ErrInvalidQuery   = errors.New("invalid audit query")
	ErrExportTooLarge = errors.New("audit export limit exceeded")
)

const MaxExportItems = 5000

type Actor struct {
	UserID, OrganizationID string
	Operator               bool
}
type Details struct {
	AccountID             string `json:"account_id,omitempty"`
	TransactionID         string `json:"transaction_id,omitempty"`
	Operation             string `json:"operation,omitempty"`
	Status                string `json:"status,omitempty"`
	DocumentID            string `json:"document_id,omitempty"`
	DocumentVersion       *int64 `json:"document_version,omitempty"`
	DocumentSHA256        string `json:"document_sha256,omitempty"`
	Reason                string `json:"reason,omitempty"`
	Decision              string `json:"decision,omitempty"`
	CancellationRequestID string `json:"cancellation_request_id,omitempty"`
	ReserveID             string `json:"reserve_id,omitempty"`
	PaymentID             string `json:"payment_id,omitempty"`
	EventID               string `json:"event_id,omitempty"`
	AmountKopecks         string `json:"amount_kopecks,omitempty"`
	Currency              string `json:"currency,omitempty"`
}
type Event struct {
	ID                  string    `json:"id"`
	DealID              *string   `json:"deal_id"`
	DealVersion         *int64    `json:"deal_version"`
	Type                string    `json:"type"`
	ActorUserID         *string   `json:"actor_user_id"`
	ActorOrganizationID *string   `json:"actor_organization_id"`
	ActorSource         *string   `json:"actor_source"`
	RequestID           string    `json:"request_id"`
	CreatedAt           time.Time `json:"created_at"`
	Result              string    `json:"result"`
	ErrorCode           *string   `json:"error_code"`
	Details             Details   `json:"details"`
}
type Record struct {
	Event
	Payload []byte
}
type Filters struct {
	DealID string     `json:"deal_id"`
	Type   string     `json:"type"`
	From   *time.Time `json:"from"`
	To     *time.Time `json:"to"`
	Limit  int        `json:"limit"`
}
type Position struct {
	Time time.Time `json:"time"`
	ID   string    `json:"id"`
}
type Query struct {
	Actor             Actor
	Filters           Filters
	Before, Highwater *Position
}
type Repository interface {
	Read(context.Context, Query) ([]Record, error)
}
type Page struct {
	Items      []Event `json:"items"`
	NextCursor *string `json:"next_cursor"`
}
type Export struct {
	Items []Event `json:"items"`
}
type Service struct {
	repo Repository
	key  []byte
}

func New(repo Repository, key []byte) *Service {
	return &Service{repo: repo, key: append([]byte(nil), key...)}
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var typePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

func ValidID(id string) bool { return idPattern.MatchString(id) }
func validate(actor Actor, filters Filters) (Filters, error) {
	if actor.UserID == "" || (!actor.Operator && actor.OrganizationID == "") {
		return filters, ErrForbidden
	}
	if (!actor.Operator && filters.DealID == "") || (filters.DealID != "" && !ValidID(filters.DealID)) || (filters.Type != "" && !typePattern.MatchString(filters.Type)) || filters.Limit < 1 || filters.Limit > 100 {
		return filters, ErrInvalidQuery
	}
	for _, p := range []**time.Time{&filters.From, &filters.To} {
		if *p != nil {
			t := (*p).UTC()
			if t.Year() < 1 || t.Year() > 9999 {
				return filters, ErrInvalidQuery
			}
			*p = &t
		}
	}
	if filters.From != nil && filters.To != nil && filters.From.After(*filters.To) {
		return filters, ErrInvalidQuery
	}
	return filters, nil
}

type cursor struct {
	UserID         string   `json:"u"`
	OrganizationID string   `json:"o"`
	Operator       bool     `json:"p"`
	Filters        Filters  `json:"f"`
	Before         Position `json:"b"`
	Highwater      Position `json:"h"`
}

func (s *Service) sign(value string) string {
	h := hmac.New(sha256.New, s.key)
	_, _ = h.Write([]byte("poaktu:audit-cursor:v1\x00" + value))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}
func (s *Service) List(ctx context.Context, actor Actor, filters Filters, rawCursor string) (Page, error) {
	filters, err := validate(actor, filters)
	if err != nil {
		return Page{}, err
	}
	q := Query{Actor: actor, Filters: filters}
	if rawCursor != "" {
		if len(rawCursor) > 4096 {
			return Page{}, ErrInvalidQuery
		}
		parts := strings.Split(rawCursor, ".")
		if len(parts) != 2 || !hmac.Equal([]byte(s.sign(parts[0])), []byte(parts[1])) {
			return Page{}, ErrInvalidQuery
		}
		body, e := base64.RawURLEncoding.DecodeString(parts[0])
		if e != nil {
			return Page{}, ErrInvalidQuery
		}
		var pos cursor
		if json.Unmarshal(body, &pos) != nil || pos.UserID != actor.UserID || pos.OrganizationID != actor.OrganizationID || pos.Operator != actor.Operator || pos.Before.ID == "" || pos.Before.Time.IsZero() || pos.Highwater.ID == "" || pos.Highwater.Time.IsZero() {
			return Page{}, ErrInvalidQuery
		}
		wanted, _ := json.Marshal(filters)
		actual, _ := json.Marshal(pos.Filters)
		if string(wanted) != string(actual) {
			return Page{}, ErrInvalidQuery
		}
		q.Before, q.Highwater = &pos.Before, &pos.Highwater
	}
	q.Filters.Limit++
	records, err := s.repo.Read(ctx, q)
	if err != nil {
		return Page{}, err
	}
	out := Page{Items: []Event{}}
	if len(records) > filters.Limit {
		highwater := q.Highwater
		if highwater == nil {
			highwater = &Position{Time: records[0].CreatedAt, ID: records[0].ID}
		}
		last := records[filters.Limit-1]
		body, _ := json.Marshal(cursor{UserID: actor.UserID, OrganizationID: actor.OrganizationID, Operator: actor.Operator, Filters: filters, Before: Position{Time: last.CreatedAt, ID: last.ID}, Highwater: *highwater})
		encoded := base64.RawURLEncoding.EncodeToString(body)
		encoded += "." + s.sign(encoded)
		out.NextCursor = &encoded
		records = records[:filters.Limit]
	}
	for _, record := range records {
		out.Items = append(out.Items, Project(record))
	}
	return out, nil
}
func (s *Service) Export(ctx context.Context, actor Actor, filters Filters) (Export, error) {
	filters.Limit = 20
	filters, err := validate(actor, filters)
	if err != nil {
		return Export{}, err
	}
	filters.Limit = MaxExportItems + 1
	records, err := s.repo.Read(ctx, Query{Actor: actor, Filters: filters})
	if err != nil {
		return Export{}, err
	}
	if len(records) > MaxExportItems {
		return Export{}, ErrExportTooLarge
	}
	out := Export{Items: []Event{}}
	for _, record := range records {
		out.Items = append(out.Items, Project(record))
	}
	return out, nil
}
func number(value *int64) string {
	if value == nil {
		return ""
	}
	return strconv.FormatInt(*value, 10)
}
