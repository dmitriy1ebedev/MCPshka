package laneprovider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"syscall"
	"time"
)

// ReaderSocket is Lane 2.0's read-only API socket (group mcpshka): MCPshka
// reads Lane natively without the root broker. The Lane side refuses every
// operation that is not READ, so this end only narrows it further.
const (
	ReaderSocket      = "/run/lane-read/api.sock"
	maxReaderResponse = 4 << 20
)

// ReadOperations are the Lane reads MCPshka may call on the reader socket.
var ReadOperations = map[string]bool{
	"lane.diag.export": true,
	"lane.device.list": true,
	"lane.device.show": true,
	// MCP-240: fixed pure BE900 reads through the native Lane reader socket.
	"lane.router.status": true,
	"lane.router.wifi": true,
	"lane.router.clients": true,
	"lane.router.history": true,
}

// ErrReaderUnavailable means the reader socket is absent (Lane before 2.0);
// callers fall back to the root broker recipe.
var ErrReaderUnavailable = errors.New("LANE_READER_UNAVAILABLE")

type Reader struct{ call caller }

func NewReader() *Reader {
	return &Reader{call: boundedCaller(ReaderSocket, maxReaderResponse, 45*time.Second)}
}

// MCP-240: BE900 read-only login may take up to 235 seconds on a busy
// router. Keep the ordinary Lane reader at 45s; only exact native router
// operations receive a longer transport deadline.
const routerReadTimeout = 250 * time.Second

func NewRouterReader() *Reader {
	return &Reader{call: boundedCaller(ReaderSocket, maxReaderResponse, routerReadTimeout)}
}

// Read returns the Lane "result" of a whitelisted read operation.
func (r *Reader) Read(ctx context.Context, operation string, params map[string]any) (json.RawMessage, error) {
	if !ReadOperations[operation] {
		return nil, fmt.Errorf("LANE_READ_FORBIDDEN: %s is not a reader operation", operation)
	}
	if params == nil {
		params = map[string]any{}
	}
	out, err := r.call(ctx, operation, params)
	if errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED) {
		return nil, fmt.Errorf("%w: %v", ErrReaderUnavailable, err)
	}
	return out, err
}
