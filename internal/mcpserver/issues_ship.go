package mcpserver

import (
	"mcpshka/internal/dockerops"
	"mcpshka/internal/jobs"
	"mcpshka/internal/selfheal"
	"mcpshka/internal/systemdsvc"
)

func ShipIssuesOnStart() { issueService.ShipOnStart() }
func startIssueSentinel(docker *dockerops.Service, services *systemdsvc.Service, manager *jobs.Manager, heal ...*selfheal.Service) {
	issueService.StartSentinel(docker, services, manager, heal...)
}
