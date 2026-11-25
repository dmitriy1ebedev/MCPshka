package mcpserver

import (
	"mcpshka/internal/statestore"
	"mcpshka/internal/tools/cloud"
)

type CloudTaskStatusInput = cloud.CloudTaskStatusInput
type CloudTaskListInput = cloud.CloudTaskListInput
type CloudTaskResult = cloud.CloudTaskResult
type CloudTaskListResult = cloud.CloudTaskListResult

func registerCloudTaskStatus(r *toolRegistry, db *statestore.DB) {
	cloud.RegisterTaskStatus(r.install, db)
}
