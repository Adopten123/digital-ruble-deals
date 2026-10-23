package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var ErrForbidden = errors.New("organization owner required")
var ErrNotFound = errors.New("deal not found")
var ErrInvalidQuery = errors.New("invalid deal query")

type Actor struct{ UserID, OrganizationID, RequestID string }
type Party struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}
type Deal struct {
	ID                  string      `json:"id"`
	DealVersion         int64       `json:"deal_version"`
	Status              string      `json:"status"`
	CustomerPartyID     string      `json:"customer_party_id"`
	SupplierPartyID     string      `json:"supplier_party_id"`
	Title               string      `json:"title"`
	Description         string      `json:"description"`
	AmountKopecks       string      `json:"amount_kopecks"`
	Currency            string      `json:"currency"`
	DeadlineAt          time.Time   `json:"deadline_at"`
	Customer            Party       `json:"customer"`
	Supplier            Party       `json:"supplier"`
	Document            *Document   `json:"document"`
	Agreements          []Agreement `json:"agreements"`
	Signatures          []any       `json:"signatures"`
	Reserve             *Reserve    `json:"reserve"`
	PaymentID           *string     `json:"payment_id"`
	Dispute             any         `json:"dispute"`
	CancellationRequest any         `json:"cancellation_request"`
	CreatedAt           time.Time   `json:"created_at"`
	UpdatedAt           time.Time   `json:"updated_at"`
}

func (d *Deal) Normalize() {
	d.DeadlineAt, d.CreatedAt, d.UpdatedAt = d.DeadlineAt.UTC(), d.CreatedAt.UTC(), d.UpdatedAt.UTC()
	if d.Agreements == nil {
		d.Agreements = []Agreement{}
	}
	if d.Signatures == nil {
		d.Signatures = []any{}
	}
}

type Input struct {
	SupplierPartyID string `json:"supplier_party_id"`
	Title           string `json:"title"`
	Description     string `json:"description"`
	AmountKopecks   string `json:"amount_kopecks"`
	Currency        string `json:"currency"`
	DeadlineAt      string `json:"deadline_at"`
}
type Failure struct {
	Status        int
	Code, Message string
}

func (f Failure) Result(requestID string) Result {
	body, _ := json.Marshal(map[string]any{"error": map[string]string{"code": f.Code, "message": f.Message}, "request_id": requestID})
	return Result{Status: f.Status, Body: body}
}

type Result struct {
	Status int
	Body   []byte
}
type Command struct {
	Actor     Actor
	Key, Hash string
	Input     Input
	Amount    int64
	Deadline  time.Time
	Invalid   *Failure
}
type Filters struct {
	Search      string     `json:"q"`
	Status      string     `json:"status"`
	Role        string     `json:"role"`
	CreatedFrom *time.Time `json:"created_from"`
	CreatedTo   *time.Time `json:"created_to"`
	Limit       int        `json:"limit"`
}
type Query struct {
	Actor      Actor
	Filters    Filters
	BeforeTime *time.Time
	BeforeID   string
}
type Page struct {
	Items      []Deal  `json:"items"`
	NextCursor *string `json:"next_cursor"`
}
type Repository interface {
	Create(context.Context, Command) (Result, error)
	List(context.Context, Query) ([]Deal, error)
	Get(context.Context, Actor, string) (Deal, error)
}
type Service struct {
	repo  Repository
	key   []byte
	files DocumentStore
}

func New(repo Repository, key []byte, files ...DocumentStore) *Service {
	s := &Service{repo: repo, key: append([]byte(nil), key...)}
	if len(files) > 0 {
		s.files = files[0]
	}
	return s
}
func ValidKey(key string) bool {
	if len(key) < 16 || len(key) > 128 {
		return false
	}
	for _, r := range key {
		if r < 33 || r > 126 {
			return false
		}
	}
	return true
}
func badInput() *Failure {
	return &Failure{400, "INVALID_REQUEST", "Проверьте стороны, название, описание, валюту и срок сделки"}
}
func validText(value string, min, max int, multiline bool) bool {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) < min || utf8.RuneCountInString(value) > max {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) && !(multiline && (r == '\n' || r == '\r' || r == '\t')) {
			return false
		}
	}
	return true
}
func (s *Service) Create(ctx context.Context, actor Actor, key string, input Input) (Result, error) {
	if actor.UserID == "" || actor.OrganizationID == "" {
		return Result{}, ErrForbidden
	}
	if !ValidKey(key) {
		return Failure{400, "IDEMPOTENCY_KEY_REQUIRED", "Нужен Idempotency-Key длиной от 16 до 128 ASCII-символов без пробелов"}.Result(actor.RequestID), nil
	}
	input.SupplierPartyID = strings.TrimSpace(input.SupplierPartyID)
	input.Title = strings.TrimSpace(input.Title)
	input.Description = strings.TrimSpace(input.Description)
	cmd := Command{Actor: actor, Key: key, Input: input}
	if !validText(input.SupplierPartyID, 1, 128, false) || input.SupplierPartyID == actor.OrganizationID || !validText(input.Title, 1, 200, false) || !validText(input.Description, 1, 5000, true) || input.Currency != "RUB" {
		cmd.Invalid = badInput()
	}
	amount, err := strconv.ParseInt(input.AmountKopecks, 10, 64)
	if err != nil || amount <= 0 || strconv.FormatInt(amount, 10) != input.AmountKopecks {
		cmd.Invalid = &Failure{400, "INVALID_AMOUNT", "Сумма должна быть положительной канонической строкой целых копеек int64"}
	}
	cmd.Amount = amount
	deadline, err := time.Parse(time.RFC3339Nano, input.DeadlineAt)
	if err != nil || deadline.Year() < 1 || deadline.Year() > 9999 {
		cmd.Invalid = badInput()
	} else {
		// PostgreSQL timestamptz stores microseconds; persist and hash the same precision.
		cmd.Deadline = deadline.UTC().Truncate(time.Microsecond)
		cmd.Input.DeadlineAt = cmd.Deadline.Format(time.RFC3339Nano)
	}
	canonical, _ := json.Marshal(cmd.Input)
	hash := sha256.Sum256(canonical)
	cmd.Hash = hex.EncodeToString(hash[:])
	return s.repo.Create(ctx, cmd)
}

type cursor struct {
	OrganizationID string    `json:"o"`
	Filters        Filters   `json:"f"`
	Time           time.Time `json:"t"`
	ID             string    `json:"i"`
}

func (s *Service) sign(value string) string {
	h := hmac.New(sha256.New, s.key)
	h.Write([]byte("poaktu:deal-cursor:v1\x00" + value))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}
func (s *Service) List(ctx context.Context, actor Actor, filters Filters, rawCursor string) (Page, error) {
	if actor.UserID == "" || actor.OrganizationID == "" {
		return Page{}, ErrForbidden
	}
	filters.Search = strings.TrimSpace(filters.Search)
	validStatus := filters.Status == ""
	for _, status := range []string{"DRAFT", "AGREED", "FUNDED", "READY_TO_SETTLE", "DISPUTED", "CANCELLATION_PENDING", "REVIEW_REQUIRED", "SETTLED", "CANCELLED", "REFUNDED"} {
		if filters.Status == status {
			validStatus = true
		}
	}
	if !validText(filters.Search, 0, 200, false) || !validStatus || (filters.Role != "" && filters.Role != "customer" && filters.Role != "supplier") || filters.Limit < 1 || filters.Limit > 100 {
		return Page{}, ErrInvalidQuery
	}
	if filters.CreatedFrom != nil {
		normalized := filters.CreatedFrom.UTC()
		filters.CreatedFrom = &normalized
	}
	if filters.CreatedTo != nil {
		normalized := filters.CreatedTo.UTC()
		filters.CreatedTo = &normalized
	}
	if filters.CreatedFrom != nil && filters.CreatedTo != nil && filters.CreatedFrom.After(*filters.CreatedTo) {
		return Page{}, ErrInvalidQuery
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
		body, err := base64.RawURLEncoding.DecodeString(parts[0])
		if err != nil {
			return Page{}, ErrInvalidQuery
		}
		var pos cursor
		if json.Unmarshal(body, &pos) != nil || pos.OrganizationID != actor.OrganizationID || pos.ID == "" || pos.Time.IsZero() {
			return Page{}, ErrInvalidQuery
		}
		wanted, _ := json.Marshal(filters)
		actual, _ := json.Marshal(pos.Filters)
		if string(wanted) != string(actual) {
			return Page{}, ErrInvalidQuery
		}
		q.BeforeTime, q.BeforeID = &pos.Time, pos.ID
	}
	q.Filters.Limit++
	items, err := s.repo.List(ctx, q)
	if err != nil {
		return Page{}, err
	}
	if items == nil {
		items = []Deal{}
	}
	page := Page{Items: items}
	if len(items) > filters.Limit {
		page.Items = items[:filters.Limit]
		last := page.Items[len(page.Items)-1]
		body, _ := json.Marshal(cursor{OrganizationID: actor.OrganizationID, Filters: filters, Time: last.CreatedAt, ID: last.ID})
		value := base64.RawURLEncoding.EncodeToString(body)
		next := value + "." + s.sign(value)
		page.NextCursor = &next
	}
	return page, nil
}
func (s *Service) Get(ctx context.Context, actor Actor, id string) (Deal, error) {
	if actor.UserID == "" || actor.OrganizationID == "" || !validText(id, 1, 128, false) {
		return Deal{}, ErrNotFound
	}
	return s.repo.Get(ctx, actor, id)
}
