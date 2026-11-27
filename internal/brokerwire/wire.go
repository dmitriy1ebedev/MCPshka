// Package brokerwire defines the local broker response boundary.
package brokerwire

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"time"
)

const MaxResponseBytes = 128 * 1024

type Request struct {
	Family string   `json:"family"`
	Args   []string `json:"args"`
}

type Response struct {
	OK     bool   `json:"ok"`
	Code   string `json:"code,omitempty"`
	Output []byte `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
}

func WriteOutcome(ctx context.Context, w io.Writer, family string, started time.Time, resp Response, runErr error) {
	writeErr := WriteResponse(w, resp)
	reason := RunReason(ctx, runErr)
	if reason != "" || writeErr != nil {
		slog.Warn("broker request outcome", "family", family, "elapsed_ms", time.Since(started).Milliseconds(),
			"handler_reason", reason, "response_write_failed", writeErr != nil)
	}
}

// Classify without logging arguments, output, database paths or raw errors.
func RunReason(ctx context.Context, err error) string {
	if err != nil {
		msg := strings.ToLower(err.Error())
		switch {
		case strings.Contains(msg, "database or disk is full"), strings.Contains(msg, "sqlite_full"):
			return "sqlite_full"
		case strings.Contains(msg, "database is locked"), strings.Contains(msg, "database table is locked"), strings.Contains(msg, "sqlite_busy"), strings.Contains(msg, "sqlite_locked"):
			return "sqlite_busy"
		case errors.Is(err, context.DeadlineExceeded):
			return "handler_deadline"
		}
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "handler_deadline"
	}
	return ""
}

func WriteResponse(conn io.Writer, resp Response) error {
	data, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if len(data) > MaxResponseBytes {
		data, _ = json.Marshal(Response{OK: false, Error: "response size exceeded"})
		data = append(data, '\n')
	}
	_, err = conn.Write(data)
	return err
}
