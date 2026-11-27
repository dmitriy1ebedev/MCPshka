package rootbroker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func testConfig() Config {
	return Config{
		SystemdStatus:   []string{"mcpshka.service", "ssh.service", "cron.service"},
		SystemdRestart:  []string{"cron.service"},
		SystemdMutation: []string{"ssh.service"},
		DockerRead:      []string{"homeassistant"},
		DockerRestart:   []string{"mosquitto"},
		DockerMutation:  []string{"homeassistant"},
	}
}

func TestSystemctlValidation(t *testing.T) {
	cfg := testConfig()
	ok := [][]string{
		{"show", "--no-pager", "--property=Id,ActiveState,SubState", "mcpshka.service"},
		{"is-active", "mcpshka.service"},
		{"restart", "ssh.service"},
		{"restart", "cron.service"},
	}
	for _, args := range ok {
		if err := validateSystemctl(cfg, args); err != nil {
			t.Fatalf("accepted form rejected %v: %v", args, err)
		}
	}
	bad := [][]string{
		{"show", "--no-pager", "--property=Environment", "mcpshka.service"},
		{"show-environment"},
		{"restart", "mcpshka.service"},
		{"restart", "unlisted.service"},
		{"stop", "cron.service"},
		{"cat", "/etc/mcpshka/auth.json"},
	}
	for _, args := range bad {
		if err := validateSystemctl(cfg, args); err == nil {
			t.Fatalf("unsafe form accepted: %v", args)
		}
	}
}

func TestJournalValidation(t *testing.T) {
	cfg := testConfig()
	// Never consult H96 approved-unit records in this synthetic test.
	validate := func(args []string) error {
		return validateJournalctlWithOwned(cfg, args, func(string) bool { return false })
	}
	if err := validate([]string{
		"--no-pager", "--output=short-iso-precise", "--unit=ssh.service", "--since=@123", "--lines=10",
	}); err != nil {
		t.Fatal(err)
	}
	// DIAG ACTIVE-08: the health surface (diagd) and MCPshka's own recipe
	// preparers have readable journals.
	for _, unit := range []string{"diagd.service", "mcpshka-recipe-prepare-47c4ddd9a0fe31c7221c226a90647218.service"} {
		if err := validate([]string{"--no-pager", "--unit=" + unit, "--lines=10"}); err != nil {
			t.Fatalf("%s journal refused: %v", unit, err)
		}
	}
	// Own projects by prefix: any lane-*/laned/diag* unit, no list to edit.
	for _, unit := range []string{"lane-engines-update.service", "lane-postcheck.service", "lane-native-host-tun.service", "laned.service", "diag-observer.service"} {
		if err := validate([]string{"--no-pager", "--unit=" + unit, "--lines=10"}); err != nil {
			t.Fatalf("bounded Lane journal refused for %s: %v", unit, err)
		}
	}
	for _, args := range [][]string{
		{"--unit=unlisted.service", "--lines=10"},
		{"--unit=ssh.service", "--lines=501"},
		{"--unit=ssh.service", "--output=json"},
		{"--unit=ssh.service", "--since=yesterday"},
		{"--unit=lane-hostbridge-xray.service", "--lines=10"},
	} {
		if err := validate(args); err == nil {
			t.Fatalf("unsafe journal form accepted: %v", args)
		}
	}
	// Approved dynamic journal access is still accepted, but only when
	// provided by the trusted owned-unit observation, not the test fixture.
	owner := func(u string) bool { return u == "synthetic-authorized.service" }
	if err := validateJournalctlWithOwned(cfg, []string{"--unit=synthetic-authorized.service", "--lines=10"}, owner); err != nil {
		t.Fatal("authorized owned-unit journal refused:", err)
	}
	if err := validateJournalctlWithOwned(cfg, []string{"--unit=synthetic-unauthorized.service", "--lines=10"}, owner); err == nil {
		t.Fatal("unowned journal accepted")
	}
}

func TestDockerValidation(t *testing.T) {
	cfg := testConfig()
	ok := [][]string{
		{"ps", "-a", "--no-trunc", "--format", "{{json .}}"},
		{"inspect", "--format", "{{json .State}}", "homeassistant"},
		{"stats", "--no-stream", "--format", "{{json .}}", "homeassistant"},
		{"logs", "--since", "60s", "--tail", "10", "homeassistant"},
		{"start", "homeassistant"},
		{"restart", "--time", "10", "homeassistant"},
		{"restart", "--time", "10", "mosquitto"},
		{"start", "mosquitto"}, // restart-listed: starting a stopped one is no wider
	}
	for _, args := range ok {
		if err := validateDocker(cfg, args); err != nil {
			t.Fatalf("accepted docker form rejected %v: %v", args, err)
		}
	}
	bad := [][]string{
		{"inspect", "--format", "{{json .State}}", "mosquitto"},
		{"stats", "--no-stream", "--format", "{{json .}}", "mosquitto"},
		{"logs", "--since", "60s", "--tail", "10", "mosquitto"},
		{"run", "--privileged", "alpine"},
		{"exec", "homeassistant", "sh"},
		{"start", "ghost"},
		{"stop", "--time", "10", "mosquitto"},
		{"inspect", "--format", "{{json .Config.Env}}", "homeassistant"},
		{"restart", "--time", "0", "homeassistant"},
	}
	for _, args := range bad {
		if err := validateDocker(cfg, args); err == nil {
			t.Fatalf("unsafe docker form accepted: %v", args)
		}
	}
}

func TestAuthorizePeerRequiresMainPIDForServiceUID(t *testing.T) {
	const serviceUID uint32 = 994
	if err := authorizePeer(&syscall.Ucred{Uid: 0, Pid: 999}, serviceUID, nil); err != nil {
		t.Fatalf("root bootstrap rejected: %v", err)
	}
	resolver := func() (int32, error) { return 1234, nil }
	if err := authorizePeer(&syscall.Ucred{Uid: serviceUID, Pid: 1234}, serviceUID, resolver); err != nil {
		t.Fatalf("service MainPID rejected: %v", err)
	}
	if err := authorizePeer(&syscall.Ucred{Uid: serviceUID, Pid: 1235}, serviceUID, resolver); err == nil {
		t.Fatal("same-uid child process accepted by broker")
	}
	if err := authorizePeer(&syscall.Ucred{Uid: serviceUID + 1, Pid: 1234}, serviceUID, resolver); err == nil {
		t.Fatal("foreign uid accepted by broker")
	}
	if err := authorizePeer(&syscall.Ucred{Uid: serviceUID, Pid: 1234}, serviceUID, nil); err == nil {
		t.Fatal("service uid accepted without pid policy")
	}
}

func TestLoadConfigRejectsBroadPermissionsAndSymlink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "broker.json")
	data := []byte(`{"systemd_status":["mcpshka.service"],"systemd_mutation":[],"docker_mutation":[]}`)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("broad config permissions accepted")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(link); err == nil {
		t.Fatal("symlink config accepted")
	}
}

func TestMCP250SambaBrokerRejectsArguments(t *testing.T) {
	if _, err := Run(context.Background(), Config{}, "smb-share-status", []string{"unexpected"}); err == nil {
		t.Fatal("Samba broker accepted an argument for a fixed-source request")
	}
}

func TestStateBackupBrokerHasNoPathOrWorkerAuthority(t *testing.T) {
	id := "0123456789abcdef0123456789abcdef"
	for _, action := range []string{"start", "probe", "restore"} {
		if err := validateStateBackup([]string{action, id}); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"worker", id}, {"start", "../escape"}, {"restore", id, "/root/.ssh/id"}, {"probe", strings.Repeat("z", 32)}, {"activate", id}} {
		if err := validateStateBackup(args); err == nil {
			t.Fatalf("unsafe backup request accepted: %v", args)
		}
	}
}

func TestCodexBridgeCannotRunArbitraryProtocolOrShell(t *testing.T) {
	for _, action := range []string{"sessions_list", "session_read", "task_submit", "task_status", "task_cancel"} {
		if err := validateCodexBridge([]string{action, "{}"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"thread/start", "{}"}, {"shell", "{}"}, {"session_read", "[]"}, {"session_read", strings.Repeat("x", 12001)}, {"sessions_list", "{}", "extra"}} {
		if err := validateCodexBridge(args); err == nil {
			t.Fatal("unsafe Codex operation accepted")
		}
	}
}

func TestRescueValidationRejectsWorkersShellAndForeignUnits(t *testing.T) {
	id := strings.Repeat("a", 32)
	for _, args := range [][]string{{"status"}, {"status", id}, {"start", "reconnect", id}, {"start", "restart", id}, {"start", "rollback", id}} {
		if err := validateRescue(args); err != nil {
			t.Fatalf("safe request rejected %v: %v", args, err)
		}
	}
	for _, args := range [][]string{{"watch"}, {"worker", "restart", id}, {"start", "ssh.service", id}, {"start", "restart; rm", id}, {"start", "restart", "../bad"}, {"status", id, "extra"}} {
		if validateRescue(args) == nil {
			t.Fatalf("unsafe request accepted: %v", args)
		}
	}
}

func TestPinnedRootLedgerReapContract(t *testing.T) {
	if err := validateDocker(testConfig(), []string{"restart", "--time", "10", "rootledger-app-1"}); err == nil {
		t.Fatal("generic Docker allowlist bypassed")
	}
}
