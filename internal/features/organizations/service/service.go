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
	"unicode/utf8"
)

var ErrInvalidQuery = errors.New("invalid organization query")
var ErrForbidden = errors.New("organization membership required")

type CatalogItem struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	IsDemo    bool      `json:"is_demo"`
	CreatedAt time.Time `json:"-"`
}
type Query struct {
	Search     string
	Limit      int
	BeforeTime *time.Time
	BeforeID   string
}
type Catalog struct {
	Items      []CatalogItem `json:"items"`
	NextCursor *string       `json:"next_cursor"`
}
type Account struct {
	ID        string `json:"id"`
	Currency  string `json:"currency"`
	Total     string `json:"total_kopecks"`
	Reserved  string `json:"reserved_kopecks"`
	Available string `json:"available_kopecks"`
}
type Organization struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	Role string `json:"role"`
}
type OwnOrganization struct {
	Organization Organization `json:"organisation"`
	Account      Account      `json:"account"`
}
type Repository interface {
	Catalog(context.Context, Query) ([]CatalogItem, error)
	Own(context.Context, string, string) (OwnOrganization, error)
}
type Service struct {
	repo Repository
	key  []byte
}

func New(repo Repository, key []byte) *Service {
	return &Service{repo: repo, key: append([]byte(nil), key...)}
}

type cursor struct {
	Time   time.Time `json:"t"`
	ID     string    `json:"i"`
	Search string    `json:"q"`
}

func (s *Service) sign(value string) string {
	h := hmac.New(sha256.New, s.key)
	h.Write([]byte("poaktu:organization-cursor:v1\x00" + value))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}
func (s *Service) List(ctx context.Context, search, rawCursor string, limit int) (Catalog, error) {
	search = strings.TrimSpace(search)
	if !utf8.ValidString(search) || utf8.RuneCountInString(search) > 200 || limit < 1 || limit > 100 {
		return Catalog{}, ErrInvalidQuery
	}
	query := Query{Search: search, Limit: limit + 1}
	if rawCursor != "" {
		if len(rawCursor) > 2048 {
			return Catalog{}, ErrInvalidQuery
		}
		parts := strings.Split(rawCursor, ".")
		if len(parts) != 2 || !hmac.Equal([]byte(s.sign(parts[0])), []byte(parts[1])) {
			return Catalog{}, ErrInvalidQuery
		}
		body, err := base64.RawURLEncoding.DecodeString(parts[0])
		if err != nil {
			return Catalog{}, ErrInvalidQuery
		}
		var position cursor
		if json.Unmarshal(body, &position) != nil || position.Time.IsZero() || position.ID == "" || position.Search != search {
			return Catalog{}, ErrInvalidQuery
		}
		query.BeforeTime = &position.Time
		query.BeforeID = position.ID
	}
	items, err := s.repo.Catalog(ctx, query)
	if err != nil {
		return Catalog{}, err
	}
	if items == nil {
		items = []CatalogItem{}
	}
	out := Catalog{Items: items}
	if len(items) > limit {
		out.Items = items[:limit]
		last := out.Items[limit-1]
		body, _ := json.Marshal(cursor{Time: last.CreatedAt, ID: last.ID, Search: search})
		encoded := base64.RawURLEncoding.EncodeToString(body)
		next := encoded + "." + s.sign(encoded)
		out.NextCursor = &next
	}
	return out, nil
}
func (s *Service) Me(ctx context.Context, userID, organizationID string) (OwnOrganization, error) {
	if organizationID == "" {
		return OwnOrganization{}, ErrForbidden
	}
	return s.repo.Own(ctx, userID, organizationID)
}
