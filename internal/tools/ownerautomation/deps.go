package ownerautomation

import (
	"context"
	"mcpshka/internal/artifacts"
	"mcpshka/internal/idempotency"
)

type Deps struct {
	Store *idempotency.Store
	Inbox *artifacts.Inbox
	Owner func(context.Context) string
}
