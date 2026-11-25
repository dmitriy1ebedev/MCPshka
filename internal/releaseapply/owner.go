package releaseapply

import (
	"errors"
	"os"
	"path/filepath"
)

// CheckOwnerEvidence expects test evidence from an already verified local receipt.
func CheckOwnerEvidence(version string, kind, arm64 string) error {
	if !ownerReleaseHasArm64(kind, arm64) {
		return errors.New("owner release requires sealed successful GitHub CI including release-arm64: HOLD")
	}
	return ownerReleaseUnstarted(version)
}

func ownerReleaseHasArm64(kind, arm64 string) bool {
	return kind == "github" && arm64 == "success"
}

func ownerReleaseUnstarted(version string) error {
	if _, err := os.Lstat(filepath.Join(ResultDir, version+".json")); !errors.Is(err, os.ErrNotExist) {
		return errors.New("root apply history already exists or is unreadable; reconcile release_apply_status: HOLD")
	}
	return nil
}
