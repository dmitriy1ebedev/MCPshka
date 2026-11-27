package executor

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestReleaseCandidateServiceOwner(t *testing.T) {
	data := []byte("service-owned staged binary")
	digest := fmt.Sprintf("%x", sha256.Sum256(data))
	path := filepath.Join(t.TempDir(), "mcpshka-0.1.187."+digest[:12]+".candidate")
	if err := os.WriteFile(path, data, 0700); err != nil {
		t.Fatal(err)
	}
	// Root-owned plans and service-owned candidates have different UIDs on host.
	layout := cleanupLayout{uid: uint32(os.Geteuid() + 1), candidateUID: uint32(os.Geteuid())}
	upgrades := []cleanupUpgrade{{Version: "0.1.187", Image: digest, State: "SUCCEEDED"}}
	if got, err := cleanupBinImageHash(context.Background(), path, layout, upgrades); err != nil || got != digest {
		t.Fatalf("candidate not recognized: %s %v", got, err)
	}
	upgrades[0].Image = fmt.Sprintf("%064x", 1)
	if _, err := cleanupBinImageHash(context.Background(), path, layout, upgrades); err == nil {
		t.Fatal("unsealed service image accepted")
	}
}
