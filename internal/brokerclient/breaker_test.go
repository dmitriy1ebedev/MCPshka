package brokerclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"testing"
	"time"
)

// MCP-070: transport failures open the circuit, calls then fail fast with a
// typed error, a dial-only HALF_OPEN probe closes it again, and a policy
// refusal from a live broker never counts as a failure.
func TestBreakerOpensOnTransportNotPolicy(t *testing.T) {
	socket := fmt.Sprintf("@mcpshka-breaker-test-%d-%d", os.Getpid(), time.Now().UnixNano())
	old, oldNow := brokerSocketPath, breakerNow
	brokerSocketPath = socket
	now := time.Now()
	breakerNow = func() time.Time { return now }
	t.Cleanup(func() { brokerSocketPath, breakerNow = old, oldNow })
	ctx := context.Background()
	for i := 0; i < breakerThreshold; i++ {
		if _, err := Run(ctx, "health"); err == nil || errors.Is(err, ErrCircuitOpen) {
			t.Fatalf("attempt %d: want a dial error, got %v", i, err)
		}
	}
	if s := Breaker(); s.State != "OPEN" || s.Failures != breakerThreshold || s.OpenUntil.IsZero() {
		t.Fatalf("state after failures: %+v", s)
	}
	start := time.Now()
	if _, err := Run(ctx, "root-task", "x"); !errors.Is(err, ErrCircuitOpen) || time.Since(start) > time.Second {
		t.Fatalf("OPEN must fail fast with the typed error: %v", err)
	}
	now = now.Add(breakerMax + time.Minute)
	if _, err := Run(ctx, "health"); !errors.Is(err, ErrCircuitOpen) || Breaker().State != "OPEN" {
		t.Fatalf("failed half-open probe must reopen: %v %+v", err, Breaker())
	}

	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for i := 0; i < 3; i++ {
			conn, err := ln.AcceptUnix()
			if err != nil {
				return
			}
			var req brokerRequest
			if json.NewDecoder(conn).Decode(&req) == nil {
				_ = json.NewEncoder(conn).Encode(brokerResponse{OK: false, Error: "POLICY_DENIED"})
			}
			conn.Close()
		}
	}()
	now = now.Add(breakerMax + time.Minute)
	if _, err := Run(ctx, "root-task", "x"); err == nil || err.Error() != "POLICY_DENIED" {
		t.Fatalf("after a good probe the request reaches the broker: %v", err)
	}
	if s := Breaker(); s.State != "CLOSED" || s.Failures != 0 {
		t.Fatalf("a policy refusal is a healthy broker: %+v", s)
	}
}
