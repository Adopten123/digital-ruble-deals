package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	audit "github.com/Adopten123/digital-ruble-deals/internal/features/audit/service"
)

var ErrForbidden = errors.New("notification user required")
var ErrNotFound = errors.New("notification unavailable")
var ErrInvalidQuery = errors.New("invalid notification query")

type Actor struct {
	UserID, OrganizationID string
	Operator               bool
}
type Notification struct {
	ID        string      `json:"id"`
	CreatedAt time.Time   `json:"created_at"`
	ReadAt    *time.Time  `json:"read_at"`
	Event     audit.Event `json:"event"`
}
type Record struct {
	ID        string
	CreatedAt time.Time
	ReadAt    *time.Time
	Event     audit.Record
}
type Filters struct {
	Unread *bool `json:"unread"`
	Limit  int   `json:"limit"`
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
type Result struct {
	Items       []Record
	UnreadCount int64
}
type Repository interface {
	List(context.Context, Query) (Result, error)
	Read(context.Context, Actor, string) error
	Deliver(context.Context, int) (int, error)
}
type Page struct {
	Items       []Notification `json:"items"`
	NextCursor  *string        `json:"next_cursor"`
	UnreadCount int64          `json:"unread_count"`
}
type Service struct {
	repo Repository
	key  []byte
}

func New(repo Repository, key []byte) *Service {
	return &Service{repo: repo, key: append([]byte(nil), key...)}
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
	_, _ = h.Write([]byte("poaktu:notification-cursor:v1\x00" + value))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}
func (s *Service) List(ctx context.Context, actor Actor, filters Filters, rawCursor string) (Page, error) {
	if actor.UserID == "" || (!actor.Operator && actor.OrganizationID == "") {
		return Page{}, ErrForbidden
	}
	if filters.Limit < 1 || filters.Limit > 100 {
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
		raw, err := base64.RawURLEncoding.DecodeString(parts[0])
		if err != nil {
			return Page{}, ErrInvalidQuery
		}
		var pos cursor
		if json.Unmarshal(raw, &pos) != nil || pos.UserID != actor.UserID || pos.OrganizationID != actor.OrganizationID || pos.Operator != actor.Operator || pos.Before.ID == "" || pos.Before.Time.IsZero() || pos.Highwater.ID == "" || pos.Highwater.Time.IsZero() {
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
	result, err := s.repo.List(ctx, q)
	if err != nil {
		return Page{}, err
	}
	out := Page{Items: []Notification{}, UnreadCount: result.UnreadCount}
	records := result.Items
	if len(records) > filters.Limit {
		highwater := q.Highwater
		if highwater == nil {
			highwater = &Position{Time: records[0].CreatedAt, ID: records[0].ID}
		}
		last := records[filters.Limit-1]
		raw, _ := json.Marshal(cursor{UserID: actor.UserID, OrganizationID: actor.OrganizationID, Operator: actor.Operator, Filters: filters, Before: Position{Time: last.CreatedAt, ID: last.ID}, Highwater: *highwater})
		token := base64.RawURLEncoding.EncodeToString(raw)
		token += "." + s.sign(token)
		out.NextCursor = &token
		records = records[:filters.Limit]
	}
	for _, r := range records {
		var readAt *time.Time
		if r.ReadAt != nil {
			t := r.ReadAt.UTC()
			readAt = &t
		}
		out.Items = append(out.Items, Notification{ID: r.ID, CreatedAt: r.CreatedAt.UTC(), ReadAt: readAt, Event: audit.Project(r.Event)})
	}
	return out, nil
}
func (s *Service) Read(ctx context.Context, actor Actor, id string) error {
	if actor.UserID == "" {
		return ErrForbidden
	}
	if actor.Operator || actor.OrganizationID == "" || !audit.ValidID(id) {
		return ErrNotFound
	}
	return s.repo.Read(ctx, actor, id)
}
func (s *Service) DeliverOnce(ctx context.Context) (bool, error) {
	n, err := s.repo.Deliver(ctx, 50)
	return n > 0, err
}
