package hostconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMCP250SambaRootPrivilegeRiskAndRedaction(t *testing.T) {
	raw := []byte("[global]\ninterfaces = lo eth0 tailscale0\nbind interfaces only = yes\nhosts allow = 127.0.0.1 192.168.0.0/24\nhosts deny = ALL\npassword = NEVER_LEAK_MY_SECRET\n[System]\npath = /\nread only = yes\nadmin users = smbfiles\nvalid users = smbfiles\n[Workspace]\npath = /srv/samba/workspace\nread only = no\nvalid users = smbfiles\n[Files]\npath = /srv/samba/share\nread only = no\nvalid users = smbfiles\n")
	got := parseSamba(raw, time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC))
	if got.State != "OBSERVED" || !got.RootShareRisk || got.GlobalScope != "BIND_AND_HOST_RULES_CONFIGURED" || len(got.Shares) != 3 ||
		got.Shares[0].Path != "/" || !got.Shares[0].ReadOnly || !got.Shares[0].AdminUsersPresent ||
		got.Shares[0].Risk != "HOLD_ROOT_SHARE_PRIVILEGE" {
		t.Fatalf("failed to identify root-elevating Samba share: %+v", got)
	}
	for _, name := range []string{"workspace", "files"} {
		found := false
		for _, s := range got.Shares {
			if s.Name == name {
				found = true
				if s.ReadOnly || s.Risk != "NONE" {
					t.Fatalf("safe workspace falsely blocked: %+v", s)
				}
			}
		}
		if !found {
			t.Fatal("missing approved share", name)
		}
	}
	data, _ := json.Marshal(got)
	if strings.Contains(string(data), "smbfiles") || strings.Contains(string(data), "NEVER_LEAK_MY_SECRET") ||
		strings.Contains(string(data), "password") {
		t.Fatalf("secret or local user leaked: %s", data)
	}
	if got.ActualMutation {
		t.Fatal("read-only observation claims mutation")
	}
}
func TestMCP250SambaUnknownPathsAndGuestFailClosed(t *testing.T) {
	raw := []byte("[global]\ninterfaces=0.0.0.0\n[System]\npath=/\nread only=no\nguest ok=yes\n[Foreign]\npath=/etc/shadow\n")
	got := parseSamba(raw, time.Now())
	if got.State != "UNKNOWN" || !got.RootShareRisk || got.GlobalScope != "UNVERIFIED_NETWORK_SCOPE" || got.UnknownSections != 1 ||
		len(got.Shares) != 1 || got.Shares[0].Risk != "HOLD_UNAPPROVED_WRITABLE_PATH" {
		t.Fatalf("dangerous root/guest/unknown accepted: %+v", got)
	}
	data, _ := json.Marshal(got)
	if strings.Contains(string(data), "/etc/shadow") {
		t.Fatal("unknown share path disclosed")
	}
}
func TestMCP250SambaReportsInheritedRootRiskAndIncompleteIncludes(t *testing.T) {
	raw := []byte("[global]\nadmin users = PRIVATE_GROUP\ninclude = UNDISCLOSED_PATH\n[Files]\npath = /\n")
	got := parseSamba(raw, time.Now())
	if got.State != "UNKNOWN" || !got.RootShareRisk || len(got.Shares) != 1 || !got.Shares[0].AdminUsersPresent {
		t.Fatalf("inherited root risk obscured: %+v", got)
	}
	data, _ := json.Marshal(got)
	if strings.Contains(string(data), "PRIVATE_GROUP") || strings.Contains(string(data), "UNDISCLOSED_PATH") {
		t.Fatalf("private configuration leaked: %s", data)
	}
}

func TestMCP250SambaFileSymlinkAndLargeRefused(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "smb.conf")
	secret := filepath.Join(dir, "secret.conf")
	if err := os.WriteFile(secret, []byte("[System]\npath = /\npassword = DO_NOT_EXPOSE\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, config); err != nil {
		t.Fatal(err)
	}
	got, err := inspectSamba(config)
	if err == nil || got.State != "UNKNOWN" || got.ActualMutation {
		t.Fatalf("symlink accepted: %+v %v", got, err)
	}
	if err := os.Remove(config); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, make([]byte, maxConfigBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = inspectSamba(config)
	if err == nil {
		t.Fatal("oversized config accepted")
	}
	if err := os.WriteFile(config, []byte("[System]\npath=/\nread only=yes\nvalid users = example\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ok, err := inspectSamba(config)
	if err != nil || ok.State != "OBSERVED" || len(ok.Shares) != 1 || ok.Shares[0].AdminUsersPresent {
		t.Fatalf("safe fixed-source parse failed: %+v %v", ok, err)
	}
}
