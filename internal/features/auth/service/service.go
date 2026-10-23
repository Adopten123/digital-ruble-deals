package service

import (
	"context"
	"errors"
	"net/mail"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Adopten123/digital-ruble-deals/internal/core/security"
)

var (
	ErrInvalidInput  = errors.New("invalid registration or login input")
	ErrEmailConflict = errors.New("email is already registered")
	ErrCredentials   = errors.New("invalid credentials")
	ErrNoSession     = errors.New("session is absent or expired")
)

type User struct {
	ID         string `json:"id"`
	Email      string `json:"email"`
	SystemRole string `json:"system_role"`
}
type Organization struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	Role string `json:"role"`
}
type Identity struct {
	User             User          `json:"user"`
	Organization     *Organization `json:"organisation"`
	SessionExpiresAt time.Time     `json:"session_expires_at"`
}
type Session struct {
	Hash      string
	UserID    string
	ExpiresAt time.Time
}
type Registration struct {
	UserID, OrganizationID, AccountID, Email, PasswordHash, Name, Kind, PreviousSessionHash string
	Session                                                                                 Session
}
type Credentials struct{ UserID, PasswordHash string }

// Repository owns atomic persistence; registration is all-or-nothing including its session.
type Repository interface {
	Register(context.Context, Registration) (Identity, error)
	Credentials(context.Context, string) (Credentials, error)
	ReplaceSession(context.Context, string, Session) (Identity, error)
	Identity(context.Context, string) (Identity, error)
	Revoke(context.Context, string) error
}

type Service struct {
	repo      Repository
	dummyHash string
	now       func() time.Time
}

func New(repo Repository) (*Service, error) {
	dummy, err := security.HashPassword("unmatched-dummy-password-policy")
	if err != nil {
		return nil, err
	}
	return &Service{repo: repo, dummyHash: dummy, now: time.Now}, nil
}

func normalizeEmail(email string) (string, bool) {
	email = strings.ToLower(strings.TrimSpace(email))
	parsed, err := mail.ParseAddress(email)
	return email, err == nil && parsed.Address == email && len(email) <= 254 && !strings.ContainsAny(email, "\r\n")
}

func ValidPassword(password string) bool {
	n := utf8.RuneCountInString(password)
	return utf8.ValidString(password) && n >= 12 && n <= 128 && len(password) <= 512
}
func validName(name string) bool {
	n := utf8.RuneCountInString(name)
	if n < 2 || n > 200 {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func (s *Service) Register(ctx context.Context, email, password, name, kind, oldHash string) (Identity, string, error) {
	email, valid := normalizeEmail(email)
	name = strings.TrimSpace(name)
	if !valid || !ValidPassword(password) || !validName(name) || (kind != "COMPANY" && kind != "IP") {
		return Identity{}, "", ErrInvalidInput
	}
	hash, err := security.HashPassword(password)
	if err != nil {
		return Identity{}, "", err
	}
	ids := make([]string, 3)
	for i := range ids {
		ids[i], err = security.ID()
		if err != nil {
			return Identity{}, "", err
		}
	}
	token, err := security.RandomToken()
	if err != nil {
		return Identity{}, "", err
	}
	registration := Registration{UserID: ids[0], OrganizationID: ids[1], AccountID: ids[2], Email: email, PasswordHash: hash, Name: name, Kind: kind, Session: Session{Hash: security.TokenHash(token), UserID: ids[0], ExpiresAt: s.now().UTC().Add(24 * time.Hour)}}
	// The previous session is revoked inside the same transaction that creates the identity.
	// No browser actor ID is trusted.
	registration.PreviousSessionHash = oldHash
	identity, err := s.repo.Register(ctx, registration)
	return identity, token, err
}

func (s *Service) Login(ctx context.Context, email, password, oldHash string) (Identity, string, error) {
	email, valid := normalizeEmail(email)
	if !valid || len(password) > 512 {
		security.VerifyPassword(s.dummyHash, "invalid")
		return Identity{}, "", ErrCredentials
	}
	credential, err := s.repo.Credentials(ctx, email)
	if errors.Is(err, ErrCredentials) {
		security.VerifyPassword(s.dummyHash, password)
		return Identity{}, "", ErrCredentials
	}
	if err != nil {
		return Identity{}, "", err
	}
	if !security.VerifyPassword(credential.PasswordHash, password) {
		return Identity{}, "", ErrCredentials
	}
	token, err := security.RandomToken()
	if err != nil {
		return Identity{}, "", err
	}
	identity, err := s.repo.ReplaceSession(ctx, oldHash, Session{Hash: security.TokenHash(token), UserID: credential.UserID, ExpiresAt: s.now().UTC().Add(24 * time.Hour)})
	return identity, token, err
}

func (s *Service) Me(ctx context.Context, token string) (Identity, error) {
	if !security.ValidToken(token) {
		return Identity{}, ErrNoSession
	}
	return s.repo.Identity(ctx, security.TokenHash(token))
}
func (s *Service) Logout(ctx context.Context, token string) error {
	if !security.ValidToken(token) {
		return ErrNoSession
	}
	return s.repo.Revoke(ctx, security.TokenHash(token))
}
