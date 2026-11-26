package brokerclient

import (
	"context"
	"errors"
	"fmt"
	"mcpshka/internal/failure"
	"net"
	"os"
	"testing"
	"time"
)

// BUG-20261104-35: on a busy host the broker answers late. That is overload,
// not an outage: the circuit stays closed, the concurrency limit halves, and
// a caller that finds no free slot gets BROKER_BUSY without sending anything.
func TestSlowBrokerShrinksLimitNotCircuit(t *testing.T) {
	socket := fmt.Sprintf("@mcpshka-slow-test-%d-%d", os.Getpid(), time.Now().UnixNano())
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	old := brokerSocketPath
	brokerSocketPath = socket
	t.Cleanup(func() { brokerSocketPath = old })
	go func() {
		for {
			conn, err := ln.AcceptUnix()
			if err != nil {
				return
			}
			go func() { time.Sleep(2 * time.Second); conn.Close() }() // never answers in time
		}
	}()
	for range 4 {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		_, err := Run(ctx, "health")
		cancel()
		detail := failure.Details(err)
		if detail == nil || detail.Class != failure.FencedUnknown || detail.Code != "BROKER_RESPONSE_TIMEOUT" || detail.NoEffect {
			t.Fatalf("slow call must preserve the uncertain effect and identify the response timeout: %v", err)
		}
	}
	s := Breaker()
	if s.State != "CLOSED" || s.Failures != 0 || s.Limit != 1 {
		t.Fatalf("slow answers must shrink the limit, not open the circuit: %+v", s)
	}
	b := breakerFor(socket)
	if err := b.acquire(context.Background(), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if _, err := Run(ctx, "health"); !errors.Is(err, ErrBusy) || time.Since(start) > time.Second {
		t.Fatalf("no free slot must be BROKER_BUSY: %v", err)
	}
	b.release(false)
	if s := Breaker(); s.InFlight != 0 || s.Limit != 2 {
		t.Fatalf("a timely release widens the limit: %+v", s)
	}
}
