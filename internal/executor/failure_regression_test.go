package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"mcpshka/internal/failure"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSnapshotDeadlineHasReasonAndBudget(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	_, err := gitSnapshotReadEnv(ctx, t.TempDir(), nil, "cat-file", "-t", strings.Repeat("a", 40))
	e := failure.Details(err)
	if !errors.Is(err, ErrHold) || !errors.Is(err, context.DeadlineExceeded) || e == nil || e.Class != failure.Deadline || e.BudgetMillis == nil || e.Deadline == "" {
		t.Fatalf("BUG-81: %v", err)
	}
}
func TestMissingGitVerseWorkspaceSuggestsNextAttempt(t *testing.T) {
	for _, attempt := range []int{1, 4, 8} {
		t.Run(strconv.Itoa(attempt), func(t *testing.T) {
			repo, commit := "/root/repos/example", strings.Repeat("a", 40)
			seed := "integration-ci:gitverse:" + repo + ":" + commit
			if attempt > 1 {
				seed += ":attempt-" + strconv.Itoa(attempt)
			}
			sum := sha256.Sum256([]byte(seed))
			pin, _ := snapshotIdentity(CIEvidencePurpose, hex.EncodeToString(sum[:16]))
			m := Manifest{Pin: pin, Purpose: CIEvidencePurpose, Source: &SourceSnapshot{Repo: repo, Commit: commit}}
			base := filepath.Join(t.TempDir(), "removed-workspace")
			if err := os.Mkdir(base, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(base); err != nil {
				t.Fatal(err)
			}
			_, err := localGitVerseEvidence(m, base, commit, strings.Repeat("b", 40), uint32(os.Geteuid()))
			e := failure.Details(err)
			if !errors.Is(err, ErrHold) || !errors.Is(err, os.ErrNotExist) || e == nil || e.Class != failure.EvidenceMissing || e.Code != "EVIDENCE_WORKSPACE_MISSING" {
				t.Fatalf("BUG-82: %v", err)
			}
			a := e.NextSafeAction
			if a.Tool != "integration_test_prepare" || a.Arguments["path"] != repo || a.Arguments["commit"] != commit || a.Arguments["evidence"] != "gitverse" || a.Arguments["attempt"] != attempt+1 {
				t.Fatalf("wrong recovery: %+v", a)
			}
			if _, err := os.Stat(base); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("reader recreated workspace")
			}
		})
	}
}
func TestHoldPreservesTypedCause(t *testing.T) {
	cause := failure.NewHold(failure.EvidenceMissing, "MISSING", "inner", "evidence missing", nil)
	err := Hold(failure.FencedUnknown, "OUTER", "outer", "outer admission", cause)
	if err != cause || !errors.Is(err, ErrHold) {
		t.Fatalf("cause erased: %v", err)
	}
}

func TestAdmissionReasonClassAction(t *testing.T) {
	for _, tc := range []struct {
		name       string
		run        func() error
		code       string
		class      failure.Class
		tool, goal string
	}{
		{"invalid pin", func() error { return New(nil).Create(Pin{}) }, "PERMIT_INPUT_INVALID", failure.PolicyDenied, "tool_resolve", "allowed read-only alternative"},
		{"invalid launch", func() error { return New(nil).Reserve(context.Background(), Pin{}, "") }, "LAUNCH_INPUT_INVALID", failure.PolicyDenied, "tool_resolve", "allowed read-only alternative"},
		{"no fence backend", func() error { return New(nil).Fence(context.Background(), Pin{}, nil) }, "FENCE_BACKEND_MISSING", failure.FencedUnknown, "mutation_contracts", ""},
		{"invalid ownership record", func() error { return decodeCandidateRecord([]byte("{"), new(CandidateOwnership)) }, "CANDIDATE_RECORD_INVALID", failure.FencedUnknown, "mutation_contracts", ""},
		{"invalid maintenance input", func() error { return New(nil).maintainReadonlyPermit(context.Background(), Manifest{}, nil, nil) }, "MAINTENANCE_INPUT_INVALID", failure.PolicyDenied, "tool_resolve", "allowed read-only alternative"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			e := failure.Details(err)
			if e == nil || e.Code != tc.code || e.Class != tc.class || e.Site == "" || e.NextSafeAction.Tool != tc.tool || !errors.Is(err, ErrHold) {
				t.Fatalf("reason contract: %v", err)
			}
			if tc.goal != "" && e.NextSafeAction.Arguments["goal"] != tc.goal {
				t.Fatalf("action: %+v", e.NextSafeAction)
			}
		})
	}
}

func TestWorkerRunSpecFailureReasons(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		change     func(*RunSpec)
	}{
		{"profile", "RUN_SPEC_INVALID", func(s *RunSpec) { s.Profile = "unknown" }},
		{"runtime", "RUN_RUNTIME_INVALID", func(s *RunSpec) { s.Env = map[string]string{"X": "Y"} }},
		{"nul", "RUN_ARGUMENT_NUL", func(s *RunSpec) { s.Command = []string{"python3", "-I", "-c", "x\x00"} }},
		{"bytes", "RUN_ARGUMENT_LIMIT", func(s *RunSpec) { s.Command = []string{"python3", "-I", "-c", strings.Repeat("x", 16385)} }},
		{"timeout profile", "RUN_TIMEOUT_UNSUPPORTED", func(s *RunSpec) { s.TimeoutSeconds = 1 }},
		{"recipe timeout", "RUN_TIMEOUT_INVALID", func(s *RunSpec) { s.Profile = "recipe-test-v1" }},
		{"package", "RUN_PACKAGE_INVALID", func(s *RunSpec) { s.Command = []string{"go", "test", "-p", "1", "../outside"} }},
		{"command", "RUN_COMMAND_UNSUPPORTED", func(s *RunSpec) { s.Command = []string{"sudo", "sh"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := RunSpec{Profile: "isolated-v1", Command: []string{"python3", "-I", "-c", "print(1)"}, WorkspaceDevice: 1, WorkspaceInode: 2}
			tc.change(&spec)
			err := spec.validate()
			detail := failure.Details(err)
			if !errors.Is(err, ErrHold) || detail == nil || detail.Class != failure.PolicyDenied || detail.Code != tc.code || detail.Site != "executor.RunSpec.validate" {
				t.Fatalf("reason lost: %v", err)
			}
		})
	}
	_, err := readManifest("../outside")
	detail := failure.Details(err)
	if !errors.Is(err, ErrHold) || detail == nil || detail.Class != failure.PolicyDenied || detail.Code != "MANIFEST_ACCESS_INVALID" {
		t.Fatalf("manifest boundary lost: %v", err)
	}
}

func TestWorkerContextBeforeAuthorityAccess(t *testing.T) {
	for _, tc := range []struct {
		name     string
		timeout  time.Duration
		cancel   bool
		class    failure.Class
		sentinel error
	}{
		{"deadline", -time.Second, false, failure.Deadline, context.DeadlineExceeded},
		{"canceled", time.Minute, true, failure.Transient, context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), tc.timeout)
			defer cancel()
			if tc.cancel {
				cancel()
			}
			err := Worker(ctx, "", "")
			detail := failure.Details(err)
			if !errors.Is(err, ErrHold) || !errors.Is(err, tc.sentinel) || detail == nil || detail.Class != tc.class || detail.Site != "executor.Worker" || detail.BudgetMillis == nil || detail.Deadline == "" {
				t.Fatalf("context lost: %v", err)
			}
			if tc.cancel && *detail.BudgetMillis < 59000 {
				t.Fatalf("budget lost: %v", err)
			}
		})
	}
}
