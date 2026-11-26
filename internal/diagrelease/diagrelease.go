// Package diagrelease is the root worker that installs a DIAG build: it takes
// the commit only from DIAG main, checks the declared version and verified
// external CI artifacts, smokes the new binary as nobody, swaps diag and
// diagd keeping .previous, restarts diagd and rolls back if the installed
// pair does not answer. Moved out of rootbroker (0.19 monolith cut); the
// broker keeps the Face ID check and the unit start.
package diagrelease

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"mcpshka/internal/ciartifact"
	"mcpshka/internal/diagprovider"
	"mcpshka/internal/heavyqueue"
	"mcpshka/internal/releaseapply"
)

// Paths are variables for tests.
var (
	Repo        = "/root/repos/diag"
	Bin         = "/usr/local/bin"
	Results     = "/var/lib/mcpshka-diag-release"
	Version     = regexp.MustCompile(`^[0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,6}(-[0-9A-Za-z.]{1,32})?$`)
	versionDecl = regexp.MustCompile(`(?m)^\s*Version\s*=\s*"([^"]+)"`)
	commitRe    = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

func fileSHA(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return releaseapply.HexSHA(raw)
}

// Result is the worker's public report.
type Result struct {
	Version  string    `json:"version"`
	Commit   string    `json:"commit"`
	Unit     string    `json:"unit"`
	State    string    `json:"state"` // STARTED, INSTALLED, ROLLED_BACK, FAILED
	Previous string    `json:"previous_version,omitempty"`
	Diag     string    `json:"diag_sha256,omitempty"`
	Diagd    string    `json:"diagd_sha256,omitempty"`
	Error    string    `json:"error,omitempty"`
	Finished time.Time `json:"finished_at"`
}

// RunWorker runs as root in its own unit and always leaves a
// result file, also when it refuses.
func RunWorker(args []string) ([]byte, error) {
	if os.Geteuid() != 0 {
		return nil, errors.New("diag release worker must run as root")
	}
	if len(args) != 2 || !commitRe.MatchString(args[0]) || !Version.MatchString(args[1]) {
		return nil, errors.New("diag release worker arguments")
	}
	res := Result{Commit: args[0], Version: args[1], Unit: diagprovider.ReleaseUnit(args[1])}
	// The existing queue also bounds installation/smoke beside other releases.
	err := heavyqueue.Host().Run("diag-release", 15*time.Minute, func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 840*time.Second)
		defer cancel()
		return release(ctx, &res)
	})
	if err != nil {
		res.Error = err.Error()
		if res.State == "" {
			res.State = "FAILED"
		}
	}
	res.Finished = time.Now().UTC()
	raw, _ := json.Marshal(res)
	if e := os.MkdirAll(Results, 0o755); e == nil {
		_ = releaseapply.WriteRootFile(filepath.Join(Results, res.Version+".json"), raw, 0o644)
	}
	return raw, err
}

func release(ctx context.Context, res *Result) error {
	git := func(a ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "/usr/bin/git", append([]string{"-c", "safe.directory=" + Repo, "-C", Repo}, a...)...)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1", "HOME=/nonexistent"}
		return cmd.Output()
	}
	if _, err := git("merge-base", "--is-ancestor", res.Commit, "refs/heads/main"); err != nil {
		return errors.New("commit is not on DIAG main: HOLD")
	}
	archive, err := git("archive", "--format=tar", res.Commit)
	if err != nil {
		return errors.New("git archive failed")
	}
	work, err := os.MkdirTemp("", "mcpshka-diag-release-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	src := filepath.Join(work, "src")
	if err := ExtractRegular(archive, src); err != nil {
		return err
	}
	decl, err := os.ReadFile(filepath.Join(src, "internal/version/version.go"))
	if m := versionDecl.FindSubmatch(decl); err != nil || m == nil || string(m[1]) != res.Version {
		return fmt.Errorf("commit %s does not declare DIAG version %s: HOLD", res.Commit[:12], res.Version)
	}
	built := map[string]string{}
	for _, name := range []string{"diag", "diagd"} {
		out := filepath.Join(work, name)
		if err := ciartifact.CopyVerified(Repo, res.Commit, "out/distributed/"+name, out); err != nil {
			return fmt.Errorf("verified external CI artifact %s unavailable (%v): forge_ci provider=distributed mode=wait path=%s commit=%s", name, err, Repo, res.Commit)
		}
		built[name] = out
	}
	if err := os.Chmod(work, 0o755); err != nil {
		return err
	}
	if err := diagAnswers(ctx, built["diag"], res.Version, true); err != nil {
		return fmt.Errorf("new DIAG smoke: %w", err)
	}
	res.Previous = installedDiagVersion(ctx)
	if err := swapDiag(built); err != nil {
		return err
	}
	res.Diag, res.Diagd = fileSHA(filepath.Join(Bin, "diag")), fileSHA(filepath.Join(Bin, "diagd"))
	if err := restartDiagd(ctx); err == nil {
		if err = diagAnswers(ctx, filepath.Join(Bin, "diag"), res.Version, false); err == nil {
			res.State = "INSTALLED"
			return nil
		}
	}
	for _, name := range []string{"diag", "diagd"} {
		prev := filepath.Join(Bin, name+".previous")
		if raw, e := os.ReadFile(prev); e == nil {
			_ = releaseapply.WriteRootFile(filepath.Join(Bin, name), raw, 0o755)
		}
	}
	_ = restartDiagd(ctx)
	res.State = "ROLLED_BACK"
	return errors.New("installed DIAG did not answer; previous diag and diagd restored")
}

// ExtractRegular unpacks only directories and regular files inside dir: a
// symlink in the tree could make the build read root's files.
func ExtractRegular(archive []byte, dir string) error {
	r := tar.NewReader(bytes.NewReader(archive))
	var total int64
	for {
		h, err := r.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(h.Name)
		if name == "pax_global_header" || h.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
			return errors.New("archive path escapes: HOLD")
		}
		path := filepath.Join(dir, name)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(path, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if total += h.Size; total > 512<<20 {
				return errors.New("DIAG tree too large: HOLD")
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
			if err != nil {
				return err
			}
			_, err = io.Copy(f, io.LimitReader(r, h.Size))
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("DIAG tree has a non-regular entry %s: HOLD", name)
		}
	}
}

// diagAnswers checks version and the capabilities schema MCPshka parses;
// the not yet installed build runs as nobody.
func diagAnswers(ctx context.Context, bin, version string, unprivileged bool) error {
	run := func(a ...string) ([]byte, error) {
		c, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		argv := append([]string{bin}, a...)
		if unprivileged {
			argv = append([]string{"/usr/bin/setpriv", "--reuid=nobody", "--regid=nogroup", "--clear-groups", "--no-new-privs"}, argv...)
		}
		cmd := exec.CommandContext(c, argv[0], argv[1:]...)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C", "HOME=/nonexistent"}
		cmd.Dir = "/"
		return cmd.Output()
	}
	raw, err := run("version")
	if f := strings.Fields(string(raw)); err != nil || len(f) < 2 || f[0] != "diag" || f[1] != version {
		return fmt.Errorf("version is not %s", version)
	}
	raw, err = run("capabilities", "--json")
	if err != nil {
		return errors.New("capabilities failed")
	}
	_, err = diagprovider.ParseDIAGCapabilities(raw)
	return err
}

func installedDiagVersion(ctx context.Context) string {
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	raw, err := exec.CommandContext(c, filepath.Join(Bin, "diag"), "version").Output()
	if f := strings.Fields(string(raw)); err == nil && len(f) >= 2 {
		return f[1]
	}
	return ""
}

func swapDiag(built map[string]string) error {
	for _, name := range []string{"diag", "diagd"} {
		raw, err := os.ReadFile(built[name])
		if err != nil {
			return err
		}
		dst := filepath.Join(Bin, name)
		if cur, err := os.ReadFile(dst); err == nil {
			if err := releaseapply.WriteRootFile(dst+".previous", cur, 0o755); err != nil {
				return err
			}
		}
		if err := releaseapply.WriteRootFile(dst, raw, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func restartDiagd(ctx context.Context) error {
	c, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := exec.CommandContext(c, "/usr/bin/systemctl", "restart", "diagd.service").Run(); err != nil {
		return err
	}
	for i := 0; i < 20; i++ {
		if exec.CommandContext(c, "/usr/bin/systemctl", "is-active", "--quiet", "diagd.service").Run() == nil {
			return nil
		}
		time.Sleep(time.Second)
	}
	return errors.New("diagd not active")
}
