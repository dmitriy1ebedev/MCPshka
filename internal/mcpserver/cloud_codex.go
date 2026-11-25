package mcpserver

import (
	"mcpshka/internal/tools/cloud"
)

type CloudCodexListInput = cloud.CloudCodexListInput
type CloudCodexTask = cloud.CloudCodexTask
type CloudCodexListResult = cloud.CloudCodexListResult

var cloudCodexPreview = cloud.PreviewCodex

func registerCloudCodex(r *toolRegistry) { cloud.RegisterCodex(r.install) }
