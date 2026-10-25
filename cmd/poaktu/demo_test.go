package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDemoPasswordFilePreservesSecretAndRejectsMultipleLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "password.txt")
	for _, suffix := range []string{"", "\n", "\r\n"} {
		value := " DemoRunOnly2026! "
		if err := os.WriteFile(path, []byte(value+suffix), 0600); err != nil {
			t.Fatal(err)
		}
		got, err := readDemoPassword(path)
		if err != nil || got != value {
			t.Fatal("password altered", err)
		}
	}
	for _, value := range []string{"short", "DemoRunOnly2026!\n\n", "DemoRunOnly2026!\nOtherPassword", "DemoRunOnly\x002026!"} {
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readDemoPassword(path); err == nil {
			t.Fatal("accepted invalid password file")
		}
	}
}
