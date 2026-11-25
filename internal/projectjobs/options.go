package projectjobs

import (
	"mcpshka/internal/jobs"
	"mcpshka/internal/projectprofile"
	"time"
)

const (
	TestMemoryLimit  = int64(1536 * 1024 * 1024)
	TestCPUTimeLimit = time.Hour
)

func Options(timeoutSeconds, outputLimitBytes int64) jobs.StartOptions {
	opts := jobs.StartOptions{OutputLimitBytes: outputLimitBytes}
	if timeoutSeconds <= 0 {
		return opts
	}
	maxSeconds := int64(jobs.MaxJobTimeout / time.Second)
	if timeoutSeconds > maxSeconds {
		opts.Timeout = jobs.MaxJobTimeout
	} else {
		opts.Timeout = time.Duration(timeoutSeconds) * time.Second
	}
	return opts
}

func ProfileOptions(plan projectprofile.Plan, requestedTimeout, requestedOutput int64) jobs.StartOptions {
	// The profile timeout is the default, not a ceiling: an explicit request
	// may go up to the flexible-budget ceiling (internal/budget).
	timeout := plan.TimeoutSeconds
	if requestedTimeout > 0 {
		timeout = requestedTimeout
	}
	output := plan.OutputLimitBytes
	if requestedOutput > 0 && requestedOutput < output {
		output = requestedOutput
	}
	opts := Options(timeout, output)
	if plan.Profile == "mcpshka" {
		opts.PrivateTemp = true
		opts.Env = map[string]string{"GOMAXPROCS": "2"}
	}
	return opts
}
