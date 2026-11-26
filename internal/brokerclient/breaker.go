package brokerclient

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"os"
	"sync"
	"time"
)

// Circuit breaker per broker socket (MCP-070). Only an unreachable broker
// (cannot dial, cannot write) counts; a policy/security refusal is a healthy
// broker answering, and a slow answer on a busy host is overload, not an
// outage: it halves the number of concurrent calls (AIMD, as in adaptive
// concurrency limits) instead of opening the circuit (BUG-20261104-35).
// Once a request is sent, a lost reply requires reconciliation.
// While OPEN, calls fail fast instead of piling up 35 s waits; after the
// cooldown a HALF_OPEN dial-only probe (no request sent) closes the circuit.

const (
	limitStart       = 8
	limitMax         = 16
	breakerThreshold = 3
	breakerBase      = 15 * time.Second
	breakerMax       = 2 * time.Minute
)

// ErrCircuitOpen is the typed fast refusal while the broker is unreachable.
var ErrCircuitOpen = errors.New("BROKER_CIRCUIT_OPEN")

// BreakerState is the readiness view of one broker dependency.
type BreakerState struct {
	State     string    `json:"state"` // CLOSED, OPEN, HALF_OPEN
	Failures  int       `json:"consecutive_failures"`
	OpenUntil time.Time `json:"open_until,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	Limit     int       `json:"concurrency_limit,omitempty"`
	InFlight  int       `json:"in_flight,omitempty"`
}

// ErrBusy means the call never reached the broker: every slot stayed taken
// until the caller's deadline. Nothing happened, so a retry is safe.
var ErrBusy = errors.New("BROKER_BUSY")

type breaker struct {
	mu        sync.Mutex
	failures  int
	opens     int
	openUntil time.Time
	reason    string
	limit     float64
	inFlight  int
}

var (
	breakersMu  sync.Mutex
	breakers    = map[string]*breaker{}
	breakerNow  = time.Now
	breakerDial = func(path string) error {
		c, err := net.DialTimeout("unix", path, time.Second)
		if err == nil {
			c.Close()
		}
		return err
	}
)

func breakerFor(path string) *breaker {
	breakersMu.Lock()
	defer breakersMu.Unlock()
	b := breakers[path]
	if b == nil {
		b = &breaker{}
		breakers[path] = b
	}
	return b
}

// Breaker reports the circuit of the production broker socket.
func Breaker() BreakerState { return breakerFor(brokerSocketPath).state() }

func (b *breaker) state() BreakerState {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := BreakerState{State: "CLOSED", Failures: b.failures, Reason: b.reason, Limit: int(b.slots()), InFlight: b.inFlight}
	if b.failures >= breakerThreshold {
		s.State, s.OpenUntil = "OPEN", b.openUntil
		if !breakerNow().Before(b.openUntil) {
			s.State = "HALF_OPEN"
		}
	}
	return s
}

// admit fails fast while OPEN; in HALF_OPEN it probes the socket by dialing
// only, which can never cause an effect.
func (b *breaker) admit(path string) error {
	s := b.state()
	switch s.State {
	case "OPEN":
		return errors.Join(ErrCircuitOpen, errors.New("privileged broker unreachable ("+s.Reason+"); retry after "+s.OpenUntil.UTC().Format(time.RFC3339)+"; mutations are not retried automatically"))
	case "HALF_OPEN":
		if err := breakerDial(path); err != nil {
			b.fail("half-open probe: " + err.Error())
			return errors.Join(ErrCircuitOpen, err)
		}
		b.ok()
	}
	return nil
}

func (b *breaker) slots() float64 {
	if b.limit == 0 {
		b.limit = limitStart
	}
	return b.limit
}

// acquire waits for a free slot until the deadline.
func (b *breaker) acquire(ctx context.Context, deadline time.Time) error {
	for {
		b.mu.Lock()
		if float64(b.inFlight) < b.slots() {
			b.inFlight++
			b.mu.Unlock()
			return nil
		}
		n := b.inFlight
		b.mu.Unlock()
		if !time.Now().Before(deadline) || ctx.Err() != nil {
			return fmt.Errorf("%w: %d privileged calls in flight on a busy host; nothing was sent, retry later", ErrBusy, n)
		}
		select {
		case <-ctx.Done():
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// release frees the slot: a timely answer widens the limit by one slot per
// round, a slow one halves it.
func (b *breaker) release(slow bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.inFlight--
	if slow {
		b.limit = max(1, b.slots()/2)
	} else {
		b.limit = min(limitMax, b.slots()+1/b.slots())
	}
}

func (b *breaker) ok() {
	b.mu.Lock()
	b.failures, b.opens, b.reason = 0, 0, ""
	b.mu.Unlock()
}

func (b *breaker) fail(reason string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures++
	b.reason = reason
	if b.failures >= breakerThreshold {
		d := breakerBase << min(b.opens, 3)
		if d > breakerMax {
			d = breakerMax
		}
		d += time.Duration(rand.Int64N(int64(d / 5))) // jitter spreads reconnects
		b.openUntil = breakerNow().Add(d)
		b.opens++
	}
}

// transportFailure tells a broken transport from an answering broker.
func transportFailure(err error) bool {
	if err == nil {
		return false
	}
	var ne net.Error
	return errors.As(err, &ne) || errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrDeadlineExceeded)
}
