package systemdsvc

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestStatusParsesBoundedShow(t *testing.T) {
	var got []string
	s := newService(map[string]struct{}{"demo.service": {}}, nil, nil, func(ctx context.Context, args ...string) ([]byte, error) {
		got = append([]string(nil), args...)
		return []byte("Id=demo.service\nLoadState=loaded\nActiveState=active\nSubState=running\nUnitFileState=enabled\nMainPID=123\nNRestarts=2\nExecMainStatus=0\n"), nil
	})
	st, err := s.Status(context.Background(), "demo.service")
	if err != nil {
		t.Fatal(err)
	}
	if st.Service != "demo.service" || st.ActiveState != "active" || st.MainPID != 123 || st.NRestarts != 2 {
		t.Fatalf("unexpected status: %+v", st)
	}
	if !slices.Contains(got, "demo.service") || !slices.Contains(got, "show") {
		t.Fatalf("unexpected argv: %#v", got)
	}
}

func TestListDependenciesAndFailedReason(t *testing.T) {
	calls := [][]string{}
	s := newService(
		map[string]struct{}{"a.service": {}, "b.service": {}},
		map[string]struct{}{"b.service": {}},
		nil,
		func(ctx context.Context, args ...string) ([]byte, error) {
			calls = append(calls, append([]string(nil), args...))
			if slices.Contains(args, "--property=Id,Requires,Wants,After,Before") {
				return []byte("Id=b.service\nRequires=a.service network.target\nWants=z.service\nAfter=a.service b.service\nBefore=\n"), nil
			}
			name := args[len(args)-1]
			if name == "a.service" {
				return []byte("Id=a.service\nLoadState=loaded\nActiveState=failed\nSubState=failed\nResult=exit-code\nStatusText=boom\nExecMainCode=exited\nExecMainStatus=7\n"), nil
			}
			return []byte("Id=b.service\nLoadState=loaded\nActiveState=active\nSubState=running\n"), nil
		},
	)
	list, err := s.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Services) != 2 || list.Services[0].Service != "a.service" || list.Services[0].Result != "exit-code" ||
		list.Services[0].ExecMainStatus != 7 || list.Services[0].StatusText != "boom" {
		t.Fatalf("unexpected list: %+v", list)
	}
	graph, err := s.Dependencies(context.Background(), "b.service")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(graph.Requires, "a.service") || !slices.Contains(graph.Requires, "network.target") ||
		!slices.Contains(graph.Wants, "z.service") {
		t.Fatalf("unexpected graph: %+v", graph)
	}
	if len(calls) < 3 {
		t.Fatalf("unexpected calls: %#v", calls)
	}
}

func TestActionRequiresMutationAllowlist(t *testing.T) {
	var actions []string
	s := newService(
		map[string]struct{}{"demo.service": {}},
		nil,
		map[string]struct{}{"demo.service": {}},
		func(ctx context.Context, args ...string) ([]byte, error) {
			if len(args) > 0 && (args[0] == "start" || args[0] == "stop" || args[0] == "restart") {
				actions = append(actions, args[0])
				return nil, nil
			}
			return []byte("Id=demo.service\nLoadState=loaded\nActiveState=active\nSubState=running\n"), nil
		},
	)
	for _, action := range []string{"start", "stop", "restart"} {
		if _, err := s.Action(context.Background(), "demo.service", action); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
	}
	if !slices.Equal(actions, []string{"start", "stop", "restart"}) {
		t.Fatalf("actions=%v", actions)
	}
	if _, err := s.Action(context.Background(), "demo.service", "reload-or-restart"); err == nil {
		t.Fatal("unsupported action accepted")
	}
	blocked := newService(map[string]struct{}{"demo.service": {}}, nil, nil, func(context.Context, ...string) ([]byte, error) {
		return nil, errors.New("must not run")
	})
	if _, err := blocked.Action(context.Background(), "demo.service", "start"); err == nil {
		t.Fatal("action without mutation allowlist accepted")
	}
}

func TestRestartRequiresSeparateAllowlist(t *testing.T) {
	calls := 0
	s := newService(
		map[string]struct{}{"demo.service": {}},
		map[string]struct{}{"demo.service": {}},
		nil,
		func(ctx context.Context, args ...string) ([]byte, error) {
			calls++
			if len(args) > 0 && args[0] == "restart" {
				return nil, nil
			}
			return []byte("Id=demo.service\nLoadState=loaded\nActiveState=active\nSubState=running\n"), nil
		},
	)
	st, err := s.Restart(context.Background(), "demo.service")
	if err != nil {
		t.Fatal(err)
	}
	// status + reverse-dependency probe (MCP-104) + restart
	if calls != 3 || st.ActiveState != "active" {
		t.Fatalf("calls=%d status=%+v", calls, st)
	}
	if _, err := s.Action(context.Background(), "demo.service", "start"); err == nil {
		t.Fatal("restart-only allowlist unexpectedly permits start")
	}

	blocked := newService(map[string]struct{}{"demo.service": {}}, nil, nil, func(context.Context, ...string) ([]byte, error) {
		return nil, errors.New("must not run")
	})
	if _, err := blocked.Restart(context.Background(), "demo.service"); err == nil {
		t.Fatal("restart without restart allowlist accepted")
	}
}

func TestSelfRestartAndInvalidNamesAreRejected(t *testing.T) {
	s := newService(
		map[string]struct{}{"mcpshka.service": {}},
		map[string]struct{}{"mcpshka.service": {}},
		map[string]struct{}{"mcpshka.service": {}},
		func(context.Context, ...string) ([]byte, error) { return nil, nil },
	)
	if _, err := s.Restart(context.Background(), "mcpshka.service"); err == nil {
		t.Fatal("self restart accepted")
	}
	for _, name := range []string{"", "../ssh.service", "-x.service", "ssh.socket", "x service.service"} {
		if validServiceName(name) {
			t.Fatalf("invalid name accepted: %q", name)
		}
	}
}

func TestLogsAreAllowlistedAndBounded(t *testing.T) {
	var gotMax int
	var gotArgs []string
	journal := func(ctx context.Context, maxBytes int, args ...string) ([]byte, bool, error) {
		gotMax = maxBytes
		gotArgs = append([]string(nil), args...)
		return []byte("line1\nline2\n"), true, nil
	}
	s := newService(
		map[string]struct{}{"demo.service": {}},
		nil,
		nil,
		func(context.Context, ...string) ([]byte, error) { return nil, nil },
		journal,
	)
	out, err := s.Logs(context.Background(), "demo.service", 48*time.Hour, 9999, 999999)
	if err != nil {
		t.Fatal(err)
	}
	if gotMax != MaxLogBytes || out.SinceSeconds != int64(MaxLogSince/time.Second) || len(out.Lines) != 2 || !out.Truncated {
		t.Fatalf("unexpected logs result: %+v max=%d", out, gotMax)
	}
	for _, want := range []string{"--unit=demo.service", "--lines=500"} {
		if !slices.Contains(gotArgs, want) {
			t.Fatalf("missing journal arg %q in %#v", want, gotArgs)
		}
	}
	if _, err := s.Logs(context.Background(), "blocked.service", time.Minute, 10, 1024); err == nil {
		t.Fatal("logs for non-allowlisted service accepted")
	}
}

func TestJournalReadSupportsCursorAndLimits(t *testing.T) {
	var gotMax int
	var gotArgs []string
	journal := func(ctx context.Context, maxBytes int, args ...string) ([]byte, bool, error) {
		gotMax = maxBytes
		gotArgs = append([]string(nil), args...)
		return []byte("2026-10-07T08:00:00+0300 one\n2026-10-07T08:00:01+0300 two\n-- cursor: s=abc;i=2;b=3;m=4;t=5;x=6\n"), true, nil
	}
	s := newService(
		map[string]struct{}{"demo.service": {}},
		nil,
		nil,
		func(context.Context, ...string) ([]byte, error) { return nil, nil },
		journal,
	)

	out, err := s.JournalRead(context.Background(), "demo.service", 48*time.Hour, 9999, 999999, "")
	if err != nil {
		t.Fatal(err)
	}
	if gotMax != MaxLogBytes || out.SinceSeconds != int64(MaxLogSince/time.Second) || len(out.Lines) != 2 ||
		out.NextCursor != "s=abc;i=2;b=3;m=4;t=5;x=6" || !out.Truncated {
		t.Fatalf("unexpected journal result: %+v max=%d", out, gotMax)
	}
	for _, want := range []string{"--unit=demo.service", "--show-cursor", "--lines=500"} {
		if !slices.Contains(gotArgs, want) {
			t.Fatalf("missing journal arg %q in %#v", want, gotArgs)
		}
	}
	if !slices.ContainsFunc(gotArgs, func(v string) bool { return strings.HasPrefix(v, "--since=@") }) {
		t.Fatalf("missing since arg in %#v", gotArgs)
	}

	gotArgs = nil
	out, err = s.JournalRead(context.Background(), "demo.service", time.Minute, 10, 2048, "s=abc;i=2;b=3;m=4;t=5;x=6")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(gotArgs, "--after-cursor=s=abc;i=2;b=3;m=4;t=5;x=6") {
		t.Fatalf("missing after-cursor arg in %#v", gotArgs)
	}
	if _, err := s.JournalRead(context.Background(), "demo.service", time.Minute, 10, 2048, "bad\ncursor"); err == nil {
		t.Fatal("invalid cursor accepted")
	}
	if _, err := s.JournalRead(context.Background(), "lane-hostbridge-xray.service", time.Minute, 10, 2048, ""); err != nil {
		t.Fatalf("Lane journal must follow its status read: %v", err)
	}
	if _, err := s.JournalRead(context.Background(), "wg-quick@wg0.service", time.Minute, 10, 2048, ""); err == nil {
		t.Fatal("other VPN journals stay configured-only")
	}
	if _, err := s.JournalRead(context.Background(), "blocked.service", time.Minute, 10, 2048, ""); err == nil {
		t.Fatal("journal for non-allowlisted service accepted")
	}
}

func TestEnvironmentListsAreValidated(t *testing.T) {
	t.Setenv("MCPSHKA_SYSTEMD_SERVICES", "alpha.service, ../bad.service")
	t.Setenv("MCPSHKA_SYSTEMD_RESTART_SERVICES", "beta.service,mcpshka.service")
	t.Setenv("MCPSHKA_SYSTEMD_MUTATION_SERVICES", "gamma.service,mcpshka.service")
	s := NewFromEnvironment()
	if _, ok := s.statusAllowed["alpha.service"]; !ok {
		t.Fatal("status allowlist missing environment service")
	}
	if _, ok := s.restartAllowed["beta.service"]; !ok {
		t.Fatal("restart allowlist missing environment service")
	}
	if _, ok := s.mutationAllowed["gamma.service"]; !ok {
		t.Fatal("mutation allowlist missing environment service")
	}
	if _, ok := s.restartAllowed["mcpshka.service"]; ok {
		t.Fatal("self restart should be removed from restart allowlist")
	}
	if _, ok := s.mutationAllowed["mcpshka.service"]; ok {
		t.Fatal("self mutation should be removed from mutation allowlist")
	}
}

// 27.10: go2rtc-ha-noise.service, installed by an approved plan, was
// POLICY_DENIED for status. A unit the plan declared is status-only.
func TestOwnedUnitIsStatusOnly(t *testing.T) {
	var seen []string
	s := newService(map[string]struct{}{"demo.service": {}}, nil, nil, func(ctx context.Context, args ...string) ([]byte, error) {
		seen = append(seen, args[len(args)-1])
		return []byte("Id=" + args[len(args)-1] + "\nLoadState=loaded\nActiveState=active\n"), nil
	})
	if _, err := s.Status(t.Context(), "go2rtc-ha-noise.service"); err == nil || !strings.Contains(err.Error(), "units") {
		t.Fatalf("unowned unit readable or unhelpful: %v", err)
	}
	s.owned = func(context.Context) map[string]string {
		return map[string]string{"go2rtc-ha-noise.service": "0123456789abcdef"}
	}
	st, err := s.Status(t.Context(), "go2rtc-ha-noise.service")
	if err != nil || st.ActiveState != "active" || st.OwnedBy != "approved_task:0123456789abcdef" {
		t.Fatalf("%+v %v", st, err)
	}
	if _, err := s.Restart(t.Context(), "go2rtc-ha-noise.service"); err == nil {
		t.Fatal("owned unit became mutable")
	}
	if _, err := s.Logs(t.Context(), "go2rtc-ha-noise.service", time.Minute, 10, 1024); err == nil {
		t.Fatal("owned unit journal became readable")
	}
	list, err := s.List(t.Context())
	if err != nil || len(list.Services) != 2 {
		t.Fatalf("service_list misses the owned unit: %+v %v", list, err)
	}
}
