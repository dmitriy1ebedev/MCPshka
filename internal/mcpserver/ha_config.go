package mcpserver

import (
	"context"

	"mcpshka/internal/adminsession"
	"mcpshka/internal/rootbroker"
)

// requireActiveAdmin admits a Home Assistant configuration change only under
// the owner's active Face ID session; reads need none.
func requireActiveAdmin(ctx context.Context, session string) error {
	return adminsession.Require(ctx, session, adminOwner, func(ctx context.Context, s, owner string) (string, error) {
		v, err := runAdminSession[rootbroker.AdminSessionView](ctx, "status", s, owner)
		return v.State, err
	})
}
