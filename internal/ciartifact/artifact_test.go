package ciartifact

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcpshka/internal/distributedci"
)

func TestArtifactRoundTripRejectsTamperAndWrongSource(t *testing.T) {
	old := Base
	Base = t.TempDir()
	t.Cleanup(func() { Base = old })
	raw := []byte("arm64 binary fixture")
	sum := sha256.Sum256(raw)
	a := distributedci.Artifact{Path: "out/distributed/diag", SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(raw))}
	p := distributedci.Plan{Commit: strings.Repeat("a", 40), Tree: strings.Repeat("b", 40)}
	if e := Store("/root/repos/diag", p, []distributedci.Artifact{a}, func(distributedci.Artifact) ([]byte, error) { return raw, nil }); e != nil {
		t.Fatal(e)
	}
	path, sha, e := Resolve("/root/repos/diag", p.Commit, a.Path)
	if e != nil || sha != a.SHA256 {
		t.Fatal(path, sha, e)
	}
	if _, _, e = Resolve("/root/repos/diag", strings.Repeat("c", 40), a.Path); e == nil {
		t.Fatal("wrong commit")
	}
	if e = os.WriteFile(path, []byte(strings.Repeat("x", len(raw))), 0600); e != nil {
		t.Fatal(e)
	}
	if _, _, e = Resolve("/root/repos/diag", p.Commit, a.Path); e == nil {
		t.Fatal("tamper")
	}
	if e = Store("/root/repos/diag", p, []distributedci.Artifact{a}, func(distributedci.Artifact) ([]byte, error) { return []byte("wrong"), nil }); e == nil {
		t.Fatal("download SHA mismatch")
	}
	if _, e = os.Stat(filepath.Join(filepath.Dir(path), "manifest.json")); e != nil {
		t.Fatal(e)
	}
}
