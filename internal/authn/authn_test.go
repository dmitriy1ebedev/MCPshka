package authn

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeRegistry(t *testing.T, path string, reg Registry) {
	t.Helper()
	data, err := json.Marshal(reg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestRegistryRotationAndRevocation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	tokenA := "token-a-secret"
	tokenB := "token-b-secret"
	writeRegistry(t, path, Registry{
		Version:     1,
		Credentials: []Credential{{ID: "chatgpt-a", TokenSHA256: HashToken(tokenA)}},
	})
	auth, err := New(path, "legacy-must-not-bypass")
	if err != nil {
		t.Fatal(err)
	}
	id, err := auth.Authenticate(tokenA)
	if err != nil || id.ID != "chatgpt-a" || id.Source != "registry" {
		t.Fatalf("token A failed: id=%+v err=%v", id, err)
	}
	if _, err := auth.Authenticate("legacy-must-not-bypass"); err == nil {
		t.Fatal("legacy fallback bypassed configured registry")
	}

	writeRegistry(t, path, Registry{
		Version: 1,
		Credentials: []Credential{
			{ID: "chatgpt-a", TokenSHA256: HashToken(tokenA)},
			{ID: "chatgpt-b", TokenSHA256: HashToken(tokenB)},
		},
	})
	if _, err := auth.Authenticate(tokenB); err != nil {
		t.Fatalf("rotated token B not accepted without restart: %v", err)
	}

	writeRegistry(t, path, Registry{
		Version: 1,
		Credentials: []Credential{
			{ID: "chatgpt-a", TokenSHA256: HashToken(tokenA), Revoked: true},
			{ID: "chatgpt-b", TokenSHA256: HashToken(tokenB)},
		},
	})
	if _, err := auth.Authenticate(tokenA); err == nil {
		t.Fatal("revoked token A accepted")
	}
	if id, err := auth.Authenticate(tokenB); err != nil || id.ID != "chatgpt-b" {
		t.Fatalf("active token B failed after revocation: id=%+v err=%v", id, err)
	}
}

func TestRegistryExpiryPermissionsAndContext(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	writeRegistry(t, path, Registry{
		Version: 1,
		Credentials: []Credential{
			{ID: "expired", TokenSHA256: HashToken("old"), ExpiresAt: time.Now().UTC().Add(-time.Minute)},
			{ID: "active", TokenSHA256: HashToken("new"), ExpiresAt: time.Now().UTC().Add(time.Hour)},
		},
	})
	auth, err := New(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auth.Authenticate("old"); err == nil {
		t.Fatal("expired credential accepted")
	}
	id, err := auth.Authenticate("new")
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithIdentity(context.Background(), id)
	got, ok := IdentityFromContext(ctx)
	if !ok || got.ID != "active" {
		t.Fatalf("identity context=%+v ok=%v", got, ok)
	}

	if err := os.Chmod(path, 0640); err != nil {
		t.Fatal(err)
	}
	if _, err := New(path, ""); err != nil {
		t.Fatalf("group-readable registry for daemon group rejected: %v", err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := New(path, ""); err == nil {
		t.Fatal("world-readable registry accepted")
	}
}

func TestLegacyBearerOnlyWithoutRegistry(t *testing.T) {
	auth, err := New("", "legacy-secret")
	if err != nil {
		t.Fatal(err)
	}
	id, err := auth.Authenticate("legacy-secret")
	if err != nil || id.ID != "legacy-bearer" {
		t.Fatalf("legacy auth failed: id=%+v err=%v", id, err)
	}
	if _, err := auth.Authenticate("wrong"); err == nil {
		t.Fatal("wrong legacy token accepted")
	}
}
