package mcpserver

import (
	"mcpshka/internal/filesystem"
	"mcpshka/internal/tools/roottask"
)

type RootTaskInput = roottask.RootTaskInput
type RootTaskAuthorizeInput = roottask.RootTaskAuthorizeInput
type RootTaskPasskeyInput = roottask.RootTaskPasskeyInput
type RootTaskPasskeyResult = roottask.RootTaskPasskeyResult
type RootTaskExecuteInput = roottask.RootTaskExecuteInput
type RootTaskResult = roottask.RootTaskResult

const maxRootTaskPayload = roottask.MaxPayload

var newTaskID = roottask.NewTaskID
var getStagedAutomations = roottask.GetStagedAutomations
var taskContentChunks = roottask.TaskContentChunks

func registerRootTaskTools(r *toolRegistry, files *filesystem.Service) {
	roottask.Register(r.install, roottask.Deps{Files: files, Owner: uploadOwner})
}
