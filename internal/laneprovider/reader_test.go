package laneprovider

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMCP240NativeRouterReaderOnlyPureReadVerbs(t *testing.T) {
	seen:=map[string]int{}
	reader:=&Reader{call:func(_ context.Context,operation string, params map[string]any)(json.RawMessage,error) {
		if len(params)!=0 {t.Fatal("read-only router unexpectedly received params",operation,params)}
		seen[operation]++
		return json.RawMessage(`{"ok":true,"actual_mutation":false,"result":{"driver":"be900"}}`),nil
	}}
	for _,operation:=range []string{"lane.router.status","lane.router.wifi","lane.router.clients","lane.router.history"} {
		if !ReadOperations[operation] {t.Fatal("required read op not allowed",operation)}
		out,err:=reader.Read(context.Background(),operation,nil)
		if err!=nil || !strings.Contains(string(out),`"actual_mutation":false`) {t.Fatal(operation,err,string(out))}
	}
	for _,operation:=range []string{"lane.router.apply","lane.router.plan","lane.router.snapshot","lane.router.wifiwatch","lane.router.rollback","lane.router.device-ip"} {
		if _,err:=reader.Read(context.Background(),operation,nil);err==nil ||
			!strings.Contains(err.Error(),"LANE_READ_FORBIDDEN") {t.Fatal("unsafe router op was not fenced",operation,err)}
	}
	if len(seen)!=4 {t.Fatal("not all safe read ops were routed exactly once",seen)}
}

func TestReaderRefusesNonReadOperations(t *testing.T) {
	r := &Reader{call: func(context.Context, string, map[string]any) (json.RawMessage, error) {
		t.Fatal("forbidden operation reached the socket")
		return nil, nil
	}}
	for _, op := range []string{"canary.action", "lane.router.apply", "lane.device.plan"} {
		if _, err := r.Read(context.Background(), op, nil); err == nil || !strings.Contains(err.Error(), "LANE_READ_FORBIDDEN") {
			t.Fatalf("%s: %v", op, err)
		}
	}
}

func TestReaderMissingSocketIsUnavailable(t *testing.T) {
	r := &Reader{call: boundedCaller(filepath.Join(t.TempDir(), "absent.sock"), maxReaderResponse, time.Second)}
	if _, err := r.Read(context.Background(), "lane.device.list", nil); !errors.Is(err, ErrReaderUnavailable) {
		t.Fatalf("want unavailable, got %v", err)
	}
}

// A diag export is larger than the 64 KiB writer-API limit.
func TestReaderAcceptsLargeExport(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "r.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	big := strings.Repeat("x", 300<<10)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = bufio.NewReader(c).ReadString('\n')
		_, _ = c.Write([]byte("{\"ok\":true,\"result\":{\"schema\":\"lane.diag-export/v1\",\"pad\":\"" + big + "\"}}\n"))
	}()
	out, err := (&Reader{call: boundedCaller(sock, maxReaderResponse, 5*time.Second)}).Read(context.Background(), "lane.diag.export", map[string]any{"since": "24h"})
	if err != nil || !strings.Contains(string(out), "lane.diag-export/v1") {
		t.Fatalf("%v %d", err, len(out))
	}
}
