package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Signature is a test action by an authenticated organisation owner. It does
// not modify PDF bytes and is not a qualified electronic signature.
type Signature struct {
	ID              string    `json:"id"`
	OrganisationID  string    `json:"organisation_id"`
	UserID          string    `json:"user_id"`
	DealVersion     int64     `json:"deal_version"`
	DocumentID      string    `json:"document_id"`
	DocumentVersion int64     `json:"document_version"`
	DocumentSHA256  string    `json:"document_sha256"`
	CreatedAt       time.Time `json:"created_at"`
}

type SignInput struct {
	DealVersion     int64  `json:"deal_version"`
	DocumentVersion int64  `json:"document_version"`
	DocumentSHA256  string `json:"document_sha256"`
}

type SignCommand struct {
	Actor          Actor
	DocumentID     string
	Key, Hash      string
	Input          SignInput
	Invalid        *Failure
	VerifyDocument func(Document, string) error
}

type SignatureRepository interface {
	Sign(context.Context, SignCommand) (Result, error)
}

func (s *Service) Sign(ctx context.Context, actor Actor, documentID, key string, in SignInput) (Result, error) {
	if actor.UserID == "" || actor.OrganizationID == "" || !validText(documentID, 1, 128, false) {
		return Result{}, ErrNotFound
	}
	if !ValidKey(key) {
		return Failure{400, "IDEMPOTENCY_KEY_REQUIRED", "Нужен Idempotency-Key длиной от 16 до 128 ASCII-символов без пробелов"}.Result(actor.RequestID), nil
	}
	var invalid *Failure
	if in.DealVersion < 1 || in.DocumentVersion < 1 || len(in.DocumentSHA256) != 64 {
		invalid = &Failure{400, "INVALID_REQUEST", "Нужны положительные версии сделки и PDF и точный SHA-256 документа"}
	} else if b, e := hex.DecodeString(in.DocumentSHA256); e != nil || len(b) != 32 || strings.ToLower(in.DocumentSHA256) != in.DocumentSHA256 {
		invalid = &Failure{400, "INVALID_REQUEST", "SHA-256 должен содержать 64 строчных шестнадцатеричных символа"}
	}
	b, err := json.Marshal(in)
	if err != nil {
		return Result{}, err
	}
	h := sha256.Sum256(b)
	repo, ok := s.repo.(SignatureRepository)
	if !ok {
		return Result{}, errors.New("signature repository unavailable")
	}
	return repo.Sign(ctx, SignCommand{Actor: actor, DocumentID: documentID, Key: key, Hash: hex.EncodeToString(h[:]), Input: in, Invalid: invalid, VerifyDocument: s.verifyDocument})
}
