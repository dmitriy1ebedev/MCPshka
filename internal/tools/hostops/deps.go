package hostops

import (
	"context"
	"mcpshka/internal/controlplane"
	"mcpshka/internal/dockerops"
	"mcpshka/internal/idempotency"
	"mcpshka/internal/jobs"
	"mcpshka/internal/systemdsvc"
	"mcpshka/internal/systeminfo"
	"mcpshka/internal/toolcontract"
)

type Deps struct {
	Manager       *jobs.Manager
	Store         *idempotency.Store
	Docker        *dockerops.Service
	Control       *controlplane.Service
	Services      *systemdsvc.Service
	HostInventory func(context.Context, string) (systeminfo.HostStorageInventory, error)
}

type HostRelocatePlanInput = toolcontract.HostRelocatePlanInput
type HostStorageInventoryInput = toolcontract.HostStorageInventoryInput
type DockerListInput = toolcontract.DockerListInput
type DockerContainerInput = toolcontract.DockerContainerInput
type DockerLogsInput = toolcontract.DockerLogsInput
type DockerActionInput = toolcontract.DockerActionInput
type EmptyInput = toolcontract.EmptyInput
type ProtectedCoreResult = toolcontract.ProtectedCoreResult
type MutationPreflightInput = toolcontract.MutationPreflightInput
type IdempotencyStatusInput = toolcontract.IdempotencyStatusInput
type ServiceStatusInput = toolcontract.ServiceStatusInput
type ServiceActionInput = toolcontract.ServiceActionInput
type JournalReadInput = toolcontract.JournalReadInput
