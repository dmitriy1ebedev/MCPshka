package nodes

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectPinnedNodeAndRejectMalformedProbe(t *testing.T) {
	ctx := context.Background()
	hostKey := fakeNode(t)
	if _, err := Add(ctx, "inspect-1", Addr{Host: "203.0.113.7", Port: 22, User: "worker"}); err != nil {
		t.Fatal(err)
	}
	fingerprint := presentKey(t, hostKey)
	if _, err := Accept(ctx, "inspect-1", fingerprint); err != nil {
		t.Fatal(err)
	}
	stub := filepath.Join(t.TempDir(), "ssh-stub")
	old := sshClient
	sshClient = stub
	t.Cleanup(func() { sshClient = old })
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nprintf 'Linux\\nx86_64\\n1001\\n/dev/vda1 100 10 90 10%% /\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	p, err := Inspect(ctx, "inspect-1")
	if err != nil || !p.OK || p.OS != "Linux" || p.Arch != "x86_64" || p.UID != 1001 || p.AvailableKB != 90 {
		t.Fatalf("expected verified inspection: %+v: %v", p, err)
	}
	if got := Get("inspect-1").LastInspect; got == nil || !got.OK {
		t.Fatalf("successful inspection not persisted: %+v", got)
	}
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nprintf 'bad result\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	p, err = Inspect(ctx, "inspect-1")
	if err == nil || p.OK {
		t.Fatalf("malformed remote output was accepted: %+v", p)
	}
	if got := Get("inspect-1").LastInspect; got == nil || got.OK || !strings.Contains(got.Error, "unexpected") {
		t.Fatalf("stale success survived a failed inspect: %+v", got)
	}
}

func TestInspectRejectsRootAccount(t *testing.T) {
	ctx := context.Background()
	hostKey := fakeNode(t)
	if _, err := Add(ctx, "inspect-root", Addr{Host: "203.0.113.7", Port: 22, User: "root"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Accept(ctx, "inspect-root", presentKey(t, hostKey)); err != nil {
		t.Fatal(err)
	}
	stub := filepath.Join(t.TempDir(), "ssh-stub")
	old := sshClient
	sshClient = stub
	t.Cleanup(func() { sshClient = old })
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nprintf 'Linux\\naarch64\\n0\\n/dev/vda1 100 10 90 10%% /\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	p, err := Inspect(ctx, "inspect-root")
	if err == nil || p.OK {
		t.Fatalf("root login marked ready: %+v", p)
	}
}
