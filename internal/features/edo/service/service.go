package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	deals "github.com/Adopten123/digital-ruble-deals/internal/features/deals/service"
)

const Source = "edo-emulator"
const KeyID = "demo-v1"
const MaxBody = 64 << 10

var ErrForbidden = errors.New("demo operator required")
var ErrNotFound = errors.New("EDO event unavailable")

type Headers struct{ KeyID, Timestamp, Signature string }
type Actor struct{ UserID, RequestID string }
type Input struct {
	DealID        string  `json:"deal_id"`
	Template      string  `json:"template"`
	ReplayEventID *string `json:"replay_event_id,omitempty"`
}
type Command struct {
	Actor     Actor
	Key, Hash string
	Input     Input
	Invalid   *deals.Failure
}
type Event struct {
	SchemaVersion       int       `json:"schema_version"`
	EventID             string    `json:"event_id"`
	Source              string    `json:"source"`
	EventType           string    `json:"event_type"`
	DealID              string    `json:"deal_id"`
	DealVersion         int64     `json:"deal_version"`
	DocumentID          string    `json:"document_id"`
	DocumentVersion     int64     `json:"document_version"`
	DocumentSHA256      string    `json:"document_sha256"`
	CustomerPartyID     string    `json:"customer_party_id"`
	SupplierPartyID     string    `json:"supplier_party_id"`
	AmountKopecks       string    `json:"amount_kopecks"`
	Currency            string    `json:"currency"`
	SignedByCustomer    bool      `json:"signed_by_customer"`
	SignedBySupplier    bool      `json:"signed_by_supplier"`
	CustomerSignatureID *string   `json:"customer_signature_id"`
	SupplierSignatureID *string   `json:"supplier_signature_id"`
	OccurredAt          time.Time `json:"occurred_at"`
}
type Envelope struct {
	Event           Event
	Raw             []byte
	Hash, RequestID string
}
type Delivery struct {
	JobID       string          `json:"job_id"`
	EventID     string          `json:"event_id"`
	DealID      string          `json:"deal_id"`
	Template    string          `json:"template"`
	Status      string          `json:"status"`
	Attempts    int             `json:"attempts"`
	HTTPStatus  *int            `json:"http_status"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   *string         `json:"error_code"`
	CreatedAt   time.Time       `json:"created_at"`
	CompletedAt *time.Time      `json:"completed_at"`
	Raw         []byte          `json:"-"`
	LeaseID     string          `json:"-"`
}
type Repository interface {
	Accept(context.Context, Envelope) (*deals.Result, error)
	Process(context.Context, Envelope) (deals.Result, error)
	Queue(context.Context, Command) (deals.Result, error)
	Get(context.Context, Actor, string) (Delivery, error)
	Claim(context.Context) (*Delivery, error)
	Complete(context.Context, Delivery, int, []byte, bool) error
}
type Service struct {
	repo Repository
	key  []byte
}

func New(repo Repository, appKey []byte) *Service {
	h := hmac.New(sha256.New, appKey)
	h.Write([]byte("poaktu:edo-source-key:v1\x00"))
	return &Service{repo: repo, key: h.Sum(nil)}
}
func (s *Service) Sign(raw []byte, now time.Time) Headers {
	timestamp := strconv.FormatInt(now.Unix(), 10)
	h := hmac.New(sha256.New, s.key)
	h.Write([]byte(timestamp + "\n"))
	h.Write(raw)
	return Headers{KeyID: KeyID, Timestamp: timestamp, Signature: "sha256=" + hex.EncodeToString(h.Sum(nil))}
}
func (s *Service) Receive(ctx context.Context, headers Headers, raw []byte, requestID string) (deals.Result, error) {
	fail := func(status int, code, message string) (deals.Result, error) {
		return (deals.Failure{Status: status, Code: code, Message: message}).Result(requestID), nil
	}
	if len(raw) > MaxBody {
		return fail(413, "PAYLOAD_TOO_LARGE", "Превышен размер события ЭДО")
	}
	stamp, err := strconv.ParseInt(headers.Timestamp, 10, 64)
	now := time.Now().Unix()
	if err != nil || strconv.FormatInt(stamp, 10) != headers.Timestamp || stamp < now-300 || stamp > now+300 || headers.KeyID != KeyID || len(headers.Signature) != 71 || !strings.HasPrefix(headers.Signature, "sha256=") {
		return fail(403, "EDO_SOURCE_INVALID", "Источник события ЭДО не подтверждён")
	}
	provided, err := hex.DecodeString(strings.TrimPrefix(headers.Signature, "sha256="))
	expected := s.Sign(raw, time.Unix(stamp, 0))
	want, _ := hex.DecodeString(strings.TrimPrefix(expected.Signature, "sha256="))
	if err != nil || strings.ToLower(headers.Signature) != headers.Signature || !hmac.Equal(provided, want) {
		return fail(403, "EDO_SOURCE_INVALID", "Источник события ЭДО не подтверждён")
	}
	event, err := Parse(raw)
	if err != nil {
		return fail(400, "INVALID_REQUEST", "Некорректная схема события ЭДО")
	}
	if event.Source != Source {
		return fail(403, "EDO_SOURCE_INVALID", "Источник события ЭДО не соответствует ключу")
	}
	hash := sha256.Sum256(raw)
	env := Envelope{Event: event, Raw: append([]byte(nil), raw...), Hash: hex.EncodeToString(hash[:]), RequestID: requestID}
	previous, err := s.repo.Accept(ctx, env)
	if err != nil {
		return deals.Result{}, err
	}
	if previous != nil {
		return *previous, nil
	}
	return s.repo.Process(ctx, env)
}
func text(s string, max int) bool {
	if s == "" || !utf8.ValidString(s) || utf8.RuneCountInString(s) > max {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func (s *Service) Queue(ctx context.Context, a Actor, key string, in Input) (deals.Result, error) {
	if a.UserID == "" {
		return deals.Result{}, ErrForbidden
	}
	if !deals.ValidKey(key) {
		return (deals.Failure{Status: 400, Code: "IDEMPOTENCY_KEY_REQUIRED", Message: "Нужен корректный Idempotency-Key"}).Result(a.RequestID), nil
	}
	cmd := Command{Actor: a, Key: key, Input: in}
	valid := false
	for _, v := range []string{"valid", "single_signature", "wrong_amount", "wrong_parties", "wrong_document", "wrong_document_version", "wrong_document_hash", "wrong_deal_version", "invalid_hmac", "replay", "same_id_different_body"} {
		if in.Template == v {
			valid = true
		}
	}
	if !valid || !text(in.DealID, 128) || ((in.Template == "replay" || in.Template == "same_id_different_body") && (in.ReplayEventID == nil || !text(*in.ReplayEventID, 128))) || (in.ReplayEventID != nil && in.Template != "replay" && in.Template != "same_id_different_body") {
		cmd.Invalid = &deals.Failure{Status: 400, Code: "INVALID_REQUEST", Message: "Проверьте сделку, шаблон и исходное событие"}
	}
	raw, _ := json.Marshal(in)
	hash := sha256.Sum256(raw)
	cmd.Hash = hex.EncodeToString(hash[:])
	return s.repo.Queue(ctx, cmd)
}
func (s *Service) Get(ctx context.Context, a Actor, id string) (Delivery, error) {
	if a.UserID == "" {
		return Delivery{}, ErrForbidden
	}
	if !text(id, 128) {
		return Delivery{}, ErrNotFound
	}
	return s.repo.Get(ctx, a, id)
}
