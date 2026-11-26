package storagefabric

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSandbox(t *testing.T) {
	for _, p := range []string{"../x", "/root", "a/../b", "a:b", "a\\b", "-flag", "a//b", "MCPshka/.trash/x", "MCPshka/backups/y"} {
		if _, e := Remote("drive", p, false); e == nil {
			t.Fatalf("accepted %q", p)
		}
	}
	if p, e := Remote("drive", "reports/a.txt", false); e != nil || p != "drive:reports/a.txt" {
		t.Fatalf("%s %v", p, e)
	}
	if _, e := Remote("iclouddrive", "x", false); e != ErrUnsupported {
		t.Fatalf("iCloud policy: %v", e)
	}
}
func TestVaultAndToken(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	v := Vault{Dir: dir}
	if e := v.Accept("drive", `{"access_token":"SECRET","token_type":"Bearer","refresh_token":"REFRESH"}`); e != nil {
		t.Fatal(e)
	}
	info, e := os.Stat(filepath.Join(dir, "drive.conf"))
	if e != nil || info.Mode().Perm() != 0600 {
		t.Fatal("vault not private")
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "drive.conf"))
	if !strings.Contains(string(raw), "type = drive") {
		t.Fatal("missing fixed backend")
	}
	if e = v.Accept("drive", `{"access_token":"x\\n[evil]"}`); e == nil {
		t.Fatal("accepted injection")
	}
	if e = v.Accept("iclouddrive", `{}`); e != ErrUnsupported {
		t.Fatal("accepted iCloud session")
	}
	os.Chmod(dir, 0755)
	if _, e = v.Config("drive"); e == nil {
		t.Fatal("public vault accepted")
	}
}
func TestRcloneCommandsNoDeleteOrEscape(t *testing.T) {
	for _, op := range []string{"list", "stat", "mkdir", "move", "rename", "trash", "upload", "download"} {
		req := Request{Action: op, Provider: "drive", Path: "a", Destination: "b", ID: strings.Repeat("a", 32)}
		argv, e := Command(req, "/private/staging/file")
		if e != nil {
			t.Fatalf("%s: %v", op, e)
		}
		joined := strings.Join(argv, " ")
		if strings.Contains(joined, "delete") || strings.Contains(joined, "purge") {
			t.Fatal("irreversible delete")
		}
		if op == "trash" && !strings.Contains(joined, "MCPshka/.trash/") {
			t.Fatal("not quarantined")
		}
	}
	_ = context.Background()
}

func TestUploadSourceRejectsSymlinkAncestorsAndHardlinks(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	os.Mkdir(real, 0700)
	os.WriteFile(filepath.Join(real, "source.blob"), []byte("owned artifact"), 0600)
	os.Symlink(real, filepath.Join(dir, "alias"))
	if f, e := openUploadSource(filepath.Join(dir, "alias", "source.blob")); e == nil {
		f.Close()
		t.Fatal("symlink ancestor accepted")
	}
	os.Link(filepath.Join(real, "source.blob"), filepath.Join(real, "hard.blob"))
	if f, e := openUploadSource(filepath.Join(real, "hard.blob")); e == nil {
		f.Close()
		t.Fatal("hardlinked source accepted")
	}
}
func TestUploadCopyChecksOwnedArtifactDigest(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source")
	os.WriteFile(src, []byte("owned artifact"), 0600)
	f, e := openUploadSource(src)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	if e = stageUpload(f, filepath.Join(dir, "staging"), strings.Repeat("0", 64), 14); e == nil {
		t.Fatal("changed artifact accepted")
	}
}

func TestVaultAcceptsRcloneExpiresIn(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	if e := (Vault{Dir: dir}).Accept("drive", `{"access_token":"SECRET","refresh_token":"REFRESH","token_type":"Bearer","expiry":"2026-10-24T10:00:00Z","expires_in":3600}`); e != nil {
		t.Fatalf("standard rclone token rejected: %v", e)
	}
}

func TestVaultAcceptWritesOwnOAuthApp(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	v := Vault{Dir: dir}
	if e := v.Accept("drive", `{"access_token":"A","refresh_token":"R","token_type":"Bearer","client_id":"123-abc.apps.googleusercontent.com","client_secret":"GOCSPX-secret_123"}`); e != nil {
		t.Fatal(e)
	}
	raw, _ := os.ReadFile(dir + "/drive.conf")
	cfg := string(raw)
	if !strings.Contains(cfg, "client_id = 123-abc.apps.googleusercontent.com\n") || !strings.Contains(cfg, "client_secret = GOCSPX-secret_123\n") || strings.Contains(cfg, `"client_id"`) {
		t.Fatalf("app credentials not written as rclone options:\n%s", cfg)
	}
	if !strings.Contains(cfg, "scope = drive.file\n") {
		t.Fatalf("a token without scope keeps the older drive.file:\n%s", cfg)
	}
	// 0.12.0: the web connect asks for the whole Drive and says so.
	if e := v.Accept("drive", `{"access_token":"A","refresh_token":"R","token_type":"Bearer","scope":"drive","client_id":"123-abc.apps.googleusercontent.com","client_secret":"GOCSPX-secret_123"}`); e != nil {
		t.Fatal(e)
	}
	if raw, _ = os.ReadFile(dir + "/drive.conf"); !strings.Contains(string(raw), "scope = drive\n") || strings.Contains(string(raw), `"scope"`) {
		t.Fatalf("scope not written as an rclone option:\n%s", raw)
	}
	if v.Accept("drive", `{"access_token":"A","scope":"drive.readonly\nx = y"}`) == nil || v.Accept("dropbox", `{"access_token":"A","scope":"drive"}`) == nil {
		t.Fatal("foreign scope accepted")
	}
	p := Engine{Vault: v}.providers().Providers
	if p[0].ID != "drive" || p[0].Authorize != "storage_connect provider=drive" || strings.Contains(p[0].Note, "shared") || !strings.Contains(p[1].Authorize, "storage_connect") {
		t.Fatalf("provider hints: %+v", p[:2])
	}
	if e := v.Accept("drive", `{"access_token":"A","refresh_token":"R"}`); e != nil {
		t.Fatal(e)
	}
	if p = (Engine{Vault: v}).providers().Providers; !strings.Contains(p[0].Note, "shared Google client") {
		t.Fatalf("shared rclone client must be flagged: %+v", p[0])
	}
	for _, bad := range []string{
		`{"access_token":"A","client_id":"only-the-id-given"}`,
		`{"access_token":"A","client_id":"x\ntype = local","client_secret":"secretsecret"}`,
		`{"access_token":"A","client_id":"short","client_secret":"secretsecret"}`,
	} {
		if v.Accept("drive", bad) == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}

func TestVaultAcceptsOneDriveWithDrive(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	v := Vault{Dir: dir}
	if e := v.Accept("onedrive", `{"access_token":"A","refresh_token":"R","token_type":"Bearer","drive_id":"b!AbC_12-x","drive_type":"personal"}`); e != nil {
		t.Fatal(e)
	}
	raw, _ := os.ReadFile(dir + "/onedrive.conf")
	cfg := string(raw)
	if !strings.Contains(cfg, "type = onedrive\n") || !strings.Contains(cfg, "drive_id = b!AbC_12-x\n") || !strings.Contains(cfg, "drive_type = personal\n") || strings.Contains(cfg, `"drive_id"`) {
		t.Fatalf("onedrive config:\n%s", cfg)
	}
	for _, bad := range []string{`{"access_token":"A"}`, `{"access_token":"A","drive_id":"x\ntype = local","drive_type":"personal"}`} {
		if v.Accept("onedrive", bad) == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	if v.Accept("drive", `{"access_token":"A","drive_id":"abc","drive_type":"personal"}`) == nil {
		t.Fatal("drive accepted onedrive keys")
	}
}

func TestNamespaceFolderStaysPut(t *testing.T) {
	for _, a := range []string{"move", "rename", "trash"} {
		if _, e := Command(Request{Action: a, Provider: "drive", Path: "MCPshka", Destination: "x", ID: strings.Repeat("a", 32)}, ""); e == nil {
			t.Fatalf("%s of MCPshka accepted", a)
		}
	}
	if _, e := Command(Request{Action: "list", Provider: "drive", Path: "MCPshka"}, ""); e != nil {
		t.Fatal(e)
	}
}

func TestRcloneReasonClassifiesAndMasks(t *testing.T) {
	r := rcloneReason("2026/11/09 ERROR : couldn't fetch token: invalid_grant: maybe token expired? ya29.a0AfB_byCabcdefghijklmnopqrstuvwxyz0123456789ABCDEF\n")
	if !strings.HasPrefix(r, "AUTH_REQUIRED") || strings.Contains(r, "ya29.a0AfB_byCabcdefghij") {
		t.Fatalf("%q", r)
	}
	if r := rcloneReason("ERROR : MCPshka: directory not found"); !strings.HasPrefix(r, "NOT_FOUND") {
		t.Fatalf("%q", r)
	}
	f := &rcloneFailure{reason: "NETWORK"}
	if !errors.Is(f, ErrUnknown) || reasonOf(f) != "NETWORK" {
		t.Fatal("rcloneFailure must stay ErrUnknown for effects")
	}
	// MCP-165: a provider refusal is FAILED, not UNKNOWN.
	if !definiteRefusal(rcloneReason("ERROR : googleapi: Error 403: Rate Limit Exceeded, rateLimitExceeded")) || definiteRefusal("NETWORK: i/o timeout") {
		t.Fatal("refusal classification")
	}
	if r := rcloneReason("ERROR : error listing: googleapi: Error 403: , rateLimitExceeded"); strings.Contains(r, ": ,") {
		t.Fatalf("empty API message left a dangling fragment: %q", r)
	}
	if reasonOf(&commandError{reason: "QUOTA_OR_RATE_LIMIT: x"}) != "QUOTA_OR_RATE_LIMIT: x" {
		t.Fatal("refusal reason lost")
	}
}

func TestUncertainOperationSettlesFromReadBack(t *testing.T) {
	for _, c := range []struct {
		action   string
		src, dst presence
		size     int64
		want     string
	}{
		{"mkdir", present, unseen, 0, "done"}, {"mkdir", absent, unseen, 0, "failed"}, {"mkdir", unseen, unseen, 0, ""},
		{"upload", present, unseen, 7, "done"}, {"upload", present, unseen, 3, ""}, {"upload", absent, unseen, 0, "failed"},
		{"move", absent, present, 0, "done"}, {"move", present, absent, 0, "failed"}, {"rename", present, present, 0, ""}, {"move", unseen, present, 0, ""},
		{"trash", absent, unseen, 0, "done"}, {"trash", present, unseen, 0, "failed"}, {"trash", unseen, unseen, 0, ""},
		{"download", unseen, unseen, 0, "failed"},
	} {
		if got := verdict(Request{Action: c.action, ArtifactSize: 7}, c.src, c.dst, c.size); got != c.want {
			t.Errorf("%s src=%d dst=%d size=%d: %q, want %q", c.action, c.src, c.dst, c.size, got, c.want)
		}
	}
}

func TestMissingPathIsNotFoundByExitCode(t *testing.T) {
	for _, c := range []struct {
		bin    string
		code   int
		reason string
		want   string
	}{
		{Rclone, 3, "ERROR", "NOT_FOUND"}, {Rclone, 4, "ERROR: x", "NOT_FOUND: x"}, {Rclone, 1, "ERROR", "ERROR"},
		{Rclone, 3, "QUOTA_OR_RATE_LIMIT", "QUOTA_OR_RATE_LIMIT"}, {"/usr/bin/systemctl", 3, "ERROR", "ERROR"},
	} {
		if got := exitReason(c.bin, c.code, c.reason); got != c.want {
			t.Errorf("%s %d %q: %q, want %q", c.bin, c.code, c.reason, got, c.want)
		}
	}
}
