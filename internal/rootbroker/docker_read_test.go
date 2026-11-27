package rootbroker

import (
	"strings"
	"testing"
)

func TestDockerReadScopeIsSeparateAndExact(t *testing.T) {
	cfg := testConfig()
	args := []string{"ps", "-a", "--no-trunc", "--format", "{{json .}}"}
	cfg.DockerRead = nil
	if err := validateDocker(cfg, args); err == nil || !strings.Contains(err.Error(), "MCPSHKA_DOCKER_READ_CONTAINERS") || !strings.Contains(err.Error(), "HOLD") {
		t.Fatalf("empty read scope must fail with a configuration diagnosis: %v", err)
	}
	raw := []byte("{\"Names\":\"homeassistant\",\"State\":\"running\"}\n{\"Names\":\"homeassistant-secret\"}\n{\"Names\":\"mosquitto\"}\n")
	out, err := filterDockerStatus([]string{"homeassistant"}, raw)
	if err != nil || strings.Contains(string(out), "secret") || strings.Contains(string(out), "mosquitto") || !strings.Contains(string(out), "running") {
		t.Fatalf("scope leak: %s %v", out, err)
	}
	for _, bad := range []string{"warning", `{"State":"running"}`, `{"Names":"homeassistant,secret"}`} {
		if out, err := filterDockerStatus([]string{"homeassistant"}, []byte(bad)); err == nil || len(out) != 0 {
			t.Fatal("unknown status identity admitted")
		}
	}
}
