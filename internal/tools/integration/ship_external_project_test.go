package integration

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestExternalProviderCIRejectsCrossRepositoryEvidenceBeforeAnyToolCall(t *testing.T) {
	for _, provider := range []string{"gitverse", "auto", "distributed"} {
		t.Run(provider, func(t *testing.T) {
			called := 0
			out, err := Step(context.Background(), Env{},
				IntegrationShipInput{
					Path:           "/root/repos/venta-aw730-homeassistant",
					Branch:         "feat/v120-release-20261123",
					Commit:         strings.Repeat("a", 40),
					AdminSessionID: strings.Repeat("b", 32),
					Evidence:       provider,
				},
				func(_ context.Context, _ string, _ map[string]any) (map[string]any, error) {
					called++
					return nil, errors.New("forbidden external lookup")
				},
			)
			if err != nil || called != 0 || out.Done ||
				out.Stage != "CI_PROVENANCE_UNSUPPORTED" ||
				!strings.Contains(out.Detail, "MCP-269") {
				t.Fatalf("unsafe cross-repo result: out=%+v, err=%v, calls=%d", out, err, called)
			}
		})
	}
	// An arbitrary source must never inherit the compiled-in Venta profile.
	out, err := Step(context.Background(), Env{},
		IntegrationShipInput{Path: "/root/repos/other", Branch: "feat/x", Commit: strings.Repeat("a", 40),
			AdminSessionID: strings.Repeat("b", 32), Evidence: "ci"},
		func(_ context.Context, _ string, _ map[string]any) (map[string]any, error) {
			t.Fatal("unsafe foreign CI call")
			return nil, nil
		})
	if err != nil || out.Stage != "CI_PROVENANCE_UNSUPPORTED" {
		t.Fatalf("arbitrary external GitHub CI admitted: %+v %v", out, err)
	}
}

func TestVentaGitHubEvidenceIsDelegatedOnlyToRootSealer(t *testing.T) {
	called := 0
	_, err := Step(context.Background(), Env{},
		IntegrationShipInput{
			Path:           "/root/repos/venta-aw730-homeassistant",
			Branch:         "feat/v120-release-20261123",
			Commit:         strings.Repeat("a", 40),
			AdminSessionID: strings.Repeat("b", 32),
			Evidence:       "ci",
		},
		func(_ context.Context, tool string, _ map[string]any) (map[string]any, error) {
			called++
			if tool != "integration_status" {
				t.Fatalf("unexpected provider publisher or bypass: %s", tool)
			}
			return nil, errors.New("native root approval required")
		},
	)
	if called != 1 || err == nil || !strings.Contains(err.Error(), "native root approval") {
		t.Fatalf("trusted project did not reach root admission: called=%d err=%v", called, err)
	}
}

func TestExternalModuleModeStillReachesItsOwnToolGate(t *testing.T) {
	invoked := 0
	_, err := Step(context.Background(), Env{},
		IntegrationShipInput{
			Path:           "/root/repos/other-go-project",
			Branch:         "feat/test",
			Commit:         strings.Repeat("a", 40),
			AdminSessionID: strings.Repeat("b", 32),
			Evidence:       "module",
		},
		func(_ context.Context, tool string, _ map[string]any) (map[string]any, error) {
			invoked++
			if tool != "integration_status" {
				t.Fatalf("unexpected tool called: %s", tool)
			}
			return nil, errors.New("independent module gate")
		},
	)
	if invoked != 1 || err == nil || !strings.Contains(err.Error(), "independent module gate") {
		t.Fatalf("module path must reach its own root admission, invoked=%d err=%v", invoked, err)
	}
}
