package mcpserver

import (
	"context"
	"errors"

	"mcpshka/internal/rootbroker"
)

// requireActiveAdmin admits a Home Assistant configuration change only under
// the owner's active Face ID session; reads need none.
func requireActiveAdmin(ctx context.Context, session string) error {
	owner, err := adminOwner(ctx)
	if err != nil {
		return err
	}
	v, err := runAdminSession[rootbroker.AdminSessionView](ctx, "status", session, owner)
	if err != nil {
		return err
	}
	if v.State != "active" {
		return errors.New("Face ID session not active: admin_session_prepare → admin_session_begin, then retry with its admin_session_id")
	}
	return nil
}
