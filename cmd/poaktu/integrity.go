package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Adopten123/digital-ruble-deals/internal/core/config"
	"github.com/Adopten123/digital-ruble-deals/internal/core/documentfile"
	integrityrepo "github.com/Adopten123/digital-ruble-deals/internal/features/integrity/repository/postgres"
	integrity "github.com/Adopten123/digital-ruble-deals/internal/features/integrity/service"
	"github.com/jackc/pgx/v5/pgxpool"
)

type integrityExit struct{ code int }

func (e integrityExit) Error() string { return "integrity command did not confirm a match" }
func (e integrityExit) ExitCode() int { return e.code }

func printIntegrity(value any, code int) error {
	if err := json.NewEncoder(os.Stdout).Encode(value); err != nil {
		return errors.New("cannot write integrity report")
	}
	if code != 0 {
		return integrityExit{code}
	}
	return nil
}
func failedIntegrity(err error) error { return printIntegrity(integrity.Unverifiable(err), 3) }

func runIntegrity(_ *slog.Logger, args []string) error {
	if len(args) == 0 {
		return failedIntegrity(integrity.ErrInvalidCheckpoint)
	}
	mode := args[0]
	if mode != "keygen" && mode != "checkpoint" && mode != "verify" {
		return failedIntegrity(integrity.ErrInvalidCheckpoint)
	}
	flags := flag.NewFlagSet("integrity "+mode, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var privatePath, publicPath, checkpointPath, outPath, pin string
	var acknowledge bool
	switch mode {
	case "keygen":
		flags.StringVar(&privatePath, "private-key", "", "new private key PEM; never overwritten")
		flags.StringVar(&publicPath, "public-key", "", "new public key PEM to pin independently")
	case "checkpoint":
		flags.StringVar(&privatePath, "private-key", "", "independent Ed25519 private key PEM")
		flags.StringVar(&outPath, "out", "", "new external checkpoint file; never overwritten")
		flags.BoolVar(&acknowledge, "acknowledge-current-state", false, "explicitly trust this quiescent state as a new reference")
	case "verify":
		flags.StringVar(&publicPath, "public-key", "", "independently pinned Ed25519 public key PEM")
		flags.StringVar(&checkpointPath, "checkpoint", "", "independently retained checkpoint")
		flags.StringVar(&pin, "checkpoint-sha256", "", "independently pinned canonical checkpoint SHA-256")
	}
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 {
		return failedIntegrity(integrity.ErrInvalidCheckpoint)
	}
	if mode == "keygen" {
		pub, err := generateIntegrityKeys(privatePath, publicPath)
		if err != nil {
			return failedIntegrity(integrity.ErrInvalidCheckpoint)
		}
		return printIntegrity(map[string]string{"status": "KEYS_CREATED", "algorithm": "Ed25519", "public_key_sha256": integrity.PublicKeyFingerprint(pub)}, 0)
	}
	var private ed25519.PrivateKey
	var public ed25519.PublicKey
	var checkpoint integrity.Checkpoint
	var err error
	if mode == "checkpoint" {
		if !acknowledge || outPath == "" {
			return failedIntegrity(integrity.ErrCannotCheckpoint)
		}
		// Refuse an existing destination before reading any database state.
		if _, e := os.Lstat(outPath); !errors.Is(e, os.ErrNotExist) {
			return failedIntegrity(integrity.ErrInvalidCheckpoint)
		}
		private, err = readIntegrityPrivate(privatePath)
	} else {
		public, err = readIntegrityPublic(publicPath)
		if err == nil {
			var raw []byte
			raw, err = readIntegrityFile(checkpointPath, integrity.MaxCheckpointBytes)
			if err == nil {
				checkpoint, err = integrity.ParseCheckpoint(raw)
			}
		}
		decoded, e := hex.DecodeString(pin)
		if err == nil && (e != nil || len(decoded) != 32 || strings.ToLower(pin) != pin || subtle.ConstantTimeCompare([]byte(pin), []byte(integrity.CheckpointFingerprint(checkpoint))) != 1) {
			err = integrity.ErrInvalidCheckpoint
		}
		if err == nil && integrity.Verify(checkpoint.Snapshot, checkpoint, public).Status != integrity.MATCH {
			err = integrity.ErrInvalidCheckpoint
		}
	}
	if err != nil {
		return failedIntegrity(integrity.ErrInvalidCheckpoint)
	}
	cfg, err := config.Load()
	if err != nil {
		return failedIntegrity(integrity.ErrUnavailable)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	poolConfig, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return failedIntegrity(integrity.ErrUnavailable)
	}
	poolConfig.MaxConns = 2
	poolConfig.ConnConfig.ConnectTimeout = 5 * time.Second
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return failedIntegrity(integrity.ErrUnavailable)
	}
	defer pool.Close()
	files, err := documentfile.OpenReadOnlyStore(cfg.DocumentsDir)
	if err != nil {
		return failedIntegrity(integrity.ErrUnavailable)
	}
	defer files.Close()
	snapshot, err := integrity.New(integrityrepo.New(pool, files)).Capture(ctx)
	if err != nil {
		return failedIntegrity(err)
	}
	if mode == "checkpoint" {
		checkpoint, err = integrity.CreateCheckpoint(snapshot, private)
		if err != nil {
			return failedIntegrity(err)
		}
		raw, e := json.MarshalIndent(checkpoint, "", "  ")
		if e != nil {
			return failedIntegrity(integrity.ErrInvalidCheckpoint)
		}
		if err = writeIntegrityExclusive(outPath, append(raw, '\n'), 0600); err != nil {
			return failedIntegrity(integrity.ErrUnavailable)
		}
		return printIntegrity(map[string]any{"status": "CHECKPOINT_CREATED", "checkpoint_sha256": integrity.CheckpointFingerprint(checkpoint), "public_key_sha256": checkpoint.KeyID, "log_id": snapshot.LogID, "sequence": snapshot.Sequence, "captured_at": snapshot.CapturedAt}, 0)
	}
	report := integrity.Verify(snapshot, checkpoint, public)
	code := 2
	if report.Status == integrity.MATCH {
		code = 0
	} else if report.Status == integrity.UNVERIFIABLE || report.Status == integrity.QUIESCENCE_REQUIRED {
		code = 3
	}
	return printIntegrity(report, code)
}

func readIntegrityFile(path string, limit int) ([]byte, error) {
	if path == "" {
		return nil, integrity.ErrInvalidCheckpoint
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Size() > int64(limit) {
		return nil, integrity.ErrInvalidCheckpoint
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || !after.Mode().IsRegular() {
		return nil, integrity.ErrInvalidCheckpoint
	}
	raw, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil || len(raw) > limit {
		return nil, integrity.ErrInvalidCheckpoint
	}
	return raw, nil
}
func writeIntegrityExclusive(path string, raw []byte, mode os.FileMode) error {
	if path == "" {
		return integrity.ErrInvalidCheckpoint
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	// A failed/unknown write is never replaced or silently retried with new keys.
	defer file.Close()
	if _, err = file.Write(raw); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	return file.Close()
}
func generateIntegrityKeys(privatePath, publicPath string) (ed25519.PublicKey, error) {
	if privatePath == "" || publicPath == "" {
		return nil, integrity.ErrInvalidCheckpoint
	}
	a, err := filepath.Abs(privatePath)
	if err != nil {
		return nil, err
	}
	b, err := filepath.Abs(publicPath)
	if err != nil || strings.EqualFold(a, b) {
		return nil, integrity.ErrInvalidCheckpoint
	}
	for _, path := range []string{privatePath, publicPath} {
		if _, err = os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			return nil, integrity.ErrInvalidCheckpoint
		}
	}
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return nil, err
	}
	publicDER, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, err
	}
	if err = writeIntegrityExclusive(privatePath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}), 0600); err != nil {
		return nil, err
	}
	if err = writeIntegrityExclusive(publicPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER}), 0644); err != nil {
		return nil, err
	}
	return pub, nil
}
func readIntegrityPrivate(path string) (ed25519.PrivateKey, error) {
	raw, err := readIntegrityFile(path, 8192)
	if err != nil {
		return nil, err
	}
	block, rest := pem.Decode(raw)
	if block == nil || block.Type != "PRIVATE KEY" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, integrity.ErrInvalidCheckpoint
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, integrity.ErrInvalidCheckpoint
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok || len(key) != ed25519.PrivateKeySize {
		return nil, integrity.ErrInvalidCheckpoint
	}
	return key, nil
}
func readIntegrityPublic(path string) (ed25519.PublicKey, error) {
	raw, err := readIntegrityFile(path, 8192)
	if err != nil {
		return nil, err
	}
	block, rest := pem.Decode(raw)
	if block == nil || block.Type != "PUBLIC KEY" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, integrity.ErrInvalidCheckpoint
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, integrity.ErrInvalidCheckpoint
	}
	key, ok := parsed.(ed25519.PublicKey)
	if !ok || len(key) != ed25519.PublicKeySize {
		return nil, integrity.ErrInvalidCheckpoint
	}
	return key, nil
}
