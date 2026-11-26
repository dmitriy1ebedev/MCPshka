package mcpserver

import (
	"context"
	"mcpshka/internal/filesystem"
	"mcpshka/internal/statestore"
	"mcpshka/internal/tools/adminops"
)

type AdminSessionInput = adminops.AdminSessionInput
type AdminSessionBeginInput = adminops.AdminSessionBeginInput
type AdminSessionEndInput = adminops.AdminSessionEndInput
type AdminSessionTaskInput = adminops.AdminSessionTaskInput
type AdminSessionRetireInput = adminops.AdminSessionRetireInput
type AdminLegacyLeasePreviewInput = adminops.AdminLegacyLeasePreviewInput
type AdminLegacyLeaseRecoverInput = adminops.AdminLegacyLeaseRecoverInput
type AdminSessionBeginResult = adminops.AdminSessionBeginResult

var adminOwner = adminops.Owner
var adminBeginArgs = adminops.BeginArgs

func runAdminSession[T any](ctx context.Context, args ...string) (T, error) {
	return adminops.Run[T](ctx, args...)
}

func registerAdminSessionTools(r *toolRegistry, files *filesystem.Service, db *statestore.DB) {
	registerAdminArchiveTools(r)
	registerCodexSessionCreateTools(r)
	adminops.Register(r.install, files, db)
}
