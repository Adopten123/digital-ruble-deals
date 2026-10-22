package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	deals "github.com/Adopten123/digital-ruble-deals/internal/features/deals/service"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var ErrForbidden = errors.New("demo operator required")
var ErrNotFound = errors.New("account unavailable")
var ErrInvalidQuery = errors.New("invalid account query")

type Actor struct{ UserID, RequestID string }
type Account struct {
	ID           string      `json:"id"`
	Organisation deals.Party `json:"organisation"`
	Currency     string      `json:"currency"`
	Total        string      `json:"total_kopecks"`
	Reserved     string      `json:"reserved_kopecks"`
	Available    string      `json:"available_kopecks"`
	CreatedAt    time.Time   `json:"-"`
}
type Page struct {
	Items      []Account `json:"items"`
	NextCursor *string   `json:"next_cursor"`
}
type Query struct {
	Actor      Actor
	Search     string
	Limit      int
	BeforeTime *time.Time
	BeforeID   string
}
type Input struct {
	AmountKopecks string `json:"amount_kopecks"`
	Reason        string `json:"reason"`
}
type Command struct {
	Actor                    Actor
	ID, Key, Hash, Operation string
	Input                    Input
	Amount                   int64
	Invalid                  *deals.Failure
}
type Repository interface {
	List(context.Context, Query) ([]Account, error)
	Move(context.Context, Command) (deals.Result, error)
}
type Service struct {
	repo Repository
	key  []byte
}

func New(repo Repository, key []byte) *Service {
	return &Service{repo: repo, key: append([]byte(nil), key...)}
}
func validText(s string, max int) bool {
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > max {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func (s *Service) Move(ctx context.Context, a Actor, id, key, operation string, in Input) (deals.Result, error) {
	if a.UserID == "" {
		return deals.Result{}, ErrForbidden
	}
	if !validText(id, 128) || id == "" {
		return deals.Result{}, ErrNotFound
	}
	if !deals.ValidKey(key) {
		return deals.Failure{Status: 400, Code: "IDEMPOTENCY_KEY_REQUIRED", Message: "Нужен корректный Idempotency-Key"}.Result(a.RequestID), nil
	}
	if operation != "DEMO_CREDIT" && operation != "DEMO_DEBIT" {
		return deals.Result{}, errors.New("invalid monetary operation")
	}
	in.Reason = strings.TrimSpace(in.Reason)
	cmd := Command{Actor: a, ID: id, Key: key, Operation: operation, Input: in}
	validReason := utf8.ValidString(in.Reason) && utf8.RuneCountInString(in.Reason) <= 2000 && in.Reason != ""
	for _, r := range in.Reason {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			validReason = false
		}
	}
	if !validReason {
		cmd.Invalid = &deals.Failure{Status: 400, Code: "INVALID_REQUEST", Message: "Нужна причина длиной 1–2000 символов"}
	}
	n, e := strconv.ParseInt(in.AmountKopecks, 10, 64)
	if e != nil || n <= 0 || strconv.FormatInt(n, 10) != in.AmountKopecks {
		cmd.Invalid = &deals.Failure{Status: 400, Code: "INVALID_AMOUNT", Message: "Нужна положительная каноническая сумма int64 в копейках"}
	}
	cmd.Amount = n
	b, _ := json.Marshal(in)
	h := sha256.Sum256(b)
	cmd.Hash = hex.EncodeToString(h[:])
	return s.repo.Move(ctx, cmd)
}

type cursor struct {
	UserID, Search, ID string
	Limit              int
	Time               time.Time
}

func (s *Service) sign(v string) string {
	h := hmac.New(sha256.New, s.key)
	h.Write([]byte("poaktu:operator-account-cursor:v1\x00" + v))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}
func (s *Service) List(ctx context.Context, a Actor, q, raw string, limit int) (Page, error) {
	if a.UserID == "" {
		return Page{}, ErrForbidden
	}
	q = strings.TrimSpace(q)
	if !validText(q, 200) || limit < 1 || limit > 100 {
		return Page{}, ErrInvalidQuery
	}
	query := Query{Actor: a, Search: q, Limit: limit + 1}
	if raw != "" {
		if len(raw) > 4096 {
			return Page{}, ErrInvalidQuery
		}
		parts := strings.Split(raw, ".")
		if len(parts) != 2 || !hmac.Equal([]byte(s.sign(parts[0])), []byte(parts[1])) {
			return Page{}, ErrInvalidQuery
		}
		b, e := base64.RawURLEncoding.DecodeString(parts[0])
		if e != nil {
			return Page{}, ErrInvalidQuery
		}
		var c cursor
		if json.Unmarshal(b, &c) != nil || c.UserID != a.UserID || c.Search != q || c.Limit != limit || c.ID == "" || c.Time.IsZero() {
			return Page{}, ErrInvalidQuery
		}
		query.BeforeTime = &c.Time
		query.BeforeID = c.ID
	}
	items, e := s.repo.List(ctx, query)
	if e != nil {
		return Page{}, e
	}
	if items == nil {
		items = []Account{}
	}
	out := Page{Items: items}
	if len(items) > limit {
		out.Items = items[:limit]
		last := out.Items[limit-1]
		b, _ := json.Marshal(cursor{UserID: a.UserID, Search: q, ID: last.ID, Limit: limit, Time: last.CreatedAt})
		v := base64.RawURLEncoding.EncodeToString(b)
		next := v + "." + s.sign(v)
		out.NextCursor = &next
	}
	return out, nil
}
