package mcpserver

import (
	"maps"
	"slices"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type registryTool struct {
	tool      *mcp.Tool
	readOnly  bool
	reader    probeHandler
	normalize func(map[string]any) (map[string]any, error)
	invoke    toolInvoker
}

type registrySnapshot struct {
	catalog   map[string]*mcp.Tool
	tools     map[string]bool
	contracts map[string]mutationContract
	revision  string
}

// No registry lock is held while invoking handlers or inspecting the host.
func (r *toolRegistry) lookupTool(name string) registryTool {
	r.catalogMu.RLock()
	defer r.catalogMu.RUnlock()
	return registryTool{r.catalog[name], r.tools[name], r.readers[name], r.inputs[name], r.invokers[name]}
}

func (r *toolRegistry) contractsLocked() map[string]mutationContract {
	out := maps.Clone(mutationContracts)
	for name, c := range r.dynamicContracts {
		out[name] = c
	}
	return out
}

// registryView is snapshotRegistry without the catalog revision: startup
// validation needs only names and contracts, and hashing the whole catalog
// (~0.6 s on H96) on every server build was a fifth of New().
func (r *toolRegistry) registryView() registrySnapshot {
	r.catalogMu.RLock()
	defer r.catalogMu.RUnlock()
	return registrySnapshot{maps.Clone(r.catalog), maps.Clone(r.tools), r.contractsLocked(), ""}
}

func (r *toolRegistry) snapshotRegistry() registrySnapshot {
	r.catalogMu.RLock()
	if r.catalogDirty {
		r.catalogMu.RUnlock()
		r.catalogMu.Lock()
		if r.catalogDirty {
			r.refreshCatalogRevisionLocked()
			r.catalogDirty = false
		}
		r.catalogMu.Unlock()
		r.catalogMu.RLock()
	}
	defer r.catalogMu.RUnlock()
	return registrySnapshot{maps.Clone(r.catalog), maps.Clone(r.tools), r.contractsLocked(), r.catalogRevision}
}

func (r *toolRegistry) contractFor(name string) (mutationContract, bool) {
	r.catalogMu.RLock()
	defer r.catalogMu.RUnlock()
	if c, ok := r.dynamicContracts[name]; ok {
		return c, true
	}
	c, ok := mutationContracts[name]
	return c, ok
}

func (s registrySnapshot) names() []string {
	out := make([]string, 0, len(s.catalog))
	for name := range s.catalog {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

// exposedNames are the names tools/list carries: hidden tools stay callable but
// a client missing them is not stale.
func (s registrySnapshot) exposedNames() []string {
	out := []string{}
	for _, name := range s.names() {
		if !hiddenTool(name) {
			out = append(out, name)
		}
	}
	return out
}

func (s registrySnapshot) schemas() map[string]string {
	out := map[string]string{}
	for name, t := range s.catalog {
		out[name] = toolSchemaHash(t)
	}
	return out
}

// Called only while publishing a complete registration under catalogMu.
func (r *toolRegistry) refreshCatalogRevisionLocked() {
	r.catalogDirty = false
	names := make([]string, 0, len(r.catalog))
	for name := range r.catalog {
		names = append(names, name)
	}
	slices.Sort(names)
	tools := make([]*mcp.Tool, 0, len(names))
	for _, name := range names {
		tools = append(tools, r.catalog[name])
	}
	r.catalogRevision = capabilityHash(struct {
		Tools     []*mcp.Tool
		Contracts map[string]mutationContract
		Aliases   map[string]string
	}{tools, r.contractsLocked(), capabilityAliases})
}
