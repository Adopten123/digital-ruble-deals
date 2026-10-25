// Package service prepares resumable demo data without signing or reserving for users.
package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/Adopten123/digital-ruble-deals/internal/core/security"
	auth "github.com/Adopten123/digital-ruble-deals/internal/features/auth/service"
	deals "github.com/Adopten123/digital-ruble-deals/internal/features/deals/service"
	"regexp"
	"time"
	"unicode"
)

const Profile = "poaktu-demo-v1"
const InitialKopecks = int64(50000000)

var ErrInvalid = errors.New("invalid demo input")
var ErrConflict = errors.New("demo run id already has different inputs")
var ErrChanged = errors.New("demo seed facts changed")
var ErrOwnerRequired = errors.New("database owner required")
var ErrIncomplete = errors.New("demo drafts incomplete; retry the same run")
var slug = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{2,39}$`)

func ValidRunID(id string) bool { return slug.MatchString(id) }
func ValidPassword(password string) bool {
	if !auth.ValidPassword(password) {
		return false
	}
	for _, r := range password {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func ID(run, purpose string) string {
	sum := sha256.Sum256([]byte("poaktu:demo:ids:v1\x00" + run + "\x00" + purpose))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

type Identity struct {
	Role             string `json:"role"`
	UserID           string `json:"user_id"`
	Email            string `json:"email"`
	OrganizationID   string `json:"organization_id,omitempty"`
	OrganizationName string `json:"organization_name,omitempty"`
	AccountID        string `json:"account_id,omitempty"`
}

func Identities(run string) []Identity {
	out := []Identity{}
	for _, role := range []string{"customer", "supplier", "operator"} {
		a := Identity{Role: role, UserID: ID(run, role+"-user"), Email: role + "." + run + "@demo.poaktu.local"}
		if role != "operator" {
			a.OrganizationID = ID(run, role+"-organization")
			a.AccountID = ID(run, role+"-account")
			name := "Заказчик"
			if role == "supplier" {
				name = "Типография"
			}
			a.OrganizationName = "Показ " + run + ": " + name
		}
		out = append(out, a)
	}
	return out
}

type SeedCommand struct {
	RunID, RequestHash, Password string
	Identities                   []Identity
	PasswordHashes               []string
}

func (c SeedCommand) Prefix() string    { return "demo_seed:v1:" + c.RunID + ":" }
func (c SeedCommand) Marker() string    { return c.Prefix() + c.RequestHash }
func (c SeedCommand) Operation() string { return "demo_seed:v1:" + c.RunID }

type SeedResult struct {
	SeededAt time.Time
	Resumed  bool
}
type Repository interface {
	Seed(context.Context, SeedCommand) (SeedResult, error)
}
type DraftCreator interface {
	Create(context.Context, deals.Actor, string, deals.Input) (deals.Result, error)
}
type Service struct {
	repo   Repository
	drafts DraftCreator
	key    []byte
}

func New(repo Repository, drafts DraftCreator, key []byte) *Service {
	return &Service{repo: repo, drafts: drafts, key: append([]byte(nil), key...)}
}

type Draft struct {
	Scenario             string `json:"scenario"`
	ID                   string `json:"id"`
	InitialAmountKopecks string `json:"initial_amount_kopecks"`
}
type Manifest struct {
	Status                 string     `json:"status"`
	Contour                string     `json:"contour"`
	Profile                string     `json:"profile"`
	RunID                  string     `json:"run_id"`
	Resumed                bool       `json:"resumed"`
	SeededAt               time.Time  `json:"seeded_at"`
	DeadlineAt             time.Time  `json:"deadline_at"`
	InitialCustomerKopecks string     `json:"initial_customer_balance_kopecks"`
	Identities             []Identity `json:"identities"`
	Deals                  []Draft    `json:"deals"`
}

var scenarios = []struct{ id, title, description, amount string }{
	{"payment", "Печать на 10 000 коробках", "Учебные печатные работы на упаковке заказчика. Приёмка одним актом по всей партии.", "30000000"},
	{"dispute", "Спор до расчёта", "Учебный показ: спор открывается до второй подписи и блокирует выплату.", "10000000"},
	{"cancellation", "Двусторонняя отмена", "Учебный показ: два разных участника согласуют освобождение своего резерва.", "5000000"},
	{"insufficient-funds", "Недостаток доступных средств", "Учебный показ: сумма превышает начальный доступный остаток; резерв отклоняется.", "60000000"},
}

func (s *Service) Create(ctx context.Context, run, password string) (Manifest, error) {
	if s == nil || s.repo == nil || s.drafts == nil || len(s.key) != 32 || !ValidRunID(run) || !ValidPassword(password) {
		return Manifest{}, ErrInvalid
	}
	mac := hmac.New(sha256.New, s.key)
	_, _ = mac.Write([]byte("poaktu:demo:request:v1\x00" + run + "\x00" + password))
	cmd := SeedCommand{RunID: run, RequestHash: hex.EncodeToString(mac.Sum(nil)), Password: password, Identities: Identities(run)}
	for range cmd.Identities {
		hash, err := security.HashPassword(password)
		if err != nil {
			return Manifest{}, err
		}
		cmd.PasswordHashes = append(cmd.PasswordHashes, hash)
	}
	seed, err := s.repo.Seed(ctx, cmd)
	if err != nil {
		return Manifest{}, err
	}
	deadline := seed.SeededAt.UTC().Add(14 * 24 * time.Hour)
	manifest := Manifest{Status: "DEMO_READY", Contour: "test", Profile: Profile, RunID: run, Resumed: seed.Resumed, SeededAt: seed.SeededAt.UTC(), DeadlineAt: deadline, InitialCustomerKopecks: "50000000", Identities: cmd.Identities, Deals: []Draft{}}
	for _, v := range scenarios {
		in := deals.Input{SupplierPartyID: cmd.Identities[1].OrganizationID, Title: "[" + run + "] " + v.title, Description: v.description, AmountKopecks: v.amount, Currency: "RUB", DeadlineAt: deadline.Format(time.RFC3339Nano)}
		actor := deals.Actor{UserID: cmd.Identities[0].UserID, OrganizationID: cmd.Identities[0].OrganizationID, RequestID: ID(run, "create-"+v.id)}
		result, err := s.drafts.Create(ctx, actor, cmd.Operation()+":"+v.id, in)
		if err != nil {
			return Manifest{}, errors.Join(ErrIncomplete, err)
		}
		if result.Status != 201 {
			return Manifest{}, ErrIncomplete
		}
		var d deals.Deal
		if err = json.Unmarshal(result.Body, &d); err != nil || d.ID == "" || d.CustomerPartyID != actor.OrganizationID || d.SupplierPartyID != in.SupplierPartyID || d.AmountKopecks != in.AmountKopecks {
			return Manifest{}, ErrIncomplete
		}
		manifest.Deals = append(manifest.Deals, Draft{Scenario: v.id, ID: d.ID, InitialAmountKopecks: v.amount})
	}
	return manifest, nil
}
