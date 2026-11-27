package mcpserver

import (
	"mcpshka/internal/tools/cloud"
)

type CloudPlan = cloud.CloudPlan
type CloudObservation = cloud.CloudObservation
type CloudFanInInput = cloud.CloudFanInInput
type CloudRun = cloud.CloudRun
type CloudFanInResult = cloud.CloudFanInResult

const cloudPlanSchema = cloud.PlanSchema
const cloudObsSchema = cloud.ObservationSchema

var cloudFanIn = cloud.FanIn

func registerCloudFanIn(r *toolRegistry) { cloud.RegisterFanIn(r.install) }
