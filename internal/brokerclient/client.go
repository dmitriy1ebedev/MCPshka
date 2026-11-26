package brokerclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mcpshka/internal/brokerwire"
	"mcpshka/internal/failure"
	"mcpshka/internal/hostload"
	"net"
	"os"
	"strings"
	"syscall"
	"time"
)

const maxBytes = 64 * 1024

const maxBrokerResponseBytes = brokerwire.MaxResponseBytes

type brokerRequest = brokerwire.Request
type brokerResponse = brokerwire.Response

var brokerSocketPath = "/run/mcpshka-broker/broker.sock"

func Enabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("MCPSHKA_PRIVILEGED_BROKER")))
	return v == "1" || v == "true" || v == "yes"
}

func Run(ctx context.Context, family string, args ...string) ([]byte, error) {
	switch family {
	case "lane", "process-signal", "storage", "health", "systemctl", "journalctl", "docker", "docker-terminal", "systemd-run", "diag-traffic", "state-backup", "codex", "rescue", "rootledger-reap", "executor", "release-probe", "release-apply-status", "root-task", "passkey", "admin-session", "historical-fence", "wifi-scan", "diag-observer", "docker-smoke", "approved-task", "ha-config", "smb-share-status", "agent-login", "executor-self-heal", "containers", "cleanup":
	default:
		return nil, errors.New("unsupported privileged broker family")
	}
	plannedRestart := family == "release-apply-status" || family == "admin-session" && len(args) == 7 && args[0] == "release-apply"
	path := brokerSocketPath
	b := breakerFor(path)
	if err := b.admit(path); err != nil {
		return nil, failure.NewHold(failure.Disconnected, "BROKER_CIRCUIT_OPEN", "brokerclient.admit", "request not sent; wait for broker readiness", err)
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(defaultDeadline())
	}
	if err := b.acquire(ctx, deadline); err != nil {
		return nil, failure.NewHold(failure.OwnerBusy, "BROKER_BUSY", "brokerclient.acquire", "request not sent; wait for a free broker slot", err)
	}
	slow, released := false, false
	defer func() {
		if !released {
			b.release(slow)
		}
	}()
	dialCtx, cancelDial := context.WithDeadline(ctx, deadline)
	conn, err := dialBroker(dialCtx, path)
	dialCanceled := dialCtx.Err() != nil
	cancelDial()
	if err != nil {
		if !plannedRestart && !dialCanceled && transportFailure(err) {
			b.fail("dial: " + err.Error())
		}
		slog.Warn("broker socket failure", "family", family, "phase", "dial", "error", err)
		return nil, failure.NewHold(failure.Disconnected, "BROKER_DIAL_FAILED", "brokerclient.dial", "request not sent; inspect broker readiness", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(deadline)

	started := time.Now()
	req := brokerRequest{Family: family, Args: append([]string(nil), args...)}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	payload = append(payload, '\n')
	if _, err := conn.Write(payload); err != nil {
		if errors.Is(err, os.ErrDeadlineExceeded) {
			slow = true
		} else if !plannedRestart {
			b.fail("write: " + err.Error())
		}
		return nil, responseLost(family, "write", started, err)
	}
	if unix, ok := conn.(*net.UnixConn); ok {
		_ = unix.CloseWrite()
	}

	raw, err := io.ReadAll(io.LimitReader(conn, maxBrokerResponseBytes+1))
	if err != nil {
		if errors.Is(err, os.ErrDeadlineExceeded) {
			slow = true // the broker took the request: busy, not down
		} else if !plannedRestart && transportFailure(err) {
			b.fail("no response: " + err.Error())
		}
		if family == "admin-session" && plannedRestart {
			b.release(slow)
			released = true
			return recoverReleaseApply(ctx, args[3], started)
		}
		return nil, responseLost(family, "read", started, err)
	}
	if len(raw) == 0 {
		// The broker closed without answering — e.g. release_apply restarted
		// it mid-request (MCP-152, 0.9.4 installed fine). Not a verified
		// failure: the effect is unknown until probed.
		if family == "admin-session" && plannedRestart {
			b.release(slow)
			released = true
			return recoverReleaseApply(ctx, args[3], started)
		}
		return nil, responseLost(family, "read", started, io.ErrUnexpectedEOF)
	}
	if len(raw) > maxBrokerResponseBytes {
		return nil, responseLost(family, "decode", started, errors.New("invalid broker response size"))
	}
	var resp brokerResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		if family == "admin-session" && plannedRestart {
			b.release(slow)
			released = true
			return recoverReleaseApply(ctx, args[3], started)
		}
		return nil, responseLost(family, "decode", started, err)
	}
	b.ok() // a complete response proves the transport is healthy
	out, truncated := Cap(resp.Output, maxBytes)
	if truncated {
		return out, errors.New("privileged broker client output exceeded limit")
	}
	if !resp.OK {
		msg := strings.TrimSpace(resp.Error)
		if msg == "" {
			msg = "privileged broker request failed"
		}
		if resp.Code == "broker_busy_before_dispatch" {
			slow = true
			return nil, failure.NewHold(failure.OwnerBusy, "BROKER_BUSY", "brokerclient.admission", "broker rejected the request before dispatch", ErrBusy)
		}
		return out, errors.New(msg)
	}
	return out, nil
}

// A refused or absent socket can recover during a broker restart. Only dial
// again while no connection has been handed to the request writer.
func dialBroker(ctx context.Context, path string) (net.Conn, error) {
	dialer := net.Dialer{Timeout: 3 * time.Second}
	conn, err := dialer.DialContext(ctx, "unix", path)
	if !errors.Is(err, syscall.ECONNREFUSED) && !errors.Is(err, os.ErrNotExist) {
		return conn, err
	}
	timer := time.NewTimer(100 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return dialer.DialContext(ctx, "unix", path)
	}
}

func responseLost(family, phase string, started time.Time, err error) error {
	code := "BROKER_RESPONSE_LOST"
	if phase == "write" {
		code = "BROKER_WRITE_UNCERTAIN"
	} else if phase == "read" && errors.Is(err, os.ErrDeadlineExceeded) {
		code = "BROKER_RESPONSE_TIMEOUT"
	}
	slog.Warn("broker socket failure", "family", family, "phase", phase, "reason", code, "elapsed_ms", time.Since(started).Milliseconds())
	return failure.NewHold(failure.FencedUnknown, code, "brokerclient."+phase, "request may have executed; reconcile the original operation before retry", err)
}

// defaultDeadline is 35 s, stretched up to twice on an oversubscribed host.
func defaultDeadline() time.Duration {
	r := hostload.Read()
	f := min(max(r.Load1/float64(r.CPUs), 1), 2)
	return time.Duration(float64(35*time.Second) * f)
}

func Cap(data []byte, limit int) ([]byte, bool) {
	if limit < 0 {
		limit = 0
	}
	if len(data) <= limit {
		return append([]byte(nil), data...), false
	}
	return append([]byte(nil), data[:limit]...), true
}
