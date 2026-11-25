package readiness

import (
	"strings"
	"testing"
)

func TestPipelineReadinessRequiresBothBackends(t *testing.T) {
	c, _ := contractFor("project_pipeline")
	if c.privilege != "executor_sandbox" || strings.Join(c.required, ",") != "executor,broker" {
		t.Fatalf("missing readiness: %+v", c)
	}
}
