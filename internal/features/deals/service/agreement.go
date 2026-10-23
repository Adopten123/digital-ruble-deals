package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Adopten123/digital-ruble-deals/internal/core/documentfile"
)

type Document struct {
	ID              string    `json:"id"`
	DealID          string    `json:"deal_id"`
	DealVersion     int64     `json:"deal_version"`
	DocumentVersion int64     `json:"document_version"`
	SHA256          string    `json:"sha256"`
	FileName        string    `json:"file_name"`
	SizeBytes       int64     `json:"size_bytes"`
	ContentType     string    `json:"content_type"`
	CreatedAt       time.Time `json:"created_at"`
}
type Agreement struct {
	ID              string    `json:"id"`
	OrganisationID  string    `json:"organisation_id"`
	UserID          string    `json:"user_id"`
	DealVersion     int64     `json:"deal_version"`
	DocumentID      string    `json:"document_id"`
	DocumentVersion int64     `json:"document_version"`
	DocumentSHA256  string    `json:"document_sha256"`
	CreatedAt       time.Time `json:"created_at"`
}
type PatchInput struct {
	ExpectedVersion int64   `json:"expected_version"`
	SupplierPartyID *string `json:"supplier_party_id,omitempty"`
	Title           *string `json:"title,omitempty"`
	Description     *string `json:"description,omitempty"`
	AmountKopecks   *string `json:"amount_kopecks,omitempty"`
	Currency        *string `json:"currency,omitempty"`
	DeadlineAt      *string `json:"deadline_at,omitempty"`
}
type AgreeInput struct {
	DealVersion     int64  `json:"deal_version"`
	DocumentID      string `json:"document_id"`
	DocumentVersion int64  `json:"document_version"`
	DocumentSHA256  string `json:"document_sha256"`
}
type ReworkInput struct {
	DealVersion int64  `json:"deal_version"`
	Reason      string `json:"reason"`
}
type GenerateInput struct {
	ExpectedDealVersion int64  `json:"expected_deal_version"`
	TemplateID          string `json:"template_id"`
}
type DocumentStore interface {
	Put([]byte) (string, error)
	Read(string) ([]byte, error)
}
type StoredDocument struct {
	Metadata Document
	Key      string
}
type PreparedDocument struct {
	Key, SHA256, FileName string
	Size                  int64
}
type Mutation struct {
	Actor                    Actor
	ID, Key, Hash, Operation string
	ExpectedVersion          int64
	Patch                    PatchInput
	Agreement                AgreeInput
	Reason                   string
	Invalid                  *Failure
	Prepare                  func(Deal, int64) (PreparedDocument, error)
}
type AgreementRepository interface {
	Mutate(context.Context, Mutation) (Result, error)
	GetDocument(context.Context, Actor, string, *int64) (StoredDocument, error)
}

var ErrInvalidDocumentQuery = errors.New("invalid document version")

func (s *Service) mutation(ctx context.Context, actor Actor, id, key, op string, version int64, input any, invalid *Failure, prepare func(Deal, int64) (PreparedDocument, error)) (Result, error) {
	if actor.UserID == "" || actor.OrganizationID == "" || !validText(id, 1, 128, false) {
		return Result{}, ErrNotFound
	}
	if !ValidKey(key) {
		return Failure{400, "IDEMPOTENCY_KEY_REQUIRED", "Нужен Idempotency-Key длиной от 16 до 128 ASCII-символов без пробелов"}.Result(actor.RequestID), nil
	}
	if version <= 0 {
		invalid = &Failure{400, "INVALID_REQUEST", "Версия должна быть положительным целым числом"}
	}
	b, err := json.Marshal(input)
	if err != nil {
		return Result{}, err
	}
	h := sha256.Sum256(b)
	cmd := Mutation{Actor: actor, ID: id, Key: key, Hash: hex.EncodeToString(h[:]), Operation: op, ExpectedVersion: version, Invalid: invalid, Prepare: prepare}
	switch v := input.(type) {
	case PatchInput:
		cmd.Patch = v
	case AgreeInput:
		cmd.Agreement = v
	case ReworkInput:
		cmd.Reason = v.Reason
	}
	repo, ok := s.repo.(AgreementRepository)
	if !ok {
		return Result{}, errors.New("agreement repository unavailable")
	}
	return repo.Mutate(ctx, cmd)
}
func (s *Service) Patch(ctx context.Context, a Actor, id, key string, in PatchInput) (Result, error) {
	var invalid *Failure
	for _, field := range []struct {
		p         *string
		limit     int
		multiline bool
	}{{in.SupplierPartyID, 128, false}, {in.Title, 200, false}, {in.Description, 5000, true}} {
		p := field.p
		if p != nil {
			*p = strings.TrimSpace(*p)
			if !validText(*p, 1, field.limit, field.multiline) {
				invalid = badInput()
			}
		}
	}
	if in.SupplierPartyID != nil && *in.SupplierPartyID == a.OrganizationID {
		invalid = badInput()
	}
	if in.Currency != nil && *in.Currency != "RUB" {
		invalid = badInput()
	}
	if in.AmountKopecks != nil {
		n, e := strconv.ParseInt(*in.AmountKopecks, 10, 64)
		if e != nil || n <= 0 || strconv.FormatInt(n, 10) != *in.AmountKopecks {
			invalid = &Failure{400, "INVALID_AMOUNT", "Сумма должна быть положительной канонической строкой целых копеек int64"}
		}
	}
	if in.DeadlineAt != nil {
		d, e := time.Parse(time.RFC3339Nano, *in.DeadlineAt)
		if e != nil || d.Year() < 1 || d.Year() > 9999 {
			invalid = badInput()
		} else {
			*in.DeadlineAt = d.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano)
		}
	}
	return s.mutation(ctx, a, id, key, "PATCH_DEAL", in.ExpectedVersion, in, invalid, nil)
}
func (s *Service) Agree(ctx context.Context, a Actor, id, key string, in AgreeInput) (Result, error) {
	var invalid *Failure
	if !validText(in.DocumentID, 1, 128, false) || in.DocumentVersion < 1 || len(in.DocumentSHA256) != 64 {
		invalid = badInput()
	} else if b, e := hex.DecodeString(in.DocumentSHA256); e != nil || len(b) != 32 || strings.ToLower(in.DocumentSHA256) != in.DocumentSHA256 {
		invalid = badInput()
	}
	return s.mutation(ctx, a, id, key, "AGREE_DEAL", in.DealVersion, in, invalid, nil)
}
func (s *Service) Rework(ctx context.Context, a Actor, id, key string, in ReworkInput) (Result, error) {
	in.Reason = strings.TrimSpace(in.Reason)
	var invalid *Failure
	if !validText(in.Reason, 1, 2000, true) {
		invalid = badInput()
	}
	return s.mutation(ctx, a, id, key, "REWORK_DEAL", in.DealVersion, in, invalid, nil)
}
func (s *Service) Upload(ctx context.Context, a Actor, id, key string, version int64, name string, data []byte) (Result, error) {
	// The request fingerprint includes the exact bytes, expected context, and safe filename.
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	if !validText(name, 1, 200, false) {
		name = "document.pdf"
	}
	h := sha256.Sum256(data)
	input := struct {
		Version int64  `json:"expected_deal_version"`
		Name    string `json:"file_name"`
		SHA     string `json:"sha256"`
	}{version, name, hex.EncodeToString(h[:])}
	prepare := func(_ Deal, _ int64) (PreparedDocument, error) { return s.prepareDocument(data, name) }
	return s.mutation(ctx, a, id, key, "UPLOAD_DOCUMENT", version, input, nil, prepare)
}
func (s *Service) Generate(ctx context.Context, a Actor, id, key string, in GenerateInput) (Result, error) {
	var invalid *Failure
	if in.TemplateID != "printing-acceptance-v1" {
		invalid = badInput()
	}
	prepare := func(d Deal, v int64) (PreparedDocument, error) {
		b, e := documentfile.Generate(documentfile.Act{DealID: d.ID, DealVersion: v, Title: d.Title, Description: d.Description, CustomerName: d.Customer.Name, SupplierName: d.Supplier.Name, AmountKopecks: d.AmountKopecks, Currency: d.Currency, DeadlineAt: d.DeadlineAt.Format(time.RFC3339Nano)})
		if e != nil {
			return PreparedDocument{}, e
		}
		return s.prepareDocument(b, "printing-acceptance.pdf")
	}
	return s.mutation(ctx, a, id, key, "GENERATE_DOCUMENT", in.ExpectedDealVersion, in, invalid, prepare)
}
func (s *Service) prepareDocument(b []byte, name string) (PreparedDocument, error) {
	if err := documentfile.Validate(b); err != nil {
		return PreparedDocument{}, documentfile.ErrInvalid
	}
	if s.files == nil {
		return PreparedDocument{}, errors.New("document storage unavailable")
	}
	key, err := s.files.Put(b)
	if err != nil {
		return PreparedDocument{}, err
	}
	hash := sha256.Sum256(b)
	return PreparedDocument{Key: key, SHA256: hex.EncodeToString(hash[:]), FileName: name, Size: int64(len(b))}, nil
}
func (s *Service) Document(ctx context.Context, a Actor, id string, v *int64) (Document, error) {
	d, e := s.storedDocument(ctx, a, id, v)
	return d.Metadata, e
}
func (s *Service) storedDocument(ctx context.Context, a Actor, id string, v *int64) (StoredDocument, error) {
	if a.UserID == "" || a.OrganizationID == "" || !validText(id, 1, 128, false) {
		return StoredDocument{}, ErrNotFound
	}
	if v != nil && *v < 1 {
		return StoredDocument{}, ErrInvalidDocumentQuery
	}
	repo, ok := s.repo.(AgreementRepository)
	if !ok {
		return StoredDocument{}, errors.New("agreement repository unavailable")
	}
	return repo.GetDocument(ctx, a, id, v)
}
func (s *Service) DocumentBytes(ctx context.Context, a Actor, id string, v int64) (Document, []byte, error) {
	d, e := s.storedDocument(ctx, a, id, &v)
	if e != nil {
		return Document{}, nil, e
	}
	if s.files == nil {
		return Document{}, nil, errors.New("document storage unavailable")
	}
	b, e := s.files.Read(d.Key)
	if e != nil {
		return Document{}, nil, e
	}
	h := sha256.Sum256(b)
	if int64(len(b)) != d.Metadata.SizeBytes || hex.EncodeToString(h[:]) != d.Metadata.SHA256 {
		return Document{}, nil, errors.New("stored document integrity mismatch")
	}
	return d.Metadata, b, nil
}
