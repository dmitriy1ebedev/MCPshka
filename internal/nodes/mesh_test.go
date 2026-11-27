package nodes

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestObserveMeshPinnedPeerAndNoImplicitWorkerReadiness(t *testing.T) {
	fakeNode(t)
	_, err := Add(context.Background(), "mesh-one", Addr{Host: "203.0.113.7", Port: 22, User: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	jsonPath := filepath.Join(tmp, "status.json")
	stub := filepath.Join(tmp, "tailscale")
	old := tailscaleBinary
	tailscaleBinary = stub
	t.Cleanup(func() { tailscaleBinary = old })
	payload := `{"BackendState":"Running","Self":{"Online":true},"Peer":{"nodekey:ABCDEFGHIJKLMNOP":{"TailscaleIPs":["100.64.1.3"],"Online":true,"ExitNode":false}}}`
	if err := os.WriteFile(jsonPath, []byte(payload), 0600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nif [ \"$1\" = \"status\" ]; then cat " + jsonPath + "; else echo 'pong from peer via DERP'; fi\n"
	if err := os.WriteFile(stub, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	o, err := ObserveMesh(context.Background(), "mesh-one", "100.64.1.3", "nodekey:ABCDEFGHIJKLMNOP")
	if err != nil || !o.IdentityMatched || !o.TransportOnline || !o.PeerReachable || o.SSHAuthorized || o.WorkerReady {
		t.Fatalf("incorrect mesh observation: %+v, %v", o, err)
	}
	swapped, err := ObserveMesh(context.Background(), "mesh-one", "100.64.1.3", "nodekey:XXXXXXXXXXXXXXX")
	if err != nil || swapped.IdentityMatched || swapped.PeerReachable || !strings.Contains(swapped.Detail, "absent") {
		t.Fatalf("peer identity swap accepted: %+v, %v", swapped, err)
	}
	wrongIP, err := ObserveMesh(context.Background(), "mesh-one", "100.64.1.4", "nodekey:ABCDEFGHIJKLMNOP")
	if err != nil || wrongIP.IdentityMatched || wrongIP.PeerReachable {
		t.Fatalf("wrong overlay IP: %+v, %v", wrongIP, err)
	}
	for _, ip := range []string{"8.8.8.8", "127.0.0.1", "192.168.0.1", "fd00::1"} {
		if _, err := ObserveMesh(context.Background(), "mesh-one", ip, "nodekey:ABCDEFGHIJKLMNOP"); err == nil {
			t.Fatalf("accepted non-tailnet IP %s", ip)
		}
	}
	payload = `{"BackendState":"Running","Self":{"Online":true},"Peer":{"nodekey:ABCDEFGHIJKLMNOP":{"TailscaleIPs":["100.64.1.3"],"Online":true,"ExitNode":true}}}`
	if err := os.WriteFile(jsonPath, []byte(payload), 0600); err != nil {
		t.Fatal(err)
	}
	exitNode, err := ObserveMesh(context.Background(), "mesh-one", "100.64.1.3", "nodekey:ABCDEFGHIJKLMNOP")
	if err != nil || exitNode.PeerReachable || !strings.Contains(exitNode.Detail, "exit-node") {
		t.Fatalf("exit-node incorrectly admitted: %+v, %v", exitNode, err)
	}
}
