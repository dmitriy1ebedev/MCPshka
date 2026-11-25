package projectjobs

import (
	"mcpshka/internal/projectprofile"
	"testing"
	"time"
)

func TestProfileJobOptionsTimeoutIsDefaultOutputIsCeiling(t *testing.T) {
	plan := projectprofile.Plan{Profile: "mcpshka", TimeoutSeconds: 30, OutputLimitBytes: 4096}
	opts := ProfileOptions(plan, 300, 65536)
	if !opts.PrivateTemp || opts.Env["GOMAXPROCS"] != "2" {
		t.Fatal("MCPshka workflow lacks managed disk-backed temporary storage or bounded Go parallelism")
	}
	if got := int64(opts.Timeout / time.Second); got != 300 {
		t.Fatalf("explicit timeout=%d want=300 (profile timeout is a default, not a ceiling)", got)
	}
	if got := int64(ProfileOptions(plan, 0, 0).Timeout / time.Second); got != 30 {
		t.Fatalf("default timeout=%d want=30", got)
	}
	if opts.OutputLimitBytes != 4096 {
		t.Fatalf("output_limit=%d want=4096", opts.OutputLimitBytes)
	}

	opts = ProfileOptions(plan, 5, 1024)
	if got := int64(opts.Timeout / time.Second); got != 5 {
		t.Fatalf("tightened timeout=%d want=5", got)
	}
	if opts.OutputLimitBytes != 1024 {
		t.Fatalf("tightened output_limit=%d want=1024", opts.OutputLimitBytes)
	}
}
