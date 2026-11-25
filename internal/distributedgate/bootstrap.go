package distributedgate

import (
	"crypto/sha256"
	"encoding/hex"
)

// ModuleTestBootstrap derives one permit per repository, commit and attempt,
// so a lost reply is reconciled by preparing again: the sealed manifest is
// replayed. A permit runs once; a later attempt is an explicit new run.
func LocalReleaseBootstrap(repo, commit, attempt string) string {
	return ModuleTestBootstrap("local-release:"+repo, commit, attempt)
}

func ModuleTestBootstrap(repo, commit, attempt string) string {
	seed := "integration-module:" + repo + ":" + commit
	if attempt != "" && attempt != "1" {
		seed += ":attempt-" + attempt
	}
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:16])
}

// CIEvidenceBootstrap is ModuleTestBootstrap for the CI evidence kind: a new
// attempt re-reads GitHub, e.g. after a rerun of a failed job.
// GitVerseEvidenceBootstrap names GitVerse evidence apart from GitHub's, so
// both may be sealed for one commit without sharing a permit.
func GitVerseEvidenceBootstrap(repo, commit, attempt string) string {
	return CIEvidenceBootstrap("gitverse:"+repo, commit, attempt)
}

func CIEvidenceBootstrap(repo, commit, attempt string) string {
	seed := "integration-ci:" + repo + ":" + commit
	if attempt != "" && attempt != "1" {
		seed += ":attempt-" + attempt
	}
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:16])
}
