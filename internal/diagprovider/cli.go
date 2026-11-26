package diagprovider

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strconv"
	"time"
)

// CLIProbe is an explicitly read-only, bounded bridge to installed DIAG.
// It does not implement Backend or claim investigation protocol compatibility.
type CLIProbe struct {
	Binary  string
	Timeout time.Duration
}
type limitedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, ErrUnsafe
	}
	return b.Buffer.Write(p)
}
func (p CLIProbe) run(ctx context.Context, limit int, args ...string) ([]byte, error) {
	if p.Binary == "" || limit < 1 || limit > 1<<20 || p.Timeout <= 0 || p.Timeout > 30*time.Second {
		return nil, ErrUnsafe
	}
	c, cancel := context.WithTimeout(ctx, p.Timeout)
	defer cancel()
	cmd := exec.CommandContext(c, p.Binary, args...)
	out := &limitedBuffer{limit: limit}
	cmd.Stdout = out
	cmd.Stderr = &limitedBuffer{limit: 4096}
	if err := cmd.Run(); err != nil {
		if errors.Is(c.Err(), context.DeadlineExceeded) {
			return nil, c.Err()
		}
		return nil, errors.Join(ErrUnsafe, err)
	}
	return append([]byte(nil), out.Bytes()...), nil
}
func (p CLIProbe) Capabilities(ctx context.Context) (Capabilities, error) {
	data, err := p.run(ctx, 64<<10, "capabilities", "--json")
	if err != nil {
		return Capabilities{}, err
	}
	return ParseDIAGCapabilities(data)
}
func (p CLIProbe) AgentEvidence(ctx context.Context, budget int) (Evidence, error) {
	if budget < 256 || budget > 1<<20 {
		return Evidence{}, ErrUnsafe
	}
	data, err := p.run(ctx, budget, "agent", "--json", "--budget-bytes", strconv.Itoa(budget))
	if err != nil {
		return Evidence{}, err
	}
	return Evidence{Source: "diag-cli", Version: DIAGCapabilitiesSchema, Provenance: "diag agent --json", CapturedAt: time.Now().UTC(), Data: data, Untrusted: true}, nil
}
