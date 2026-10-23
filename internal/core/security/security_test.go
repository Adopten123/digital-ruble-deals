package security

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPasswordAndTokens(t *testing.T) {
	hash, err := HashPassword("ДостаточноДлинныйПароль1!")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(hash, "ДостаточноДлинныйПароль1!") || VerifyPassword(hash, "different-password") {
		t.Fatal("password verification failed")
	}
	if VerifyPassword("$argon2id$v=19$m=999999999,t=2,p=1$abc$def", "password") {
		t.Fatal("unbounded policy accepted")
	}
	first, err := RandomToken()
	if err != nil {
		t.Fatal(err)
	}
	second, _ := RandomToken()
	if !ValidToken(first) || first == second || len(TokenHash(first)) != 64 {
		t.Fatal("token policy failed")
	}
}
func TestPersistentKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys", "app.key")
	first, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("key rotated on restart")
	}
	if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateKey(path); err == nil {
		t.Fatal("corrupt key silently replaced")
	}
}
