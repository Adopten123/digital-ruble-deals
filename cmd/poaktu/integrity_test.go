package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestIntegrityKeyFilesAreIndependentAndNeverOverwritten(t *testing.T) {
	dir := t.TempDir()
	private := filepath.Join(dir, "private.pem")
	public := filepath.Join(dir, "public.pem")
	pub, err := generateIntegrityKeys(private, public)
	if err != nil {
		t.Fatal(err)
	}
	a, err := readIntegrityPrivate(private)
	if err != nil {
		t.Fatal(err)
	}
	b, err := readIntegrityPublic(public)
	if err != nil || !bytes.Equal(pub, b) || !bytes.Equal(a[32:], b) {
		t.Fatal("key roundtrip", err)
	}
	before, _ := os.ReadFile(private)
	if _, err = generateIntegrityKeys(private, public); err == nil {
		t.Fatal("overwrote existing keys")
	}
	after, _ := os.ReadFile(private)
	if !bytes.Equal(before, after) {
		t.Fatal("private key replaced")
	}
	if err = writeIntegrityExclusive(private, []byte("wrong"), 0600); err == nil {
		t.Fatal("exclusive write replaced key")
	}
	same := filepath.Join(dir, "same.pem")
	if _, err = generateIntegrityKeys(same, same); err == nil {
		t.Fatal("same key paths accepted")
	}
	if _, err = readIntegrityPublic(private); err == nil {
		t.Fatal("private key treated as public")
	}
	if _, err = readIntegrityPrivate(public); err == nil {
		t.Fatal("public key treated as private")
	}
	if err = os.WriteFile(public, append(append([]byte{}, after...), []byte("trailing")...), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = readIntegrityPublic(public); err == nil {
		t.Fatal("trailing material accepted")
	}
	if err = os.WriteFile(public, bytes.Repeat([]byte{'x'}, 8193), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = readIntegrityPublic(public); err == nil {
		t.Fatal("oversize accepted")
	}
	if _, err = readIntegrityFile(dir, 8192); err == nil {
		t.Fatal("directory accepted")
	}
}
