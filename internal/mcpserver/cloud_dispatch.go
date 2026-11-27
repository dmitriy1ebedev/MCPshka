package mcpserver

import (
	"context"
	"errors"
	"mcpshka/internal/cloudtasks"
	"mcpshka/internal/rootbroker"
	"mcpshka/internal/statestore"
	"mcpshka/internal/tools/cloud"
)

type CloudDispatchInput = cloud.CloudDispatchInput
type CloudPollInput = cloud.CloudPollInput

func callCloudBroker(ctx context.Context, action, session, owner, raw string) (cloudtasks.DispatchReceipt, error) {
	return runAdminSession[cloudtasks.DispatchReceipt](ctx, action, session, owner, raw)
}

func requireCloudSession(ctx context.Context, session, owner string) error {
	if session == "" {
		return errors.New("active Face ID administrative session required")
	}
	v, err := runAdminSession[rootbroker.AdminSessionView](ctx, "status", session, owner)
	if err != nil {
		return err
	}
	if v.ID != session || v.Owner != owner || v.State != "active" || v.AuthMethod != "webauthn" {
		return errors.New("active owner-bound Face ID administrative session required")
	}
	return nil
}

var dispatchCloud = cloud.Dispatch

func registerCloudDispatch(r *toolRegistry, db *statestore.DB) {
	cloud.RegisterDispatch(r.install, cloud.Deps{DB: db, Owner: adminOwner, RequireSession: requireCloudSession, Call: callCloudBroker})
}
