package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

const outputLimit = 256 << 10

type tail struct{ buffer bytes.Buffer }

func (t *tail) Write(p []byte) (int, error) {
	n := len(p)
	if n >= outputLimit {
		t.buffer.Reset()
		p = p[n-outputLimit:]
	} else if overflow := t.buffer.Len() + n - outputLimit; overflow > 0 {
		t.buffer.Next(overflow)
	}
	_, err := t.buffer.Write(p)
	return n, err
}

func (r runner) execute(ctx context.Context, j *job) (res result) {
	res.JobID, res.Code = j.ID, -1
	if len(j.Argv) < 2 || j.Argv[0] != "go" || !slices.Contains([]string{"test", "vet", "build"}, j.Argv[1]) {
		res.Error = "invalid Go job"
		return
	}
	work, err := os.MkdirTemp("", "mcpshka-runner-")
	if err != nil {
		res.Error = err.Error()
		return
	}
	defer os.RemoveAll(work)
	archiveCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	err = r.unpackJob(archiveCtx, work, j.ID)
	cancel()
	if err == nil {
		var goPath string
		goPath, err = r.tools.goFor(ctx, work)
		if err == nil {
			res.Code, res.Output, err = r.tools.command(ctx, j, work, goPath)
		}
	}
	if cause := context.Cause(ctx); cause != nil {
		err = cause
	}
	if err != nil {
		res.Error = err.Error()
	}
	return
}

func (r runner) unpackJob(ctx context.Context, work, id string) error {
	body, err := r.request(ctx, "archive?job_id="+url.QueryEscape(id), nil)
	if err != nil {
		return err
	}
	defer body.Close()
	return unpack(work, body)
}

func (t toolchain) command(ctx context.Context, j *job, work, goPath string) (int, string, error) {
	timeout := j.Timeout
	if timeout <= 0 {
		timeout = 3600
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(min(timeout, 3*3600))*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, goPath, j.Argv[1:]...)
	cmd.Dir = work
	cmd.Env = append(t.env(), "PATH="+filepath.Dir(goPath)+":"+t.path())
	if _, exists := os.LookupEnv("GOFLAGS"); !exists {
		cmd.Env = append(cmd.Env, "GOFLAGS=-mod=mod")
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			if errors.Is(err, syscall.ESRCH) {
				return os.ErrProcessDone
			}
			return err
		}
		return nil
	}
	cmd.WaitDelay = 2 * time.Second
	var out tail
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	output := strings.ToValidUTF8(out.buffer.String(), "\uFFFD")
	if len(output) > outputLimit {
		output = strings.ToValidUTF8(output[len(output)-outputLimit:], "")
	}
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		err = errors.New("timeout")
	case ctx.Err() != nil:
		err = ctx.Err()
	default:
		var exited *exec.ExitError
		if errors.As(err, &exited) {
			return exited.ExitCode(), output, nil
		}
	}
	if err != nil {
		return -1, output, err
	}
	return 0, output, nil
}

func unpack(dir string, input io.Reader) error {
	gz, err := gzip.NewReader(input)
	if err != nil {
		return err
	}
	defer gz.Close()
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	tr := tar.NewReader(gz)
	var total int64
	for entries := 0; ; entries++ {
		h, err := tr.Next()
		if err == io.EOF {
			_, err := io.Copy(io.Discard, io.LimitReader(gz, 64<<10))
			return err
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(h.Name)
		if entries >= 100000 || !filepath.IsLocal(name) || h.Size < 0 || h.Size > (512<<20)-total {
			return errors.New("archive entry exceeds runner limits")
		}
		total += h.Size
		if err := root.MkdirAll(filepath.Dir(name), 0755); err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			err = root.MkdirAll(name, 0755)
		case tar.TypeSymlink:
			target := filepath.Join(filepath.Dir(name), h.Linkname)
			if h.Linkname == "" || filepath.IsAbs(h.Linkname) || !filepath.IsLocal(target) {
				return fmt.Errorf("invalid archive symlink %q", name)
			}
			err = root.Symlink(h.Linkname, name)
		case tar.TypeReg:
			var f *os.File
			f, err = root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, os.FileMode(h.Mode)&0777)
			if err == nil {
				_, err = io.Copy(f, tr)
				if closeErr := f.Close(); err == nil {
					err = closeErr
				}
			}
		default:
			return fmt.Errorf("unsupported archive entry %q: %d", name, h.Typeflag)
		}
		if err != nil {
			return err
		}
	}
}
