package rootbroker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// A root-owned receipt survives installer replacement without authorizing replay.
type permanentRetirement struct {
	Schema                 string `json:"schema"`
	State                  string `json:"state"`
	DeploymentID           string `json:"deployment_id"`
	ReceiptSHA256          string `json:"receipt_sha256"`
	FenceSHA256            string `json:"fence_sha256"`
	AuthorizationSessionID string `json:"authorization_session_id"`
	RetiredAt              int64  `json:"retired_at"`
}

func retirementPath(dir, id string) string { return filepath.Join(dir, id+".retired.json") }

func readPermanentRetirement(dir string, args []string, now time.Time, read func(string, int64) ([]byte, error)) ([]byte, error) {
	if len(args) != 3 || !adminDeploymentID.MatchString(args[0]) || !validHash(args[1]) || !validHash(args[2]) {
		return nil, errors.New("invalid retirement identity: HOLD")
	}
	raw, err := read(retirementPath(dir, args[0]), 4096)
	if err != nil {
		return nil, err
	}
	var record permanentRetirement
	if err := decodeUniqueHistorical(raw, &record, 7); err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil, errors.New("invalid retirement fields: HOLD")
	}
	for _, name := range []string{"schema", "state", "deployment_id", "receipt_sha256", "fence_sha256", "authorization_session_id", "retired_at"} {
		if _, ok := fields[name]; !ok {
			return nil, errors.New("retirement field alias: HOLD")
		}
	}
	if record.Schema != "mcpshka.permanent-retirement/v1" || record.State != "NONREPLAYABLE_OUTCOME_UNKNOWN" || record.DeploymentID != args[0] || record.ReceiptSHA256 != args[1] || record.FenceSHA256 != args[2] || !validHex(record.AuthorizationSessionID, 32) || record.RetiredAt <= 0 || record.RetiredAt > now.Unix() {
		return nil, errors.New("retirement record pin mismatch: HOLD")
	}
	fence, err := read(filepath.Join(dir, args[0]+".json"), 8192)
	if err != nil {
		return nil, err
	}
	if historicSHA(fence) != record.FenceSHA256 {
		return nil, errors.New("retired fence changed: HOLD")
	}
	return json.Marshal(historicalFenceProof{Schema: "mcpshka.historical-fence-proof/v2", State: "VERIFIED_NONREPLAYABLE", DeploymentID: record.DeploymentID, ReceiptSHA256: record.ReceiptSHA256, FenceSHA256: record.FenceSHA256, RetirementSHA256: historicSHA(raw)})
}

// The existing owner-authenticated, sandboxed retirement worker is the only writer.
// Legacy verification is needed once; read-only probes never create this record.
func sealLegacyRetirement(ctx context.Context, args []string) ([]byte, error) {
	if err := validateRetirementRequest(args); err != nil {
		return nil, err
	}
	if args[3] != legacyFenceID || args[4] != legacyReceiptSHA {
		return nil, errors.New("legacy retirement pin mismatch: HOLD")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sessionLock, err := openAdminSessionLock(taskRoot)
	if err != nil {
		return nil, err
	}
	defer sessionLock.Close()
	current, err := readAdminSession(taskRoot, time.Now())
	if err != nil {
		return nil, err
	}
	if err := requireAdminSession(current, args[1], args[2], current.Generation, time.Now()); err != nil {
		return nil, err
	}
	deploymentLock, err := openMutationLock("/run/mcpshka-self-deploy.lock")
	if err != nil {
		return nil, err
	}
	defer deploymentLock.Close()
	image, err := historicalRootRead("/usr/local/bin/mcpshka", 128<<20)
	if err != nil {
		return nil, err
	}
	if historicSHA(image) != args[5] {
		return nil, errors.New("current controller changed: HOLD")
	}
	const dir = "/var/lib/mcpshka-deploy-fences"
	if err := historicalRootDir(dir, 0755); err != nil {
		return nil, err
	}
	fence, err := historicalRootRead(filepath.Join(dir, args[3]+".json"), 8192)
	if err != nil {
		return nil, err
	}
	proofArgs := []string{args[3], args[4], historicSHA(fence)}
	// Re-read any existing record rather than overwriting a partial or corrupt write.
	if _, err := os.Lstat(retirementPath(dir, args[3])); err == nil {
		return readPermanentRetirement(dir, proofArgs, time.Now(), historicalRootRead)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	proof, err := runHistoricalFenceProof(ctx, proofArgs)
	if err != nil {
		return nil, err
	}
	var verified historicalFenceProof
	if json.Unmarshal(proof, &verified) != nil || verified.Schema != "mcpshka.historical-fence-proof/v1" || verified.UpgradeID != legacyUpgradeID {
		return nil, errors.New("legacy root chain unverified: HOLD")
	}
	record := permanentRetirement{Schema: "mcpshka.permanent-retirement/v1", State: "NONREPLAYABLE_OUTCOME_UNKNOWN", DeploymentID: args[3], ReceiptSHA256: args[4], FenceSHA256: historicSHA(fence), AuthorizationSessionID: current.ID, RetiredAt: time.Now().Unix()}
	if err := writeRootOnly(retirementPath(dir, args[3]), record, true); err != nil {
		return nil, err
	}
	return readPermanentRetirement(dir, proofArgs, time.Now(), historicalRootRead)
}
