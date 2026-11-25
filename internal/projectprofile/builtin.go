package projectprofile

func diagProfile() Profile {
	return Profile{
		Schema:      SchemaV1,
		Name:        "diag",
		CI:          &CI{Provider: "distributed", ExternalOnly: true},
		Description: "DIAG Go/Make development workflows with bounded execution defaults.",
		Match: Match{
			Module:  "gitverse.ru/dmitriy_1ebedev/diag",
			Markers: []string{"go.mod", "Makefile"},
		},
		Workflows: map[string]Workflow{
			"test": {
				Command: "make", Args: []string{"test"},
				TimeoutSeconds: 1800, OutputLimitBytes: 32 * 1024 * 1024,
			},
			// make is admitted only inside the sealed test sandbox, so a
			// project_build of `make build` was refused as privileged and
			// blocked DIAG P11 acceptance; go build is the same target.
			"build": {
				Command: "go", Args: []string{"build", "-trimpath", "-buildvcs=false", "-ldflags=-s -w", "-o", "build/diag", "./cmd/diag"},
				Env:            map[string]string{"CGO_ENABLED": "0"},
				TimeoutSeconds: 1200, OutputLimitBytes: 16 * 1024 * 1024,
			},
			"check": {
				Command: "make", Args: []string{"vet"},
				TimeoutSeconds: 600, OutputLimitBytes: 8 * 1024 * 1024,
			},
		},
	}
}

func mcpshkaProfile() Profile {
	return Profile{Schema: SchemaV1, Name: "mcpshka", Description: "Bounded MCPshka development workflows.", Match: Match{Module: "mcpshka", Markers: []string{"go.mod"}}, Workflows: map[string]Workflow{
		"test":        {Command: "python3", Args: []string{"-I", "-B", "packaging/native/test-plan.py", "FAST", "--run", "--timeout", "300"}, TimeoutSeconds: 600, OutputLimitBytes: 4 * 1024 * 1024},
		"build":       {Command: "go", Args: []string{"build", "-p", "1", "-buildvcs=false", "./..."}, TimeoutSeconds: 300, OutputLimitBytes: 16384},
		"check":       {Command: "python3", Args: []string{"-I", "-B", "packaging/native/test-plan.py", "INTEGRATION", "--run", "--timeout", "300"}, TimeoutSeconds: 600, OutputLimitBytes: 4 * 1024 * 1024},
		"fast":        {Command: "python3", Args: []string{"-I", "-B", "packaging/native/test-plan.py", "FAST", "--run", "--timeout", "300"}, TimeoutSeconds: 600, OutputLimitBytes: 4 * 1024 * 1024},
		"integration": {Command: "python3", Args: []string{"-I", "-B", "packaging/native/test-plan.py", "INTEGRATION", "--run", "--timeout", "600"}, TimeoutSeconds: 1200, OutputLimitBytes: 8 * 1024 * 1024},
		"release":     {Command: "python3", Args: []string{"-I", "-B", "packaging/native/test-plan.py", "RELEASE", "--run", "--timeout", "1200"}, TimeoutSeconds: 3600, OutputLimitBytes: 8 * 1024 * 1024},
		"production":  {Command: "python3", Args: []string{"-I", "-B", "packaging/native/test-plan.py", "PRODUCTION", "--run"}, TimeoutSeconds: 120, OutputLimitBytes: 32768},
	}}
}

func laneProfile() Profile {
	return Profile{Schema: SchemaV1, Name: "lane", CI: &CI{Provider: "distributed", ExternalOnly: true}, Match: Match{Module: "lane", Markers: []string{"go.mod"}}, Workflows: map[string]Workflow{
		"test":  {Command: "go", Args: []string{"test", "-p", "1", "./..."}, TimeoutSeconds: 1800, OutputLimitBytes: 32 << 20},
		"build": {Command: "go", Args: []string{"build", "-p", "1", "./cmd/lane-go"}, TimeoutSeconds: 1800, OutputLimitBytes: 32 << 20},
	}}
}
