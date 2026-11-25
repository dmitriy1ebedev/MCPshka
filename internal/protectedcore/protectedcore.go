package protectedcore

import (
	"path/filepath"
	"strings"
)

type Component struct {
	Name           string   `json:"name"`
	RuntimePaths   []string `json:"runtime_paths,omitempty"`
	SourcePrefixes []string `json:"source_prefixes,omitempty"`
}

var components = []Component{
	{
		Name: "auth",
		RuntimePaths: []string{
			"/etc/mcpshka/auth.json",
			"/etc/mcpshka/mcp-auth-header",
			"/etc/mcpshka/mcpshka.env",
		},
		SourcePrefixes: []string{"internal/authn/"},
	},
	{
		Name:           "policy",
		SourcePrefixes: []string{"internal/policy/"},
	},
	{
		Name:           "approval",
		SourcePrefixes: []string{"internal/approval/"},
	},
	{
		Name: "updater",
		RuntimePaths: []string{
			"/usr/local/bin/mcpshka",
			"/usr/local/bin/mcpshka.previous",
			"/etc/systemd/system/mcpshka.service",
			"/usr/local/libexec/mcpshka-self-deploy",
			"/usr/local/libexec/mcpshka-release-probe.py",
			"/usr/local/libexec/mcpshka-state-store.py",
			"/usr/local/libexec/mcpshka-migrate-service-user",
			"/usr/local/libexec/mcpshka-migrate-auth-registry.py",
			"/usr/local/libexec/mcpshka-user.service",
		},
		SourcePrefixes: []string{
			"internal/deploy/",
			"packaging/native/install.sh",
			"packaging/native/self-deploy.sh",
			"packaging/native/release-probe.py",
			"packaging/native/rollback.sh",
			"packaging/native/state-store.py",
			"packaging/native/migrate-service-user.sh",
			"packaging/native/migrate-auth-registry.py",
			"packaging/systemd/",
		},
	},
	{
		Name: "rescue",
		RuntimePaths: []string{
			"/usr/local/bin/mcpshka-rescue",
			"/etc/systemd/system/mcpshka-rescue.service",
		},
		SourcePrefixes: []string{
			"internal/rescue/",
			"cmd/mcpshka-rescue/",
		},
	},
	{
		Name: "privileged-broker",
		RuntimePaths: []string{
			"/usr/local/libexec/mcpshka-broker",
			"/usr/local/libexec/mcpshka-container-terminal",
			"/usr/local/libexec/mcpshka-install-broker",
			"/usr/local/libexec/mcpshka-codex-bridge.py",
			"/usr/local/libexec/diag-traffic-route-observe",
			"/etc/mcpshka/broker.json",
			"/etc/systemd/system/mcpshka-broker.service",
			"/run/mcpshka-broker",
			"/var/lib/diag/traffic-runs",
			"/var/lib/mcpshka-executor",
		},
		SourcePrefixes: []string{
			"cmd/mcpshka-broker/",
			"cmd/mcpshka-container-terminal/",
			"internal/containerterminal/",
			"internal/rootbroker/",
			"internal/releaseapply/",
			"internal/hostevents/",
			"internal/brokerdocker/",
			"internal/tools/cloud/",
			"internal/tools/knowledgetools/",
			"internal/tools/ownerautomation/",
			"internal/tools/networktools/",
			"internal/tools/orchestration/",
			"internal/tools/forgeops/",
			"internal/tools/releaseops/",
			"internal/tools/taskplan/",
			"internal/tools/adminops/",
			"internal/tools/hostops/",
			"internal/tools/fstools/",
			"internal/tools/projectops/",
			"internal/tools/capabilities/",
			"internal/tools/integration/",
			"internal/tools/issues/",
			"internal/tools/forge/",
			"internal/tools/recovery/",
			"internal/tools/workyield/",
			"internal/tools/health/",
			"internal/tools/roottask/",
			"internal/executor/",
			"internal/projectenv/",
			"internal/gitobjects/",
			"internal/cievidence/",
			"internal/brokerclient/",
			"internal/brokerwire/",
			"packaging/native/install-broker.sh",
			"packaging/native/codex-bridge.py",
			"packaging/native/diag-traffic-route-observe.py",
			"packaging/systemd/mcpshka-broker.service",
		},
	},
	{
		Name:           "protected-core-manifest",
		SourcePrefixes: []string{"internal/protectedcore/"},
	},
}

func Components() []Component {
	out := make([]Component, len(components))
	for i, item := range components {
		out[i] = Component{
			Name:           item.Name,
			RuntimePaths:   append([]string(nil), item.RuntimePaths...),
			SourcePrefixes: append([]string(nil), item.SourcePrefixes...),
		}
	}
	return out
}

func RuntimePathProtected(path string) bool {
	clean := filepath.Clean(strings.TrimSpace(path))
	if clean == "." || clean == "" {
		return false
	}
	for _, item := range components {
		for _, protected := range item.RuntimePaths {
			p := filepath.Clean(protected)
			if clean == p || strings.HasPrefix(clean, p+string(filepath.Separator)) {
				return true
			}
		}
	}
	return false
}

func SourcePathProtected(path string) bool {
	clean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(path)))
	if clean == "." || clean == "" || strings.HasPrefix(clean, "../") {
		return false
	}
	for _, item := range components {
		for _, prefix := range item.SourcePrefixes {
			p := filepath.ToSlash(filepath.Clean(prefix))
			if strings.HasSuffix(prefix, "/") {
				p += "/"
			}
			if clean == strings.TrimSuffix(p, "/") || strings.HasPrefix(clean, p) {
				return true
			}
		}
	}
	return false
}
