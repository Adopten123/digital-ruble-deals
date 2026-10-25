package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"github.com/Adopten123/digital-ruble-deals/internal/core/config"
	"github.com/Adopten123/digital-ruble-deals/internal/core/integritylock"
	corepg "github.com/Adopten123/digital-ruble-deals/internal/core/repository/postgres"
	"github.com/Adopten123/digital-ruble-deals/internal/features/bootstrap"
	dealrepo "github.com/Adopten123/digital-ruble-deals/internal/features/deals/repository/postgres"
	deals "github.com/Adopten123/digital-ruble-deals/internal/features/deals/service"
	demorepo "github.com/Adopten123/digital-ruble-deals/internal/features/demorun/repository/postgres"
	demo "github.com/Adopten123/digital-ruble-deals/internal/features/demorun/service"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"log/slog"
	"os"
	"time"
)

type demoExit struct{ code int }

func (e demoExit) Error() string { return "demo preparation did not complete" }
func (e demoExit) ExitCode() int { return e.code }
func demoFailure(code, message, run string, exit int) error {
	result := map[string]string{"status": "DEMO_FAILED", "code": code, "message": message}
	if demo.ValidRunID(run) {
		result["run_id"] = run
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		return errors.New("cannot write demo report")
	}
	return demoExit{exit}
}
func readDemoPassword(path string) (string, error) {
	raw, err := readRegularFile(path, 1024)
	if err != nil {
		return "", demo.ErrInvalid
	}
	if bytes.HasSuffix(raw, []byte("\r\n")) {
		raw = raw[:len(raw)-2]
	} else if bytes.HasSuffix(raw, []byte("\n")) {
		raw = raw[:len(raw)-1]
	}
	password := string(raw)
	if !demo.ValidPassword(password) {
		return "", demo.ErrInvalid
	}
	return password, nil
}
func runDemo(_ *slog.Logger, args []string) error {
	invalid := func(run string) error {
		return demoFailure("INVALID_ARGUMENT", "Нужны demo create, run-id из 3–40 строчных латинских букв / цифр / дефиса и файл одного учебного пароля от 12 до 128 символов.", run, 2)
	}
	if len(args) == 0 || args[0] != "create" {
		return invalid("")
	}
	flags := flag.NewFlagSet("demo create", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var run, passwordFile string
	flags.StringVar(&run, "run-id", "", "stable demo identifier; retry exactly the same id")
	flags.StringVar(&passwordFile, "password-file", "", "existing private one-line password file; never printed")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || !demo.ValidRunID(run) || passwordFile == "" {
		return invalid(run)
	}
	password, err := readDemoPassword(passwordFile)
	if err != nil {
		return invalid(run)
	}
	cfg, err := config.Load()
	if err != nil {
		return demoFailure("UNAVAILABLE", "Нужны готовый тестовый контур с DEMO_MODE=true и подключение владельца БД.", run, 3)
	}
	key, err := readRegularFile(cfg.KeyFile, 32)
	if err != nil || len(key) != 32 {
		return demoFailure("UNAVAILABLE", "Не удалось прочитать сохранённый ключ приложения. Используйте прежний ключ после bootstrap.", run, 3)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pc, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return demoFailure("UNAVAILABLE", "Проверьте подключение учебной БД.", run, 3)
	}
	pc.MaxConns = 4
	pc.ConnConfig.ConnectTimeout = 5 * time.Second
	pc.AfterConnect = integritylock.HoldConnection
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return demoFailure("UNAVAILABLE", "Учебная БД недоступна.", run, 3)
	}
	defer pool.Close()
	if err = bootstrap.Ready(ctx, pool); err != nil {
		return demoFailure("UNAVAILABLE", "Сначала выполните штатные migrate, access и bootstrap.", run, 3)
	}
	result, err := demo.New(demorepo.New(pool), deals.New(dealrepo.New(pool), key), key).Create(ctx, run, password)
	switch {
	case errors.Is(err, corepg.ErrCommitUnknown):
		return demoFailure("UNKNOWN_OUTCOME", "Исход сохранения неизвестен. Повторите те же run-id, файл пароля и ключ приложения; новый ID не создавайте автоматически.", run, 3)
	case errors.Is(err, demo.ErrOwnerRequired):
		return demoFailure("OWNER_REQUIRED", "Подготовку запускает владелец учебной БД; runtime-доступ для неё не подходит.", run, 3)
	case errors.Is(err, demo.ErrConflict):
		return demoFailure("RUN_CONFLICT", "Этот ID уже связан с другими входными данными или занятой учёткой. Прежние данные сохранены.", run, 2)
	case errors.Is(err, demo.ErrChanged):
		return demoFailure("RUN_CHANGED", "Исходные учебные учётки или проводка изменены. Автоматический сброс не выполняется; проверьте данные.", run, 3)
	case err != nil:
		return demoFailure("INCOMPLETE", "Подготовка не завершена. Повторите тот же run-id с прежним файлом пароля и ключом приложения.", run, 3)
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}
