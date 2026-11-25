package distributedci

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

type Artifact struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

type Build struct {
	Package string `json:"package"`
	Path    string `json:"path"`
}

type ProjectCheck struct {
	Shard int      `json:"shard"`
	Argv  []string `json:"argv"`
}
type ProjectConfig struct {
	Builds []Build        `json:"builds"`
	Checks []ProjectCheck `json:"checks,omitempty"`
}

var artifactName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,100}$`)

func ValidArtifact(a Artifact) bool {
	b, e := hex.DecodeString(a.SHA256)
	return e == nil && len(b) == 32 && a.SHA256 == strings.ToLower(a.SHA256) && a.Bytes > 0 && a.Bytes <= 64<<20 && filepath.Dir(a.Path) == "out/distributed" && artifactName.MatchString(filepath.Base(a.Path)) && filepath.Clean(a.Path) == a.Path && !strings.ContainsAny(a.Path, "\\\x00\r\n")
}

func buildProject(ctx context.Context, dir string) ([]Artifact, error) {
	raw, e := os.ReadFile(filepath.Join(dir, ".ci/distributed.json"))
	if os.IsNotExist(e) {
		return nil, nil
	}
	if e != nil || len(raw) > 4096 {
		return nil, errors.New("invalid CI build profile")
	}
	var config ProjectConfig
	if json.Unmarshal(raw, &config) != nil || len(config.Builds) == 0 || len(config.Builds) > 4 {
		return nil, errors.New("invalid CI build profile")
	}
	out := []Artifact{}
	for _, b := range config.Builds {
		if !strings.HasPrefix(b.Package, "./cmd/") || strings.Contains(b.Package, "..") || !strings.HasPrefix(b.Path, "out/distributed/") || filepath.Clean(b.Path) != b.Path {
			return nil, errors.New("invalid CI build output")
		}
		if e = os.MkdirAll(filepath.Join(dir, filepath.Dir(b.Path)), 0755); e != nil {
			return nil, e
		}
		cmd := exec.CommandContext(ctx, "go", "build", "-p", "1", "-trimpath", "-buildvcs=false", "-o", b.Path, b.Package)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH=arm64", "CGO_ENABLED=0", "GOFLAGS=-mod=readonly")
		var output capBuffer
		output.limit = 4 << 20
		cmd.Stdout = &output
		cmd.Stderr = &output
		if cmd.Run() != nil {
			if ctx.Err() != nil || output.exceeded || infraOutput(output.String()) {
				return nil, ErrInfrastructure
			}
			return nil, errors.New("CI build failed")
		}
		f, e := os.Open(filepath.Join(dir, b.Path))
		if e != nil {
			return nil, e
		}
		h := sha256.New()
		n, e := io.Copy(h, io.LimitReader(f, (64<<20)+1))
		f.Close()
		a := Artifact{b.Path, hex.EncodeToString(h.Sum(nil)), n}
		if e != nil || !ValidArtifact(a) {
			return nil, errors.New("CI build artifact exceeds budget")
		}
		out = append(out, a)
	}
	return out, nil
}

func projectChecks(ctx context.Context, dir string, n int) error {
	raw, e := os.ReadFile(filepath.Join(dir, ".ci/distributed.json"))
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil || len(raw) > 4096 {
		return errors.New("invalid CI profile")
	}
	var config ProjectConfig
	if json.Unmarshal(raw, &config) != nil || len(config.Checks) > 8 {
		return errors.New("invalid CI checks")
	}
	for _, check := range config.Checks {
		if check.Shard < 0 || check.Shard >= len(Sites) || len(check.Argv) == 0 || len(check.Argv) > 32 {
			return errors.New("invalid CI check")
		}
		if check.Shard != n {
			continue
		}
		cmd := exec.CommandContext(ctx, check.Argv[0], check.Argv[1:]...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "PYTHONPATH=src", "GOMAXPROCS=2")
		var output capBuffer
		output.limit = 4 << 20
		cmd.Stdout = &output
		cmd.Stderr = &output
		if cmd.Run() != nil {
			if ctx.Err() != nil || output.exceeded || infraOutput(output.String()) {
				return ErrInfrastructure
			}
			return errors.New("project CI check failed")
		}
	}
	return nil
}
