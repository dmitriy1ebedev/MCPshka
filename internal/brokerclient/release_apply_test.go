package brokerclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"mcpshka/internal/releaseapply"
)

func TestReleaseApplyLostReplyReadsReport(t *testing.T) {
	for _, mode := range []string{"eof", "partial", "timeout", "status", "delayed"} {
		t.Run(mode, func(t *testing.T) {
			oldSocket, oldDir := brokerSocketPath, releaseapply.ResultDir
			t.Cleanup(func() { brokerSocketPath, releaseapply.ResultDir = oldSocket, oldDir })
			releaseapply.ResultDir = t.TempDir()
			brokerSocketPath = fmt.Sprintf("@apply-%d-%d", os.Getpid(), time.Now().UnixNano())
			ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: brokerSocketPath, Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			done := make(chan error, 1)
			report := []byte(`{"version":"0.13.5","state":"APPLYING","unit":"mcpshka-release-apply-0135"}`)
			b := breakerFor(brokerSocketPath)
			b.limit = 1 // status must not retain the mutation's only slot
			go func() {
				conn, err := ln.AcceptUnix()
				if err != nil {
					done <- err
					return
				}
				var req brokerRequest
				err = json.NewDecoder(conn).Decode(&req)
				if err == nil && mode != "status" && mode != "delayed" {
					err = releaseapply.SaveResult("0.13.5", report)
				}
				if mode == "timeout" {
					time.Sleep(150 * time.Millisecond)
				}
				if mode == "partial" {
					_, err = conn.Write([]byte(`{"ok":tr`))
				}
				conn.Close()
				if err == nil && (mode == "status" || mode == "delayed") {
					conn, err = ln.AcceptUnix()
					if err == nil {
						defer conn.Close()
						err = json.NewDecoder(conn).Decode(&req)
						if err == nil && req.Family != "release-apply-status" {
							err = fmt.Errorf("mutation replayed: %+v", req)
						}
						state := "SUCCEEDED"
						if mode == "delayed" {
							state = "NOT_STARTED"
							if err == nil {
								err = releaseapply.SaveResult("0.13.5", report)
							}
						}
						raw, _ := json.Marshal(map[string]string{"state": state, "version": "0.13.5", "unit": "mcpshka-release-apply-0135", "reported_at": time.Now().UTC().Format(time.RFC3339Nano)})
						if err == nil {
							err = json.NewEncoder(conn).Encode(brokerResponse{OK: true, Output: raw})
						}
					}
				}
				done <- err
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if mode == "timeout" {
				ctx, cancel = context.WithTimeout(ctx, 50*time.Millisecond)
				defer cancel()
			}
			raw, err := Run(ctx, "admin-session", "release-apply", "session", "owner", "0.13.5", "store", "build", "test")
			if err != nil {
				t.Fatal(err)
			}
			var out map[string]string
			if json.Unmarshal(raw, &out) != nil || out["unit"] != "mcpshka-release-apply-0135" || (out["state"] != "APPLYING" && out["state"] != "SUCCEEDED") {
				t.Fatal(string(raw))
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if b.state().State != "CLOSED" || b.state().Failures != 0 {
				t.Fatal(b.state())
			}
		})
	}
}
