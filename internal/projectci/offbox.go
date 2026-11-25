package projectci

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"mcpshka/internal/runnerhub"
)

// OffBoxTest is phase_gate's test evidence for head away from H96; it reads test evidence for head away from H96 (0.9.8, DIAG P34:
// «нет sealed-теста, H96 перегружен»): the project's GitVerse CI, then a
// runner. With none it names the one cheapest next call — CI or a live
// runner before project_test on H96.
func OffBoxTest(root, head, version string) (string, string) {
	if detail := installedReleaseCIEvidence(root, head, version); detail != "" {
		return "PASS", detail
	}
	if detail := stagedReleaseCIEvidence(root, head); detail != "" {
		return "PASS", detail
	}
	var chk struct{ Status, Detail string }
	ci := "forge_ci provider=gitverse path=" + root + " commit=" + head
	hasCI := false
	if _, err := RepoName(root); err == nil {
		_, statErr := os.Stat(filepath.Join(root, ".gitverse", "workflows"))
		hasCI = statErr == nil
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		cst, err := New().ReadStatus(ctx, root, head)
		cancel()
		switch {
		case err != nil || cst.State == "NO_RUN":
		case cst.State == "PASS":
			chk.Status, chk.Detail = "PASS", fmt.Sprintf("GitVerse CI run %d passed on %s (%s)", cst.RunID, head[:12], cst.Repository)
			return chk.Status, chk.Detail
		case cst.State == "RUNNING":
			chk.Status, chk.Detail = "RUNNING", fmt.Sprintf("GitVerse CI run %d running on %s; next: %s mode=wait, then job_wait until done", cst.RunID, head[:12], ci)
			return chk.Status, chk.Detail
		case cst.Infra:
			chk.Status, chk.Detail = "FAIL", fmt.Sprintf("GitVerse CI run %d failed on the CI site itself, not the code; %s mode=rerun", cst.RunID, ci)
			return chk.Status, chk.Detail
		default:
			chk.Status, chk.Detail = "FAIL", fmt.Sprintf("GitVerse CI run %d failed on %s: read %s mode=status (failures), fix, commit, publish", cst.RunID, head[:12], ci)
			return chk.Status, chk.Detail
		}
	}
	if detail, ok := runnerhub.Default.PassedOn(root, head); ok {
		chk.Status, chk.Detail = "PASS", detail
		return chk.Status, chk.Detail
	}
	chk.Status = "MISSING"
	switch {
	case hasCI:
		chk.Detail = "no test for HEAD " + head[:12] + "; next: " + ci + " mode=publish (runs off H96), then mode=wait + job_wait"
	case runnerhub.Default != nil && runnerhub.Default.Fit("go") == "" && goModule(root):
		chk.Detail = "no test for HEAD " + head[:12] + "; next: runner_task path=" + root + " commit=" + head + " profile=go-test (live runner, off H96)"
	default:
		chk.Detail = "no test for HEAD " + head[:12] + "; next: project_test path=" + root + " (H96; add .gitverse/workflows to test off the box)"
	}
	return chk.Status, chk.Detail
}

func goModule(root string) bool {
	_, err := os.Stat(filepath.Join(root, "go.mod"))
	return err == nil
}
