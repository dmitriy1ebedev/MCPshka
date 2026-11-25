package mcpserver

import (
	"context"
	"errors"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mcpshka/internal/jobs"
	"mcpshka/internal/ownerweb"
)

type StorageConnectInput struct {
	Provider       string `json:"provider" jsonschema:"drive, yandex or dropbox"`
	IdempotencyKey string `json:"idempotency_key"`
}
type StorageConnectOutput struct {
	Provider  string    `json:"provider"`
	URL       string    `json:"url,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	Next      string    `json:"next"`
}
type StorageOAuthAppInput struct {
	Provider       string `json:"provider" jsonschema:"drive, yandex or dropbox"`
	ClientID       string `json:"client_id"`
	ClientSecret   string `json:"client_secret" jsonschema:"Stored in the controller's private secrets file; never returned."`
	IdempotencyKey string `json:"idempotency_key"`
}

func registerStorageConnect(r *toolRegistry, m *jobs.Manager) {
	addTool(r, &mcp.Tool{Name: "storage_connect", Description: "Connect a cloud (drive, yandex, dropbox) from any browser: «подключи Google Drive / Яндекс.Диск / Dropbox». Always returns a one-time link (10 min) on the MCPshka HTTPS origin: the page signs the owner in, and when the cloud has no OAuth app yet it walks through creating one and takes its keys in the browser (Yandex and Dropbox also have a quick way without an app). Tokens and keys go straight to H96, never through the chat. Verify with storage_auth_status provider=….", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false}}, func(ctx context.Context, req *mcp.CallToolRequest, in StorageConnectInput) (*mcp.CallToolResult, StorageConnectOutput, error) {
		if _, e := m.Policy().DecideCommand("mcpshka-storage-connect", nil); e != nil {
			return nil, StorageConnectOutput{}, e
		}
		out := StorageConnectOutput{Provider: in.Provider}
		if !ownerweb.StorageProviderKnown(in.Provider) {
			return nil, out, errors.New("provider must be drive, yandex or dropbox")
		}
		link, exp, err := ownerweb.StorageBegin(in.Provider, uploadOwner(ctx))
		if err != nil {
			return nil, out, err
		}
		out.URL, out.ExpiresAt = link, exp
		out.Next = "Дайте владельцу эту ссылку (одной строкой, в конце ответа): страница сама проведёт по шагам — вход в облако, а если приложения облака ещё нет, покажет что создать и примет его ключи. Ключи и токены в чат не просить. Затем storage_auth_status provider=" + in.Provider + " — живая проверка."
		return nil, out, nil
	})
	addTool(r, &mcp.Tool{Name: "storage_oauth_app_set", Description: "Register the owner's OAuth app (client id and secret) for drive, yandex or dropbox, used by storage_connect. Stored in the controller's private secrets file, never returned.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false}}, func(ctx context.Context, req *mcp.CallToolRequest, in StorageOAuthAppInput) (*mcp.CallToolResult, StorageConnectOutput, error) {
		if _, e := m.Policy().DecideCommand("mcpshka-storage-connect", nil); e != nil {
			return nil, StorageConnectOutput{}, e
		}
		if err := ownerweb.SaveStorageApp(in.Provider, ownerweb.StorageOAuthApp{ClientID: in.ClientID, ClientSecret: in.ClientSecret}); err != nil {
			return nil, StorageConnectOutput{}, err
		}
		return nil, StorageConnectOutput{Provider: in.Provider, Next: "Готово. Теперь storage_connect provider=" + in.Provider + "."}, nil
	})
}
