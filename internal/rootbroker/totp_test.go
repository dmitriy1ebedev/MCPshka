package rootbroker

import (
	"context"
	"encoding/base32"
	"strings"
	"testing"
	"time"
)

func TestTOTPStandardSixDigitVectors(t *testing.T) {
	secret := []byte("12345678901234567890")
	vectors := []struct {
		unix int64
		want string
	}{
		{59, "287082"}, {1111111109, "081804"}, {1111111111, "050471"},
		{1234567890, "005924"}, {2000000000, "279037"}, {20000000000, "353130"},
	}
	for _, tc := range vectors {
		got := totpCode(secret, tc.unix/30)
		if got != tc.want {
			t.Fatalf("time=%d code=%s expected=%s", tc.unix, got, tc.want)
		}
	}
	raw := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret)
	if len(raw) != 32 {
		t.Fatal("unexpected RFC 6238 seed encoding")
	}
}
func TestRootTaskRejectsUnscopedAndUnexpectedActions(t *testing.T) {
	id := strings.Repeat("a", 32)
	digest := strings.Repeat("b", 64)
	if err := validateTaskFields(id, "homeassistant_automations", digest, digest); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"", "root", "shell", "all", "/", "homeassistant"} {
		if err := validateTaskFields(id, scope, digest, digest); err == nil {
			t.Fatalf("accepted privileged scope %q", scope)
		}
	}
	if err := validateTaskFields("../escape", "homeassistant_automations", digest, digest); err == nil {
		t.Fatal("path escape accepted")
	}
	if err := validateTaskFields(id, "homeassistant_automations", "notahash", digest); err == nil {
		t.Fatal("unbound plan accepted")
	}
	if _, err := RunRootTask(context.Background(), []string{"execute", "shell", id, "caller"}); err == nil {
		t.Fatal("arbitrary shell admitted")
	}
	if _, err := RunRootTask(context.Background(), []string{"inspect", "extra"}); err == nil {
		t.Fatal("inspect accepted arguments")
	}
}
func TestTaskPermitExpiryAndOwner(t *testing.T) {
	now := time.Now()
	p := rootTaskPermit{ID: strings.Repeat("a", 32), Owner: "local-stdio", Scope: "homeassistant_automations", State: "authorized", ExpiresAt: now.Add(60 * time.Minute)}
	if err := validateTaskAccess(p, "local-stdio", now); err != nil {
		t.Fatal(err)
	}
	if err := validateTaskAccess(p, "other", now); err == nil {
		t.Fatal("foreign actor accepted")
	}
	if err := validateTaskAccess(p, "local-stdio", now.Add(61*time.Minute)); err == nil {
		t.Fatal("expired task accepted")
	}
	p.State = "revoked"
	if err := validateTaskAccess(p, "local-stdio", now); err == nil {
		t.Fatal("revoked task accepted")
	}
}
func TestHAStagingRequiresListUniqueIDs(t *testing.T) {
	good := []byte("- id: first\n  alias: First\n  triggers: []\n  actions: []\n- id: second\n  alias: Second\n  triggers: []\n  actions: []\n")
	if err := validateHAContent(context.Background(), good); err != nil {
		t.Fatalf("valid HA automations rejected: %v", err)
	}
	for _, bad := range []string{"id: single\n", "- id: duplicate\n- id: duplicate\n", "- id: valid\n- without: id\n", "- id: [broken\n"} {
		if err := validateHAContent(context.Background(), []byte(bad)); err == nil {
			t.Fatalf("invalid HA YAML accepted: %s", bad)
		}
	}
}
