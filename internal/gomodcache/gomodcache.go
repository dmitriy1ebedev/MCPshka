// Package gomodcache is the trusted Go module cache of root: reading a
// committed go.sum, naming cache entries, and warming the cache for a sealed
// offline test (MCP-197). It runs no project code.
package gomodcache

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Root is root's module cache, filled only by checksum-verified downloads.
const Root = "/root/.local/share/go/pkg/mod"

// ErrSum: go.sum is not the plain committed format.
var ErrSum = errors.New("go.sum outside the bounded format")

// Module paths and versions are bounded before they name cache entries.
var (
	pathPattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9._~/-]{0,250}$`)
	versionPattern = regexp.MustCompile(`^v[0-9A-Za-z.+-]{1,120}$`)
)

// Ref is one module version of go.sum; Source is set when go.sum has its
// zip hash (the build needs the code, not only go.mod).
type Ref struct {
	Path, Version string
	Source        bool
}

// ParseSum reads at most 1 MiB and 2000 modules of go.sum.
func ParseSum(r io.Reader) ([]Ref, error) {
	index := map[[2]string]int{}
	out := []Ref{}
	sc := bufio.NewScanner(io.LimitReader(r, 1<<20))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 0 {
			continue
		}
		if len(f) != 3 || !strings.HasPrefix(f[2], "h1:") {
			return nil, ErrSum
		}
		version, modOnly := strings.CutSuffix(f[1], "/go.mod")
		if !pathPattern.MatchString(strings.ToLower(f[0])) || strings.Contains(f[0], "..") || strings.Contains(f[0], "//") || !versionPattern.MatchString(version) {
			return nil, ErrSum
		}
		key := [2]string{f[0], version}
		i, ok := index[key]
		if !ok {
			i = len(out)
			index[key] = i
			out = append(out, Ref{Path: f[0], Version: version})
		}
		out[i].Source = out[i].Source || !modOnly
		if len(out) > 2000 {
			return nil, ErrSum
		}
	}
	if sc.Err() != nil {
		return nil, ErrSum
	}
	return out, nil
}

// Escape applies the module cache case encoding (!x for X).
func Escape(p string) string {
	var b strings.Builder
	for _, r := range p {
		if r >= 'A' && r <= 'Z' {
			b.WriteByte('!')
			b.WriteRune(r + ('a' - 'A'))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Missing lists path@version of go.sum modules whose code is absent from
// cache. A sealed snapshot copies only what is there; anything listed fails
// inside the offline sandbox with a misleading read-only error.
func Missing(goSum []byte, cache string) ([]string, error) {
	refs, err := ParseSum(bytes.NewReader(goSum))
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, r := range refs {
		if !r.Source {
			continue
		}
		if _, err := os.Stat(filepath.Join(cache, Escape(r.Path)+"@"+r.Version)); err != nil {
			out = append(out, r.Path+"@"+r.Version)
		}
	}
	return out, nil
}

// WarmArgs are the systemd-run arguments of one warm-up unit: only the
// committed go.mod and go.sum in dir, `go mod download` through the public
// proxy with every new module checked against sum.golang.org; no VCS, no
// foreign toolchain, the cache is the only writable target. wrap adds the
// heavy-slice properties.
func WarmArgs(unit, dir string) []string {
	gopath := filepath.Dir(filepath.Dir(Root))
	args := []string{"--unit=" + unit, "--quiet", "--collect", "--wait",
		"--property=Type=oneshot", "--property=User=root", "--property=Group=root", "--property=UMask=0022",
		"--property=WorkingDirectory=" + dir, "--property=ExecStopPost=/bin/rm -rf " + dir,
		"--property=RuntimeMaxSec=300s", "--property=MemoryMax=512M", "--property=CPUQuota=100%", "--property=TasksMax=64",
		"--property=NoNewPrivileges=yes", "--property=PrivateTmp=yes", "--property=PrivateDevices=yes",
		"--property=ProtectSystem=strict", "--property=ReadWritePaths=" + gopath + " " + dir,
		"--property=ProtectKernelTunables=yes", "--property=ProtectKernelModules=yes", "--property=ProtectControlGroups=yes",
		"--property=RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6", "--property=RestrictNamespaces=yes",
		"--property=CapabilityBoundingSet=", "--property=StandardOutput=null", "--property=StandardError=journal"}
	for _, e := range []string{"HOME=/root", "GOPATH=" + gopath, "GOMODCACHE=" + Root,
		"GOTOOLCHAIN=local", "GOFLAGS=-mod=mod", "GOPROXY=https://proxy.golang.org",
		"GOSUMDB=sum.golang.org", "GONOSUMDB=", "GONOSUMCHECK=", "GOPRIVATE=", "GOINSECURE=",
		"GOVCS=*:off", "GOTELEMETRY=off"} {
		args = append(args, "--setenv="+e)
	}
	return append(args, "--", "/usr/bin/go", "mod", "download")
}
