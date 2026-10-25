package documentfile

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestVerifierStoreNeverCreatesOrPublishesEvidence(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := OpenReadOnlyStore(missing); err == nil {
		t.Fatal("missing evidence accepted")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("verification created missing directory")
	}
	dir := t.TempDir()
	writer, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	data := minimalPDF("", "", "", []byte("q\nQ\n"))
	key, err := writer.Put(data)
	if err != nil {
		t.Fatal(err)
	}
	writer.Close()
	reader, err := OpenReadOnlyStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	got, err := reader.Read(key)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("read: %v", err)
	}
	if _, err = reader.Put(data); err == nil {
		t.Fatal("read-only verifier published new evidence")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatal("verifier changed store")
	}
	if _, err = reader.Read("../outside.pdf"); err == nil {
		t.Fatal("unrooted evidence path accepted")
	}
}
