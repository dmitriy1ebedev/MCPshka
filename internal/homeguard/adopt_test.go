package homeguard

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// P5.3 24.11: after a rollback someone re-created the drop-ins by hand with
// the same directives plus a comment; Install adopted nothing and held the
// whole policy. Same directives are ours in effect.
func TestInstallAdoptsHandWrittenSameDirectives(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(t.TempDir(), "backup.json")
	path := filepath.Join(root, "docker.service.d", "50-gateway-priority.conf")
	run := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		switch args[0] {
		case "show":
			return []byte("CPUWeight=100\nIOWeight=100\nCPUQuotaPerSecUSec=infinity\n"), nil
		case "daemon-reload":
			return nil, nil
		}
		return []byte("docker.service loaded active running\n"), nil
	}
	if err := Install(context.Background(), run, root, state, false); err != nil {
		t.Fatal(err)
	}
	if err := Install(context.Background(), run, root, state, true); err != nil {
		t.Fatal(err)
	}
	hand := "# дом и сеть важнее сборок\n[Service]\nCPUWeight=1000\nIOWeight=1000\n"
	os.MkdirAll(filepath.Dir(path), 0755)
	if err := os.WriteFile(path, []byte(hand), 0644); err != nil {
		t.Fatal(err)
	}
	if err := Install(context.Background(), run, root, state, false); err != nil {
		t.Fatalf("held on an equivalent drop-in: %v", err)
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != homeText {
		t.Fatalf("%q", raw)
	}
	// A different hand change is still the owner's and still holds.
	os.WriteFile(path, []byte("[Service]\nCPUWeight=50\n"), 0644)
	if err := Install(context.Background(), run, root, state, true); err == nil {
		t.Fatal("rollback overwrote a different owner change")
	}
}
