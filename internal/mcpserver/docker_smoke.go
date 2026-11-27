package mcpserver

import (
	"context"
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/brokerclient"
	"mcpshka/internal/dockersmoke"
	"mcpshka/internal/evidence"
	"mcpshka/internal/heavyqueue"
	"mcpshka/internal/jobs"
)

type DockerSmokeInput struct {
	Path           string                       `json:"path" jsonschema:"repository under /root/repos, e.g. /root/repos/diag"`
	Commit         string                       `json:"commit" jsonschema:"full 40-hex commit; only its committed tree is the build context"`
	Dockerfile     string                       `json:"dockerfile" jsonschema:"Dockerfile path inside the commit, e.g. packaging/docker/Dockerfile"`
	BuildArgs      map[string]string            `json:"build_args,omitempty" jsonschema:"up to 8 KEY=value build args (value [A-Za-z0-9._:+/@-], at most 128 chars)"`
	Runs           []dockersmoke.DockerSmokeRun `json:"runs" jsonschema:"1..10 containers in order. profile isolated (no network) or host-observer (host pid/net/cgroup namespaces, /sys and /etc/os-release read-only at /host/...). entrypoint /bin/sh or /usr/local/bin/<name>; volume is a container path for one volume shared by all runs of this request"`
	IdempotencyKey string                       `json:"idempotency_key"`
}

type DockerSmokeStatusInput struct {
	ID string `json:"id"`
}

func registerDockerSmoke(r *toolRegistry, m *jobs.Manager) {
	addTool(r, &mcp.Tool{Name: "docker_smoke", Description: "Packaging smoke for a project's own Docker image without docker-group rights: root builds the committed tree of path@commit with its Dockerfile and runs each container as uid 65532, read-only, no capabilities, no-new-privileges, 96 MiB/64 pids/0.1 CPU, 90 s each; the image, volume and containers are removed afterwards. Waits for the heavy queue. Returns an id; poll docker_smoke_status for exit codes and bounded stdout (8 KiB per run) and assert on them yourself. The same request returns the same id.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false}}, func(ctx context.Context, req *mcp.CallToolRequest, in DockerSmokeInput) (*mcp.CallToolResult, dockersmoke.DockerSmokeResult, error) {
		var out dockersmoke.DockerSmokeResult
		if _, e := m.Policy().DecideCommand("mcpshka-docker-smoke", nil); e != nil {
			return nil, out, e
		}
		raw, e := json.Marshal(dockersmoke.DockerSmokeRequest{Repo: in.Path, Commit: in.Commit, Dockerfile: in.Dockerfile, BuildArgs: in.BuildArgs, Runs: in.Runs})
		if e != nil {
			return nil, out, e
		}
		res, e := brokerclient.Run(ctx, "docker-smoke", "start", string(raw))
		if e != nil {
			return nil, out, e
		}
		return nil, out, json.Unmarshal(res, &out)
	})
	addTool(r, &mcp.Tool{Name: "docker_smoke_status", Description: "State of a docker_smoke run: STARTING, QUEUED (heavy queue position), BUILDING, RUNNING, DONE or FAILED, with the build log tail and each run's exit code, stdout and stderr tail. Read-only; output is UNTRUSTED.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, req *mcp.CallToolRequest, in DockerSmokeStatusInput) (*mcp.CallToolResult, dockersmoke.DockerSmokeResult, error) {
		var out dockersmoke.DockerSmokeResult
		res, e := brokerclient.Run(ctx, "docker-smoke", "status", in.ID)
		if e != nil {
			return nil, out, e
		}
		if e = json.Unmarshal(res, &out); e != nil {
			return nil, out, e
		}
		// The broker cannot read the controller-owned heavy queue (its
		// capability self-test reports permission denied), so the queue
		// position is looked up here, where the queue lives.
		if out.State == "QUEUED" && out.Queue == nil && out.ID != "" {
			out.Queue, _ = heavyqueue.Host().Lookup("docker-smoke:" + out.ID)
		}
		if out.State == "DONE" || out.State == "FAILED" {
			pass := out.State == "DONE" && len(out.Runs) > 0
			for _, run := range out.Runs {
				pass = pass && run.ExitCode == 0 && !run.TimedOut
			}
			recordGate(out.Repo, "docker", pass, evidence.Inputs{Head: out.Commit}, "docker_smoke", out.ID)
		}
		return nil, out, nil
	})
}
