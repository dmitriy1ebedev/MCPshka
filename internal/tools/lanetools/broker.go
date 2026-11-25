package lanetools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// Only fixed Lane recipes need more than the broker's short default.
func Timeout(family string, args []string, fallback time.Duration) time.Duration {
	if family == "lane" || family == "admin-session" && len(args) > 0 && args[0] == "lane" {
		return 17 * time.Minute
	}
	return fallback
}

// The socket must remain writable until the bounded Lane process returns.
func RequestContext(ctx context.Context, conn net.Conn, family string, args []string, fallback time.Duration) (context.Context, context.CancelFunc) {
	budget := Timeout(family, args, fallback)
	_ = conn.SetDeadline(time.Now().Add(budget + 5*time.Second))
	return context.WithTimeout(ctx, budget)
}

// Admin keeps the existing Face ID session lock for the complete Lane call.
func Admin[T any](ctx context.Context, args []string, authorize func([]string, time.Time) (*os.File, T, error)) ([]byte, error) {
	if len(args) != 4 || args[0] != "lane" {
		return nil, errors.New("invalid Lane administrative recipe")
	}
	lock, _, err := authorize(args, time.Now())
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	return dispatch(ctx, args[3], true)
}

func Read(ctx context.Context, args []string) ([]byte, error) {
	if len(args) != 1 {
		return nil, errors.New("one Lane recipe required")
	}
	return dispatch(ctx, args[0], false)
}

func dispatch(ctx context.Context, raw string, authorized bool) ([]byte, error) {
	if len(raw) > 20<<10 {
		return nil, errors.New("Lane recipe exceeds limit")
	}
	var in Input
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&in) != nil || d.Decode(new(any)) != io.EOF {
		return nil, errors.New("invalid Lane recipe JSON")
	}
	args, stdin, mutation, err := recipe(in)
	if err != nil {
		return nil, err
	}
	if mutation && !authorized {
		return nil, errors.New("LANE_FACE_ID_REQUIRED")
	}
	if strings.HasPrefix(in.Action, "router ") {
		lock, e := os.OpenFile("/run/mcpshka-broker/lane-router.lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
		if e != nil {
			return nil, e
		}
		defer lock.Close()
		if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
			return []byte(`{"ok":false,"code":"BE900_SESSION_BUSY","error":"Сеанс роутера занят; дождитесь завершения текущей операции."}`), nil
		}
		defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	}
	binary := "/usr/local/lib/lane/lane-go"
	if st, e := os.Stat(binary); e != nil || st.Mode()&0111 == 0 {
		binary = "/opt/lane/current/lane"
	}
	// A transient unit leaves the broker's read-only sandbox; Lane owns rollback.
	unit := []string{"--quiet", "--wait", "--pipe", "--collect", "--property=Type=exec", "--property=RuntimeMaxSec=900s", "--property=MemoryMax=384M", "--property=TasksMax=64", "--property=CPUQuota=100%", "--", binary}
	if strings.HasPrefix(in.Action, "router ") {
		// Survives a lost broker reply: a second router unit cannot start.
		unit = append([]string{"--unit=mcpshka-lane-router"}, unit...)
	}
	return execute(ctx, "/usr/bin/systemd-run", append(unit, args...), stdin)
}

type output struct{ bytes.Buffer }

func (b *output) Write(p []byte) (int, error) {
	if len(p) > (64<<10)-b.Len() {
		return 0, errors.New("Lane output exceeds 64 KiB")
	}
	return b.Buffer.Write(p)
}

func execute(ctx context.Context, binary string, args []string, stdin []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 16*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "HOME=/root", "LANG=C.UTF-8"}
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.WaitDelay = 5 * time.Second
	var stdout, stderr output
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if binary == "/usr/bin/systemd-run" && strings.Contains(strings.Join(args, " "), "--unit=mcpshka-lane-router") && (strings.Contains(stderr.String(), "already exists") || strings.Contains(stderr.String(), "already running")) {
		return []byte(`{"ok":false,"code":"BE900_SESSION_BUSY","error":"Предыдущая операция роутера ещё выполняется; дождитесь её завершения."}`), nil
	}
	// Lane reports errors (including BE900_SESSION_BUSY) as JSON on stdout.
	if json.Valid(stdout.Bytes()) {
		return stdout.Bytes(), nil
	}
	if err != nil {
		return nil, errors.New("LANE_EXECUTION_UNKNOWN: inspect Lane status/history before retrying")
	}
	return nil, errors.New("LANE_INVALID_JSON")
}
