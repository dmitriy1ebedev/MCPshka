package mcpserver

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/catalogdigest"
	clientcatalog "mcpshka/internal/tools/clientcatalog"
	"time"
)

type ClientCatalogEntry = clientcatalog.ClientCatalogEntry
type ClientCatalogView = clientcatalog.ClientCatalogView
type ClientCatalogStatus = clientcatalog.ClientCatalogStatus
type clientCatalog = clientcatalog.Catalog
type fullCatalogKey = clientcatalog.FullCatalogKey

func newClientCatalog(path string) *clientCatalog {
	return clientcatalog.New(clientcatalog.Deps{Path: path, Now: time.Now, Hidden: hiddenTool, CoreTools: coreTools, SchemaHash: toolSchemaHash})
}

func toolSchemaHash(t *mcp.Tool) string {
	return capabilityHash(struct{ Input, Output any }{t.InputSchema, t.OutputSchema})
}
func (r *toolRegistry) capabilitySchemas() map[string]string {
	return r.snapshotRegistry().schemas()
}
func registerClientCatalogTool(r *toolRegistry) {
	clientcatalog.Register(r.install, r.clients, func() clientcatalog.Snapshot {
		view := r.snapshotRegistry()
		return clientcatalog.Snapshot{Names: view.exposedNames(), Schemas: view.schemas(), Digest: catalogdigest.Hash(view.catalog)}
	})
}
