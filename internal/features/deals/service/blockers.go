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

type Dispute struct {
	ID             string    `json:"id"`
	DealID         string    `json:"deal_id"`
	DealVersion    int64     `json:"deal_version"`
	OrganisationID string    `json:"organisation_id"`
	UserID         string    `json:"user_id"`
	Reason         string    `json:"reason"`
	CreatedAt      time.Time `json:"created_at"`
}

type CancellationDecision struct {
	OrganisationID string    `json:"organisation_id"`
	UserID         string    `json:"user_id"`
	Decision       string    `json:"decision"`
	Reason         *string   `json:"reason"`
	CreatedAt      time.Time `json:"created_at"`
}

type CancellationRequest struct {
	ID                      string                `json:"id"`
	DealID                  string                `json:"deal_id"`
	DealVersion             int64                 `json:"deal_version"`
	InitiatorOrganisationID string                `json:"initiator_organisation_id"`
	InitiatorUserID         string                `json:"initiator_user_id"`
	Reason                  string                `json:"reason"`
	Status                  string                `json:"status"`
	CreatedAt               time.Time             `json:"created_at"`
	Decision                *CancellationDecision `json:"decision"`
}

type ReasonInput struct {
	DealVersion int64  `json:"deal_version"`
	Reason      string `json:"reason"`
}

type CancelAcceptInput struct {
	DealVersion int64 `json:"deal_version"`
}

type BlockCommand struct {
	Actor                    Actor
	ID, RequestID, Key, Hash string
	Operation                string
	ExpectedVersion          int64
	Reason                   string
	Invalid                  *Failure
}

type BlockRepository interface {
	Block(context.Context, BlockCommand) (Result, error)
}

func (s *Service) block(ctx context.Context, a Actor, id, requestID, key, operation string, input any) (Result, error) {
	if a.UserID == "" || a.OrganizationID == "" || !validText(id, 1, 128, false) || (requestID != "" && !validText(requestID, 1, 128, false)) {
		return Result{}, ErrNotFound
	}
	if !ValidKey(key) {
		return Failure{400, "IDEMPOTENCY_KEY_REQUIRED", "Нужен Idempotency-Key длиной от 16 до 128 ASCII-символов без пробелов"}.Result(a.RequestID), nil
	}
	cmd := BlockCommand{Actor: a, ID: id, RequestID: requestID, Key: key, Operation: operation}
	switch in := input.(type) {
	case ReasonInput:
		in.Reason = strings.TrimSpace(in.Reason)
		cmd.ExpectedVersion, cmd.Reason = in.DealVersion, in.Reason
		if !validText(in.Reason, 1, 2000, true) {
			cmd.Invalid = &Failure{400, "INVALID_REQUEST", "Укажите причину длиной от 1 до 2000 символов"}
		}
		input = in
	case CancelAcceptInput:
		cmd.ExpectedVersion = in.DealVersion
	default:
		return Result{}, errors.New("unsupported blocking input")
	}
	if cmd.ExpectedVersion < 1 {
		cmd.Invalid = &Failure{400, "INVALID_REQUEST", "Версия сделки должна быть положительным целым числом"}
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return Result{}, err
	}
	h := sha256.Sum256(raw)
	cmd.Hash = hex.EncodeToString(h[:])
	repo, ok := s.repo.(BlockRepository)
	if !ok {
		return Result{}, errors.New("blocking repository unavailable")
	}
	return repo.Block(ctx, cmd)
}

func (s *Service) Dispute(ctx context.Context, a Actor, id, key string, in ReasonInput) (Result, error) {
	return s.block(ctx, a, id, "", key, "OPEN_DISPUTE", in)
}
func (s *Service) RequestCancellation(ctx context.Context, a Actor, id, key string, in ReasonInput) (Result, error) {
	return s.block(ctx, a, id, "", key, "REQUEST_CANCELLATION", in)
}
func (s *Service) AcceptCancellation(ctx context.Context, a Actor, id, requestID, key string, in CancelAcceptInput) (Result, error) {
	return s.block(ctx, a, id, requestID, key, "ACCEPT_CANCELLATION", in)
}
func (s *Service) RejectCancellation(ctx context.Context, a Actor, id, requestID, key string, in ReasonInput) (Result, error) {
	return s.block(ctx, a, id, requestID, key, "REJECT_CANCELLATION", in)
}
