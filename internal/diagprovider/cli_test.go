package diagprovider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCLIProbeReadOnlyBounded(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "diag")
	script := "#!/bin/sh\ncase \"$1\" in\n capabilities) echo '{\"schema_version\":\"diag.capabilities/v1alpha1\",\"deployment\":{\"mode\":\"native\"}}';;\n agent) echo '{\"evidence\":\"untrusted\"}';;\n *) exit 3;;\nesac\n"
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	p := CLIProbe{Binary: bin, Timeout: time.Second}
	cap, err := p.Capabilities(context.Background())
	if err != nil || cap.Protocol != 0 {
		t.Fatalf("%+v %v", cap, err)
	}
	ev, err := p.AgentEvidence(context.Background(), 1024)
	if err != nil || !ev.Untrusted || len(ev.Data) == 0 {
		t.Fatalf("%+v %v", ev, err)
	}
	if _, err = p.AgentEvidence(context.Background(), 1); !errors.Is(err, ErrUnsafe) {
		t.Fatal(err)
	}
}
func TestCLIProbeOutputOverflow(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "diag")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nhead -c 100000 /dev/zero\n"), 0700); err != nil {
		t.Fatal(err)
	}
	p := CLIProbe{Binary: bin, Timeout: time.Second}
	if _, err := p.Capabilities(context.Background()); !errors.Is(err, ErrUnsafe) {
		t.Fatal(err)
	}
}

func TestCLIProbeTimeout(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "diag")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nsleep 3\n"), 0700); err != nil {
		t.Fatal(err)
	}
	p := CLIProbe{Binary: bin, Timeout: 50 * time.Millisecond}
	if _, err := p.Capabilities(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
func TestCLIProbeRealDIAGOptional(t *testing.T) {
	if os.Getenv("DIAG_REAL_PROBE") != "1" {
		t.Skip("opt-in read-only host probe")
	}
	p := CLIProbe{Binary: "/usr/local/bin/diag", Timeout: 15 * time.Second}
	c, err := p.Capabilities(context.Background())
	if err != nil || c.Version != DIAGCapabilitiesSchema || c.Protocol != 0 {
		t.Fatalf("%+v %v", c, err)
	}
	ev, err := p.AgentEvidence(context.Background(), 16384)
	if err != nil || len(ev.Data) == 0 || !ev.Untrusted {
		t.Fatalf("evidence bytes=%d err=%v", len(ev.Data), err)
	}
}
