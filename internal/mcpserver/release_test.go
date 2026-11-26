package mcpserver

import (
	"encoding/json"
	"mcpshka/internal/policy"
	"mcpshka/internal/releaseprep"
	"strings"
	"testing"
)

func TestReleaseJobKeepsURLPrivateAndBounded(t *testing.T) {
	in := ReleasePrepareInput{ArtifactURL: "https://files.oaiusercontent.com/file?sig=secret", Version: "0.1.110", CurrentVersion: "0.1.109", SourceDigest: strings.Repeat("a", 64), Receipt: map[string]any{}, IdempotencyKey: "one"}
	args, opts, e := releaseJobInput(in)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(args)
	if len(args) != 2 || args[0] != "release-artifact" || policy.Default().ClassifyCommand(releaseprep.Command, args) == policy.ClassPrivileged {
		t.Fatal("native release job requires a forbidden interpreter")
	}
	if strings.Contains(string(raw), "secret") || opts.Env["MCPSHKA_RELEASE_URL"] != in.ArtifactURL || opts.MemoryLimitBytes != 256<<20 || opts.Timeout.Seconds() != 120 || opts.OutputLimitBytes != 8192 {
		t.Fatal("release privacy or limits lost")
	}
	for _, bad := range []string{"http://files.oaiusercontent.com/f", "https://localhost/f", "https://x@files.oaiusercontent.com/f", "https://files.oaiusercontent.com:444/f"} {
		in.ArtifactURL = bad
		if _, _, e = releaseJobInput(in); e == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}
func TestReleaseJobAcceptsPinnedInboxArtifact(t *testing.T) {
	in := ReleasePrepareInput{
		ArtifactPath: "/var/lib/mcpshka/artifact-inbox/" + strings.Repeat("a", 32) + ".blob",
		Version:      "0.1.125", CurrentVersion: "0.1.123",
		SourceDigest: strings.Repeat("b", 64), Receipt: map[string]any{},
		IdempotencyKey: "inbox",
	}
	args, opts, err := releaseJobInput(in)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(args)
	if strings.Contains(string(raw), in.ArtifactPath) || opts.Env["MCPSHKA_RELEASE_FILE"] != in.ArtifactPath {
		t.Fatal("inbox source leaked into job arguments")
	}
	for _, bad := range []string{
		"/tmp/" + strings.Repeat("a", 32) + ".blob",
		"/var/lib/mcpshka/artifact-inbox/../" + strings.Repeat("a", 32) + ".blob",
		"/var/lib/mcpshka/artifact-inbox/" + strings.Repeat("A", 32) + ".blob",
	} {
		in.ArtifactPath = bad
		if _, _, err = releaseJobInput(in); err == nil {
			t.Fatalf("accepted unsafe inbox path %q", bad)
		}
	}
	in.ArtifactPath = "/var/lib/mcpshka/artifact-inbox/" + strings.Repeat("a", 32) + ".blob"
	in.ArtifactURL = "https://files.oaiusercontent.com/file"
	if _, _, err = releaseJobInput(in); err == nil {
		t.Fatal("accepted ambiguous artifact sources")
	}
}

func TestReleaseStatusRejectsUnrelatedJob(t *testing.T) {
	m := uploadManager(t)
	s, e := m.StoreReceipt("unrelated", nil, m.StateDir(), "{}")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = readRelease(m, s.ID); e == nil {
		t.Fatal("unrelated job accepted")
	}
}

func TestReleaseStatusRecognizesNativeAndHistoricalControlReceipts(t *testing.T) {
	m := uploadManager(t)
	for _, job := range []struct {
		command string
		args    []string
	}{
		{releaseprep.Command, []string{"release-artifact", "{}"}},
		{"python3", []string{"-I", releaseRunner, "{}"}},
	} {
		s, e := m.StoreReceipt(job.command, job.args, m.StateDir(), `{"schema":"mcpshka.release-prepared/v1","deploy_authorized":false}`)
		if e != nil {
			t.Fatal(e)
		}
		out, e := readRelease(m, s.ID)
		// A control receipt has no process exit and must not fabricate preparation success.
		if e != nil || out.JobID != s.ID || out.State != "done" || out.ExitCode != nil || out.Prepared != nil || out.Trust != "UNTRUSTED" {
			t.Fatalf("valid preparation receipt not recognized: %+v %v", out, e)
		}
	}
}

func TestLocalReleaseIsNotAJob(t *testing.T) {
	// The root broker accepts only the controller's PID; a job could never
	// reach it, so source=local is redirected to the in-process release_local.
	in := ReleasePrepareInput{Source: "local", Version: "0.1.169", CurrentVersion: "0.1.168", SourceDigest: strings.Repeat("a", 64), BuildPermit: strings.Repeat("b", 32), LaunchID: strings.Repeat("c", 32), TestPermit: strings.Repeat("d", 32), IdempotencyKey: "local"}
	if _, _, err := releaseJobInput(in); err == nil || !strings.Contains(err.Error(), "release_local") {
		t.Fatalf("local release still started as a job: %v", err)
	}
}

func TestLocalReleaseMCPDoesNotRequireGitHubArtifactFields(t *testing.T) {
	client := fullCatalogClient(t)
	found := false
	for _, tool := range catalogTools(t, client) {
		if tool.Name != "release_prepare" {
			continue
		}
		found = true
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema struct {
			Required []string `json:"required"`
		}
		if err = json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		for _, key := range schema.Required {
			if key == "receipt" || key == "artifact_url" {
				t.Fatalf("local MCP request cannot satisfy required %s", key)
			}
		}
	}
	if !found {
		t.Fatal("release_prepare not registered")
	}
}
