package main

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type toolchain struct {
	goHome, bin, py string
	capabilities    map[string]string
}

func newToolchain(home, configDir string) toolchain {
	if home == configDir {
		tools := filepath.Join(home, ".mcpshka-tools")
		return toolchain{goHome: filepath.Join(home, ".mcpshka-go"), bin: filepath.Join(tools, "bin"), py: filepath.Join(tools, "py")}
	}
	return toolchain{goHome: filepath.Join(configDir, "go"), bin: filepath.Join(configDir, "bin"), py: filepath.Join(configDir, "py")}
}

func (t toolchain) path() string { return t.bin + ":" + os.Getenv("PATH") }

func (t toolchain) env() []string {
	var env []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "MCPSHKA_RUNNER_TOKEN=") {
			env = append(env, e)
		}
	}
	pyPath := t.py
	if old := os.Getenv("PYTHONPATH"); old != "" {
		pyPath += ":" + old
	}
	return append(env, "PATH="+t.path(), "PYTHONPATH="+pyPath)
}

func (t toolchain) lookup(name string) string {
	if local, err := exec.LookPath(filepath.Join(t.bin, name)); err == nil {
		return local
	}
	p, _ := exec.LookPath(name)
	return p
}

func (t *toolchain) prepare(ctx context.Context) {
	c := map[string]string{"os": runtime.GOOS, "arch": runtime.GOARCH, "cpus": strconv.Itoa(runtime.NumCPU()), "go": "auto"}
	if p := t.lookup("go"); p != "" {
		if out, err := t.probe(ctx, 20*time.Second, p, "env", "GOVERSION"); err == nil && len(bytes.TrimSpace(out)) != 0 {
			c["go"] = strings.TrimSpace(string(out))
		}
	}
	if t.lookup("sqlite3") == "" && runtime.GOARCH == "amd64" {
		if err := t.installSQLite(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "sqlite3:", err)
		}
	}
	if t.lookup("sqlite3") != "" {
		c["sqlite3"] = "1"
	}
	if python := t.lookup("python3"); python != "" {
		_, err := t.probe(ctx, 20*time.Second, python, "-c", "import yaml")
		if err != nil {
			args := []string{"-m", "pip", "install", "-q", "--target", t.py, "pyyaml"}
			if _, err = t.probe(ctx, 5*time.Minute, python, args...); err != nil {
				_, _ = t.probe(ctx, 5*time.Minute, python, append(args, "--break-system-packages")...)
			}
			_, err = t.probe(ctx, 20*time.Second, python, "-c", "import yaml")
		}
		if err == nil {
			c["pyyaml"] = "1"
		}
	}
	t.capabilities = c
}

func (t toolchain) probe(ctx context.Context, timeout time.Duration, program string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Env = t.env()
	cmd.WaitDelay = 2 * time.Second
	var out tail
	cmd.Stdout = &out
	err := cmd.Run()
	return out.buffer.Bytes(), err
}

var goVersion = regexp.MustCompile(`(?m)^(go|toolchain)[ \t]+(?:go)?(1\.[0-9]+(?:\.[0-9]+)?(?:rc[0-9]+|beta[0-9]+)?)[ \t]*(?://[^\n]*)?$`)

func (t toolchain) goFor(ctx context.Context, work string) (string, error) {
	if p := t.lookup("go"); p != "" {
		return p, nil
	}
	want := "1.22.0"
	mod, err := os.ReadFile(filepath.Join(work, "go.mod"))
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	for _, m := range goVersion.FindAllSubmatch(mod, -1) {
		want = string(m[2])
		if string(m[1]) == "toolchain" {
			break
		}
	}
	if strings.Count(want, ".") == 1 && !strings.Contains(want, "rc") && !strings.Contains(want, "beta") {
		want += ".0"
	}
	dest := filepath.Join(t.goHome, want)
	if p, err := exec.LookPath(filepath.Join(dest, "go", "bin", "go")); err == nil {
		return p, nil
	}
	if err := os.MkdirAll(t.goHome, 0700); err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(t.goHome, ".download-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	body, err := download(ctx, "https://go.dev/dl/go"+want+".linux-"+runtime.GOARCH+".tar.gz")
	if err != nil {
		return "", err
	}
	defer body.Close()
	if err := unpack(stage, body); err != nil {
		return "", err
	}
	if _, err := exec.LookPath(filepath.Join(stage, "go", "bin", "go")); err != nil {
		return "", err
	}
	p := filepath.Join(dest, "go", "bin", "go")
	if err := os.Rename(stage, dest); err != nil {
		if _, checkErr := exec.LookPath(p); checkErr != nil {
			return "", err
		}
	}
	return p, nil
}

var sqliteRelease = regexp.MustCompile(`PRODUCT,[^,]*,([0-9]{4}/sqlite-tools-linux-x64-[0-9]+\.zip)`)

func download(ctx context.Context, url string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, statusError(resp.StatusCode)
	}
	return resp.Body, nil
}

func (t toolchain) installSQLite(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	page, err := download(ctx, "https://sqlite.org/download.html")
	if err != nil {
		return err
	}
	html, err := io.ReadAll(io.LimitReader(page, 2<<20))
	page.Close()
	if err != nil {
		return err
	}
	match := sqliteRelease.FindSubmatch(html)
	if match == nil {
		return fmt.Errorf("Linux amd64 release not found")
	}
	body, err := download(ctx, "https://sqlite.org/"+string(match[1]))
	if err != nil {
		return err
	}
	defer body.Close()
	raw, err := io.ReadAll(io.LimitReader(body, 32<<20))
	if err != nil {
		return err
	}
	z, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return err
	}
	for _, entry := range z.File {
		if filepath.Base(entry.Name) != "sqlite3" || entry.UncompressedSize64 > 20<<20 {
			continue
		}
		src, err := entry.Open()
		if err != nil {
			return err
		}
		defer src.Close()
		if err := os.MkdirAll(t.bin, 0700); err != nil {
			return err
		}
		f, err := os.CreateTemp(t.bin, ".sqlite3-")
		if err != nil {
			return err
		}
		defer os.Remove(f.Name())
		_, err = io.Copy(f, io.LimitReader(src, int64(entry.UncompressedSize64)+1))
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
		if err := os.Chmod(f.Name(), 0755); err != nil {
			return err
		}
		return os.Rename(f.Name(), filepath.Join(t.bin, "sqlite3"))
	}
	return fmt.Errorf("sqlite3 missing from archive")
}
