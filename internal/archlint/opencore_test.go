package archlint

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Open core (0.19 bridge, owner 24.11): the public core must not grow new
// dependencies on the owner's home add-ons (Lane, Home Assistant, Venta,
// webOS, go2rtc media, DIAG). privatePackages are the add-ons; coreImporters
// are today's core files that still wire them in — a ratchet: the list may
// only shrink as wiring moves behind plugin registration.
var privatePackages = []string{
	"lanelink", "laneprovider", "haconfig", "haprovider", "ventagit", "webos", "mediapipe", "wifiscan",
	"diagcontract", "diagobserver", "diagprovider", "diagrelease", "tools/hatools", "tools/lanetools", "tools/webosops",
}

var coreImporters = map[string]bool{
	"cmd/mcpshka/main.go": true, "internal/hosttriage/triage.go": true, "internal/knowledgeops/knowledge.go": true,
	"internal/mcpserver/capability_registry.go": true, "internal/mcpserver/diag_release_tools.go": true,
	"internal/mcpserver/ha_tools.go": true, "internal/mcpserver/health_tools.go": true, "internal/mcpserver/host_triage.go": true,
	"internal/mcpserver/media_stream.go": true, "internal/mcpserver/server_deps.go": true, "internal/mcpserver/server.go": true,
	"internal/mcpserver/tools_network.go": true, "internal/mcpserver/webos_test_tool.go": true,
	"internal/ownerweb/connections_api.go": true, "internal/ownerweb/connections.go": true, "internal/ownerweb/connections_providers.go": true,
	"internal/rootbroker/admin_session.go": true, "internal/rootbroker/diag_release.go": true, "internal/rootbroker/integration_admin.go": true,
	"internal/rootbroker/rootbroker.go": true, "internal/rootbroker/server.go": true,
	"internal/toolcontract/status.go": true, "internal/toolcontract/tool_types.go": true,
	"internal/tools/health/collector.go": true, "internal/tools/knowledgetools/diag.go": true, "internal/tools/networktools/network.go": true,
}

func TestCoreDoesNotGrowAddonImports(t *testing.T) {
	root := repoRoot(t)
	private := func(rel string) bool {
		for _, p := range privatePackages {
			if strings.HasPrefix(rel, "internal/"+p+"/") {
				return true
			}
		}
		return false
	}
	seen := map[string]bool{}
	var grown []string
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			if private(rel) {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, p := range privatePackages {
				if strings.Contains(string(raw), `"mcpshka/internal/`+p+`"`) {
					seen[rel] = true
					if !coreImporters[rel] {
						grown = append(grown, rel+" imports "+p)
					}
					break
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	sort.Strings(grown)
	for _, g := range grown {
		t.Errorf("%s: core must not depend on a home add-on; register it as a plugin", g)
	}
	for rel := range coreImporters {
		if !seen[rel] {
			t.Errorf("%s no longer imports an add-on: drop it from coreImporters", rel)
		}
	}
}
