package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"golang.org/x/sys/unix"
	"io"
	"mcpshka/internal/integrationqueue"
	"os"
	"path/filepath"
	"strings"
)

type protectedRefEffect struct {
	Version     int                `json:"version"`
	Repository  RepositoryIdentity `json:"repository"`
	ApprovalID  string             `json:"approval_id"`
	ExecutionID string             `json:"execution_id"`
	Generation  uint64             `json:"generation"`
	Ref         string             `json:"ref"`
	Expected    string             `json:"expected"`
	Candidate   string             `json:"candidate"`
	State       string             `json:"state"`
}

func protectedEffectKey(execution string) string {
	return "executor/protected-effects/" + execution + ".json"
}

// Private effect core. The supervised root caller must hold the repository
// admission lock and prove the current worker/claim generation before entry.
// Existing legacy paths never qualify for this born-store transaction.
func (c *Controller) applyProtectedRefEffect(ctx context.Context, parent string, a IntegrationApproval, generation uint64, owner uint32, afterTarget func() error) error {
	return c.applyProtectedRefEffectWithSync(ctx, parent, a, generation, owner, afterTarget, nil)
}

func (c *Controller) applyProtectedRefEffectWithSync(ctx context.Context, parent string, a IntegrationApproval, generation uint64, owner uint32, afterTarget func() error, syncRef func(string) error) error {

	if c.db == nil || generation == 0 || !digestPattern.MatchString(a.ID) || !idPattern.MatchString(a.ExecutionID) || !integrationqueue.ValidRequest(a.Request) {
		return ErrHold
	}
	if _, err := c.protectedRepositoryBirth(ctx, parent, a.Repository, owner); err != nil {
		return ErrHold
	}
	root, err := os.OpenRoot(a.Repository.CommonDir)
	if err != nil {
		return ErrHold
	}
	defer root.Close()
	sync := func(ref string) error {
		if syncRef != nil {
			return syncRef(ref)
		}
		return syncProtectedRefDirectory(root, ref)
	}
	if err := protectedImportSpace(root, 1<<20); err != nil {
		return err
	}
	if err := validateProtectedObjects(ctx, root, a.Request.Commit); err != nil {
		return err
	}
	if err := validateProtectedObjects(ctx, root, a.Request.TargetHead); err != nil {
		return err
	}
	if err := protectedFastForward(ctx, root, a.Request.TargetHead, a.Request.Commit); err != nil {
		return err
	}
	target := "refs/heads/" + a.Request.TargetBranch
	sum := sha256.Sum256([]byte(a.ExecutionID))
	receipt := "refs/mcpshka/integration/" + hex.EncodeToString(sum[:])
	intent := protectedRefEffect{Version: 1, Repository: a.Repository, ApprovalID: a.ID, ExecutionID: a.ExecutionID, Generation: generation, Ref: target, Expected: a.Request.TargetHead, Candidate: a.Request.Commit, State: "ATTEMPT"}
	raw, _ := json.Marshal(intent)
	head, err := readProtectedDirectRef(root, target)
	if err != nil {
		return ErrHold
	}
	if head != intent.Expected && head != intent.Candidate {
		return ErrHold
	}
	existingReceipt, receiptErr := readProtectedDirectRef(root, receipt)
	if receiptErr != nil && !os.IsNotExist(receiptErr) {
		return ErrHold
	}
	if receiptErr == nil && (existingReceipt != intent.Candidate || head != intent.Candidate) {
		return ErrHold
	}
	// A marker or candidate HEAD without a pre-effect journal is not authority.
	if receiptErr == nil || head != intent.Expected {
		if _, err := c.db.GetContext(ctx, protectedEffectKey(a.ExecutionID)); err != nil {
			return ErrHold
		}
	}
	fresh, err := c.db.PutIfAbsent(protectedEffectKey(a.ExecutionID), raw)
	if err != nil {
		return err
	}
	if !fresh {
		prior, err := c.db.GetContext(ctx, protectedEffectKey(a.ExecutionID))
		if err != nil || len(prior) > 4096 {
			return ErrHold
		}
		var old protectedRefEffect
		d := json.NewDecoder(bytes.NewReader(prior))
		d.DisallowUnknownFields()
		if d.Decode(&old) != nil || d.Decode(new(any)) != io.EOF {
			return ErrHold
		}
		state := old.State
		old.State = "ATTEMPT"
		if old != intent || (state != "ATTEMPT" && state != "DONE") {
			return ErrHold
		}
		// Never retry an unknown target update. A candidate HEAD proves the effect;
		// only its execution receipt may be completed under the same fenced claim.
		if head != intent.Candidate {
			return ErrHold
		}
		if state == "DONE" {
			got, err := readProtectedDirectRef(root, receipt)
			if err != nil || got != intent.Candidate {
				return ErrHold
			}
			return nil
		}
		if err := sync(target); err != nil {
			return err
		}
	} else {
		if head != intent.Expected {
			return ErrHold
		}
		if err := publishProtectedRefWithSync(ctx, root, target, intent.Candidate, false, sync); err != nil {
			return err
		}
		if afterTarget != nil {
			if err := afterTarget(); err != nil {
				return err
			}
		}
	}
	// Receipt is ordered AFTER the durable target, not claimed atomic with it.
	if got, err := readProtectedDirectRef(root, receipt); err == nil {
		if got != intent.Candidate {
			return ErrHold
		}
		if err := sync(receipt); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return ErrHold
	} else if err := publishProtectedRefWithSync(ctx, root, receipt, intent.Candidate, true, sync); err != nil {
		return err
	}
	if _, err := c.observeProtectedIntegration(ctx, parent, a, owner); err != nil {
		return err
	}
	intent.State = "DONE"
	raw, _ = json.Marshal(intent)
	return c.db.Put(protectedEffectKey(a.ExecutionID), raw)
}

func protectedFastForward(ctx context.Context, root *os.Root, expected, candidate string) error {
	if !commitPattern.MatchString(expected) || !commitPattern.MatchString(candidate) {
		return ErrHold
	}
	todo := []string{candidate}
	seen := make(map[string]bool)
	for len(todo) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		oid := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		if seen[oid] {
			continue
		}
		seen[oid] = true
		if len(seen) > protectedObjectCount {
			return ErrHold
		}
		if oid == expected {
			return nil
		}
		kind, payload, _, err := readProtectedObject(ctx, root, oid, protectedObjectBytes)
		if err != nil || kind != "commit" {
			return ErrHold
		}
		links, err := protectedObjectLinks(kind, payload)
		if err != nil {
			return err
		}
		for _, link := range links {
			if link.Kind == "commit" {
				todo = append(todo, link.OID)
			}
		}
		if len(todo) > protectedObjectCount {
			return ErrHold
		}
	}
	return ErrHold
}

func readProtectedDirectRef(root *os.Root, ref string) (string, error) {
	f, err := root.Open(ref)
	if err != nil {
		return "", err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 42))
	if err != nil || len(raw) != 41 || raw[40] != '\n' || !commitPattern.MatchString(string(raw[:40])) {
		return "", ErrHold
	}
	return string(raw[:40]), nil
}

// Atomic file publication is serialized by the trusted repository lock. The
// parent fd pins the exact directory; fsync orders target before receipt.
func publishProtectedRef(ctx context.Context, root *os.Root, ref, oid string, noReplace bool) error {
	return publishProtectedRefWithSync(ctx, root, ref, oid, noReplace, nil)
}

func publishProtectedRefWithSync(ctx context.Context, root *os.Root, ref, oid string, noReplace bool, syncRef func(string) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !commitPattern.MatchString(oid) || !strings.HasPrefix(ref, "refs/") || filepath.Clean(ref) != ref || strings.Contains(ref, "..") {
		return ErrHold
	}
	dir := filepath.Dir(ref)
	for at := "refs"; at != dir; {
		next := strings.Split(strings.TrimPrefix(dir, at+"/"), "/")[0]
		at = at + "/" + next
		if err := root.Mkdir(at, 0700); err != nil && !os.IsExist(err) {
			return ErrHold
		}
		parent, err := root.Open(filepath.Dir(at))
		if err != nil {
			return ErrHold
		}
		e := parent.Sync()
		ce := parent.Close()
		if e != nil || ce != nil {
			return ErrHold
		}
	}
	d, err := root.Open(dir)
	if err != nil {
		return ErrHold
	}
	defer d.Close()
	name := filepath.Base(ref)
	tmp := ".effect-" + name
	if err := root.Remove(filepath.Join(dir, tmp)); err != nil && !os.IsNotExist(err) {
		return ErrHold
	}
	f, err := root.OpenFile(filepath.Join(dir, tmp), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return ErrHold
	}
	_, we := io.WriteString(f, oid+"\n")
	se := f.Sync()
	ce := f.Close()
	if we != nil || se != nil || ce != nil {
		return ErrHold
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	flags := uint(0)
	if noReplace {
		flags = unix.RENAME_NOREPLACE
	}
	if err := unix.Renameat2(int(d.Fd()), tmp, int(d.Fd()), name, flags); err != nil {
		return ErrHold
	}
	if syncRef != nil {
		return syncRef(ref)
	}
	return d.Sync()
}

func syncProtectedRefDirectory(root *os.Root, ref string) error {
	d, err := root.Open(filepath.Dir(ref))
	if err != nil {
		return ErrHold
	}
	defer d.Close()
	return d.Sync()
}
