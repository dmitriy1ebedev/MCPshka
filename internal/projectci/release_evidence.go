package projectci

import (
	"context"
	"encoding/json"
	"time"

	"mcpshka/internal/brokerclient"
	"mcpshka/internal/releaseapplystatus"
)

func installedReleaseCIEvidence(root, head, version string) string {
	if root != "/root/repos/mcpshka" || !releaseapplystatus.Version.MatchString(version) {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	raw, err := brokerclient.Run(ctx, "release-apply-status", version)
	if err != nil {
		return ""
	}
	return releaseCIEvidence(raw, head, version)
}

func releaseCIEvidence(raw []byte, head, version string) string {
	var r struct {
		State   string `json:"state"`
		Version string `json:"version"`
		Commit  string `json:"commit"`
		Kind    string `json:"test_evidence"`
	}
	if len(head) != 40 || json.Unmarshal(raw, &r) != nil ||
		r.State != "SUCCEEDED" || r.Commit != head || r.Version != version {
		return ""
	}
	switch r.Kind {
	case "github", "gitverse", "distributed":
		return r.Kind + " CI sealed in the root release receipt for " + head[:12]
	case "module":
		// The release queue's sealed H96 test (evidence=module) is the gate
		// root installed on; without it VERIFY held «no test for HEAD» (0.18.0).
		return "sealed H96 module test in the root release receipt for " + head[:12]
	default:
		return ""
	}
}
