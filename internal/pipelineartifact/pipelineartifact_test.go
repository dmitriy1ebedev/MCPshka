package pipelineartifact

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPipelineArtifactSealAndAliases(t *testing.T) {
	for _, kind := range []string{"regular", "symlink", "directory-symlink", "hardlink", "missing"} {
		t.Run(kind, func(t *testing.T) {
			base := t.TempDir()
			scratch := filepath.Join(base, "scratch")
			project := filepath.Join(scratch, "project")
			if err := os.MkdirAll(filepath.Join(project, "build"), 0700); err != nil {
				t.Fatal(err)
			}
			raw := []byte("ipk evidence")
			source := filepath.Join(project, "original")
			os.WriteFile(source, raw, 0600)
			target := filepath.Join(project, "build", "app.ipk")
			switch kind {
			case "regular":
				os.WriteFile(target, raw, 0600)
			case "symlink":
				os.Symlink(source, target)
			case "directory-symlink":
				os.Remove(filepath.Dir(target))
				os.Symlink(project, filepath.Dir(target))
				target = "build/original"
			case "hardlink":
				os.Link(source, target)
			}
			path := "build/app.ipk"
			if kind == "directory-symlink" {
				path = target
			}
			dest := filepath.Join(base, "sealed")
			err := Seal([]string{path}, scratch, dest, uint32(os.Getuid()))
			if kind != "regular" {
				if err == nil {
					t.Fatal("unsafe artifact accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := os.ReadFile(filepath.Join(dest, "receipt.json"))
			if err != nil {
				t.Fatal(err)
			}
			var artifacts []Artifact
			if err = json.Unmarshal(receipt, &artifacts); err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(raw)
			if len(artifacts) != 1 || artifacts[0].SHA256 != hex.EncodeToString(sum[:]) || artifacts[0].Bytes != int64(len(raw)) {
				t.Fatalf("bad receipt: %s", receipt)
			}
			info, err := os.Stat(filepath.Join(dest, "0"))
			if err != nil || info.Mode().Perm() != 0400 {
				t.Fatal("artifact not sealed", err)
			}
		})
	}
}
