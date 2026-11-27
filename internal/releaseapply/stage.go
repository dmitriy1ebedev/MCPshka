package releaseapply

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const releaseApplyBin = "/root/repos/mcpshka/bin"
const releaseApplyUpgrade = "/root"

func StagedHelper(version string) string {
	return filepath.Join(releaseApplyUpgrade, "mcpshka-upgrade-"+version, "coordinated-upgrade.py")
}

func HexSHA(raw []byte) string {
	s := sha256.Sum256(raw)
	return hex.EncodeToString(s[:])
}

// Stage writes candidates, helper and plan exactly as
// /root/mcpshka-run/bootstrap-stage.py does, all root-owned.
func Stage(version, current, tree string, images, files map[string][]byte) (map[string]any, error) {
	paths := map[string]string{}
	for kind, prefix := range map[string]string{"controller": "mcpshka-", "broker": "mcpshka-broker-"} {
		digest := HexSHA(images[kind])
		dst := filepath.Join(releaseApplyBin, prefix+version+"."+digest[:12]+".candidate")
		if existing, err := os.ReadFile(dst); err != nil || HexSHA(existing) != digest {
			if err := WriteRootFile(dst, images[kind], 0o700); err != nil {
				return nil, err
			}
		}
		paths[kind] = dst
	}
	helper := StagedHelper(version)
	if err := os.MkdirAll(filepath.Dir(helper), 0o700); err != nil {
		return nil, err
	}
	if err := WriteRootFile(helper, files["coordinated-upgrade.py"], 0o600); err != nil {
		return nil, err
	}
	installed := func(p string) string {
		raw, err := os.ReadFile(p)
		if err != nil {
			return ""
		}
		return HexSHA(raw)
	}
	planPath := filepath.Join(releaseApplyBin, "mcpshka-coordinated-release-"+version+".json")
	op := make([]byte, 16)
	if _, err := rand.Read(op); err != nil {
		return nil, err
	}
	operation := hex.EncodeToString(op)
	if raw, err := os.ReadFile(planPath); err == nil {
		var old map[string]any
		if json.Unmarshal(raw, &old) == nil {
			if id, ok := old["operation_id"].(string); ok {
				if decoded, err := hex.DecodeString(id); err == nil && len(decoded) == 16 && hex.EncodeToString(decoded) == id {
					operation = id
				}
			}
		}
	}
	plan := map[string]any{
		"schema": "mcpshka.coordinated-upgrade/v1", "operation_id": operation,
		"version": version, "current_version": current, "ci_run": 0, "source_tree": tree,
		"controller_sha256": HexSHA(images["controller"]), "broker_sha256": HexSHA(images["broker"]),
		"current_controller_sha256": installed("/usr/local/bin/mcpshka"),
		"current_broker_sha256":     installed("/usr/local/libexec/mcpshka-broker"),
		"controller_candidate":      paths["controller"], "broker_candidate": paths["broker"],
		"helper_sha256":          HexSHA(files["coordinated-upgrade.py"]),
		"gate_sha256":            HexSHA(files["release-gate.py"]),
		"guarded_helper_sha256":  HexSHA(files["self-deploy.sh"]),
		"current_gate_sha256":    installed("/usr/local/libexec/mcpshka-self-deploy"),
		"current_guarded_sha256": installed("/usr/local/libexec/mcpshka-self-deploy-guarded"),
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return nil, err
	}
	if err := WriteRootFile(planPath, raw, 0o600); err != nil {
		return nil, err
	}
	// Only the staged and the running release stay; older images go.
	PruneCandidates(releaseApplyBin, version, current)
	return plan, nil
}

func WriteRootFile(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".release-apply-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil || tmp.Chmod(mode) != nil || tmp.Sync() != nil || tmp.Close() != nil {
		tmp.Close()
		return fmt.Errorf("write %s", filepath.Base(path))
	}
	return os.Rename(tmp.Name(), path)
}
