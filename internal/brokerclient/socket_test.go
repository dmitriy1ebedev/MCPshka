package brokerclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sort"
	"testing"
	"time"
)

// This is a wire-protocol test. Importing rootbroker here would create a
// production dependency cycle (rootbroker -> projectlease -> brokerclient).
func testBrokerWire(t *testing.T, wantFamily string, wantArgs []string, response brokerResponse) {
	t.Helper()
	socket := fmt.Sprintf("@mcpshka-broker-test-%d-%d", os.Getpid(), time.Now().UnixNano())
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	old := brokerSocketPath
	brokerSocketPath = socket
	t.Cleanup(func() { brokerSocketPath = old })

	done := make(chan error, 1)
	go func() {
		conn, err := ln.AcceptUnix()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		var req brokerRequest
		if err := json.NewDecoder(conn).Decode(&req); err != nil {
			done <- err
			return
		}
		if req.Family != wantFamily || len(req.Args) != len(wantArgs) {
			done <- fmt.Errorf("unexpected request %+v", req)
			return
		}
		for i := range wantArgs {
			if req.Args[i] != wantArgs[i] {
				done <- fmt.Errorf("unexpected argument %d: %q", i, req.Args[i])
				return
			}
		}
		done <- json.NewEncoder(conn).Encode(response)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := Run(ctx, wantFamily, wantArgs...)
	if response.OK {
		if err != nil || string(out) != string(response.Output) {
			t.Fatalf("broker round trip: %q %v", out, err)
		}
	} else if err == nil {
		t.Fatal("broker error response was accepted")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestUnixSocketBrokerAllowsSystemdRunFamily(t *testing.T) {
	testBrokerWire(t, "systemd-run", []string{"sentinel"}, brokerResponse{OK: true, Output: []byte("accepted\n")})
}

func TestUnixSocketBrokerRoundTrip(t *testing.T) {
	testBrokerWire(t, "systemctl", []string{"is-active", "mcpshka.service"}, brokerResponse{OK: true, Output: []byte("active\n")})
}

func TestUnixSocketBrokerErrorResponse(t *testing.T) {
	testBrokerWire(t, "journalctl", []string{"-n", "1"}, brokerResponse{OK: false, Error: "permission denied"})
}

func TestUnixSocketBrokerHealthRoundTrip(t *testing.T) {
	testBrokerWire(t, "health", []string{"broker"}, brokerResponse{OK: true, Output: []byte(`{"version":"0.1.174"}`)})
}

// diag_wifi_scan shipped in 0.1.181 with its broker family missing here and
// failed before reaching the broker (BUG-20261024-102).
func TestUnixSocketBrokerAllowsWifiScanFamily(t *testing.T) {
	testBrokerWire(t, "wifi-scan", []string{"wlan0"}, brokerResponse{OK: true, Output: []byte(`{"bss_seen":3}`)})
}

// A synthetic socket peer cancels the request after receiving it, then holds
// the response for 20ms. No privileged broker or production payload is used.
func BenchmarkBrokerCancelledRoundTrip(b *testing.B) {
	socket := fmt.Sprintf("@mcpshka-cancel-bench-%d-%d", os.Getpid(), time.Now().UnixNano())
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		b.Fatal(err)
	}
	defer ln.Close()
	old := brokerSocketPath
	brokerSocketPath = socket
	defer func() { brokerSocketPath = old }()
	samples := make([]time.Duration, b.N)
	cancelled := 0
	var elapsed time.Duration
	b.ResetTimer()
	for i := range b.N {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		done := make(chan error, 1)
		go func() {
			conn, err := ln.AcceptUnix()
			if err != nil {
				done <- err
				return
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(time.Second))
			var req brokerRequest
			if err := json.NewDecoder(conn).Decode(&req); err != nil {
				done <- err
				return
			}
			cancel()
			time.Sleep(20 * time.Millisecond)
			// A cancelled client may already have closed its socket.
			_ = json.NewEncoder(conn).Encode(brokerResponse{OK: true, Output: []byte("ok")})
			done <- nil
		}()
		start := time.Now()
		_, err := Run(ctx, "health", "broker")
		samples[i] = time.Since(start)
		elapsed += samples[i]
		if err != nil {
			cancelled++
		}
		cancel()
		if err := <-done; err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	for _, p := range []int{50, 95, 99} {
		i := (len(samples)*p+99)/100 - 1
		b.ReportMetric(float64(samples[i].Nanoseconds()), fmt.Sprintf("p%d-ns/call", p))
	}
	b.ReportMetric(float64(b.N)/elapsed.Seconds(), "client-calls/s")
	b.ReportMetric(float64(cancelled)/float64(b.N), "cancelled/call")
}

func BenchmarkBrokerNormalRoundTrip(b *testing.B) {
	socket := fmt.Sprintf("@mcpshka-normal-bench-%d-%d", os.Getpid(), time.Now().UnixNano())
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		b.Fatal(err)
	}
	old := brokerSocketPath
	brokerSocketPath = socket
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := ln.AcceptUnix()
			if err != nil {
				return
			}
			_ = conn.SetDeadline(time.Now().Add(time.Second))
			var req brokerRequest
			if json.NewDecoder(conn).Decode(&req) == nil {
				_ = json.NewEncoder(conn).Encode(brokerResponse{OK: true, Output: []byte("ok")})
			}
			_ = conn.Close()
		}
	}()
	defer func() { ln.Close(); <-done; brokerSocketPath = old }()
	b.ResetTimer()
	for range b.N {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		out, err := Run(ctx, "health", "broker")
		cancel()
		if err != nil || string(out) != "ok" {
			b.Fatalf("roundtrip: %q %v", out, err)
		}
	}
}
