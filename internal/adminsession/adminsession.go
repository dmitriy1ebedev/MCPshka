// Package adminsession is the one gate for "this call needs the owner's
// active Face ID window": mcpserver passes how to find the owner and read
// the window's state, the rules live here.
package adminsession

import (
	"context"
	"errors"
	"strings"
)

// Require admits a call only under the named, active Face ID window.
// S5.TIDY.4: an empty id used to read "the current grant", so a call that
// named no window still passed while any window was open.
func Require(ctx context.Context, session string, owner func(context.Context) (string, error), state func(ctx context.Context, session, owner string) (string, error)) error {
	if strings.TrimSpace(session) == "" {
		return errors.New("admin_session_id required: name the Face ID window (admin_session_status) explicitly")
	}
	who, err := owner(ctx)
	if err != nil {
		return err
	}
	st, err := state(ctx, session, who)
	if err != nil {
		return err
	}
	if st != "active" {
		return errors.New("Face ID session not active: admin_session_prepare → admin_session_begin, then retry with its admin_session_id")
	}
	return nil
}
