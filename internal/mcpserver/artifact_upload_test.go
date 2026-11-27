package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"mcpshka/internal/artifacts"
	"mcpshka/internal/authn"
	"mcpshka/internal/filesystem"
	"mcpshka/internal/jobs"
	"mcpshka/internal/policy"
	"mcpshka/internal/statestore"
)

// requireArtifactReserve skips upload tests on a host whose disk is below the
// reserve production enforces (10% or 3 GiB): the inbox refuses uploads
// there by design, which says nothing about the code (a sealed module test
// on the full H96 hit it while its scratch held the build cache).
func requireArtifactReserve(t *testing.T, dir string) {
	t.Helper()
	var fs syscall.Statfs_t
	if syscall.Statfs(dir, &fs) != nil {
		return
	}
	total, free := uint64(fs.Blocks)*uint64(fs.Bsize), uint64(fs.Bavail)*uint64(fs.Bsize)
	reserve := max(uint64(3<<30), total/10)
	if free < reserve+(1<<20) {
		t.Skipf("host disk below the artifact inbox reserve: %d of %d bytes free", free, reserve)
	}
}

func uploadManager(t *testing.T) *jobs.Manager {
	t.Helper()
	state := t.TempDir()
	requireArtifactReserve(t, state)
	if _, e := statestore.Open(filepath.Join(state, "state.db")); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(state, "state.mode"), []byte("sqlite-v1\n"), 0600); e != nil {
		t.Fatal(e)
	}
	p := policy.Default()
	p.AllowedWorkdirs = []string{state}
	m, e := jobs.New(filepath.Join(state, "jobs"), p)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { m.Close(time.Second) })
	return m
}
func uploadClient(t *testing.T, m *jobs.Manager, owner, role string) *mcp.ClientSession {
	t.Helper()
	ctx := authn.WithIdentity(context.Background(), authn.Identity{ID: owner, Role: role, Scopes: []string{"*"}})
	server := New(m, filesystem.New(m.Policy()))
	st, ct := mcp.NewInMemoryTransports()
	ss, e := server.Connect(ctx, st, nil)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { ss.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "file-test", Version: "1"}, nil)
	cs, e := client.Connect(ctx, ct, nil)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}
func callUpload(t *testing.T, c *mcp.ClientSession, name string, args any) artifacts.Upload {
	t.Helper()
	res, e := c.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if e != nil || res.IsError {
		t.Fatalf("%s: %+v %v", name, res, e)
	}
	b, e := json.Marshal(res.StructuredContent)
	if e != nil {
		t.Fatal(e)
	}
	var u artifacts.Upload
	if e = json.Unmarshal(b, &u); e != nil {
		t.Fatal(e)
	}
	return u
}
func TestArtifactUploadMCPContractsAndPrincipalIsolation(t *testing.T) {
	m := uploadManager(t)
	a := uploadClient(t, m, "a", "operator")
	b := uploadClient(t, m, "b", "operator")
	data := []byte("prepared companion roadmap")
	hash := sha256.Sum256(data)
	begin := ArtifactUploadInput{Name: "extension.md", Size: int64(len(data)), SHA256: hex.EncodeToString(hash[:]), IdempotencyKey: "same-key"}
	ua := callUpload(t, a, "artifact_upload_begin", begin)
	ub := callUpload(t, b, "artifact_upload_begin", begin)
	if ua.ID == ub.ID {
		t.Fatal("principal retry namespace shared")
	}
	if got := callUpload(t, a, "artifact_upload_begin", begin); got.ID != ua.ID {
		t.Fatal("begin replay duplicated")
	}
	res, e := b.CallTool(context.Background(), &mcp.CallToolParams{Name: "artifact_upload_status", Arguments: ArtifactUploadStatusInput{ID: ua.ID}})
	if e == nil && !res.IsError {
		t.Fatal("cross-principal status")
	}
	chunk := ArtifactUploadInput{ID: ua.ID, DataBase64: base64.StdEncoding.EncodeToString(data), IdempotencyKey: "chunk"}
	callUpload(t, a, "artifact_upload_chunk", chunk)
	callUpload(t, a, "artifact_upload_chunk", chunk)
	ready := callUpload(t, a, "artifact_upload_finish", ArtifactUploadInput{ID: ua.ID, IdempotencyKey: "finish"})
	if ready.State != "ready" || ready.Offset != int64(len(data)) {
		t.Fatal("not sealed", ready)
	}
	got, e := os.ReadFile(ready.Path)
	if e != nil || string(got) != string(data) {
		t.Fatal("file content", e)
	}
	reader := uploadClient(t, m, "reader", "reader")
	res, e = reader.CallTool(context.Background(), &mcp.CallToolParams{Name: "artifact_upload_begin", Arguments: begin})
	if e == nil && !res.IsError {
		t.Fatal("reader wrote inbox")
	}
	callUpload(t, b, "artifact_upload_cancel", ArtifactUploadInput{ID: ub.ID, IdempotencyKey: "cancel"})
}
func TestArtifactUploadCompatRedactsPayloadAndRejectsInvalidInput(t *testing.T) {
	m := uploadManager(t)
	i := artifactInbox(m)
	ctx := context.Background()
	content := []byte("PRIVATE-FILE-CONTENT")
	h := sha256.Sum256(content)
	input := ArtifactUploadInput{Name: "file.md", Size: int64(len(content)), SHA256: hex.EncodeToString(h[:])}
	raw, _ := json.Marshal(input)
	in := ExecStartInput{Command: "mcpshka", Args: []string{"artifact-upload", "begin", string(raw)}, Workdir: filepath.Dir(m.StateDir()), IdempotencyKey: "compat"}
	j, e := artifactUploadCompat(ctx, m, i, in)
	if e != nil {
		t.Fatal(e)
	}
	o, e := m.Read(j.ID, "stdout", 0, 4096)
	if e != nil {
		t.Fatal(e)
	}
	var u artifacts.Upload
	if e = json.Unmarshal([]byte(o.Text), &u); e != nil {
		t.Fatal(e)
	}
	input = ArtifactUploadInput{ID: u.ID, DataBase64: base64.StdEncoding.EncodeToString(content)}
	raw, _ = json.Marshal(input)
	in.Args[1] = "chunk"
	in.Args[2] = string(raw)
	in.IdempotencyKey = "compat-chunk"
	j, e = artifactUploadCompat(ctx, m, i, in)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(strings.Join(j.Args, " "), input.DataBase64) || strings.Contains(strings.Join(j.Args, " "), string(content)) {
		t.Fatal("file bytes in durable argv")
	}
	input.DataBase64 = "!!!!"
	raw, _ = json.Marshal(input)
	in.Args[2] = string(raw)
	if _, e = artifactUploadCompat(ctx, m, i, in); e == nil {
		t.Fatal("invalid base64 accepted")
	}
	in.Args = []string{"artifact-upload", "exec", "{}"}
	if _, e = artifactUploadCompat(ctx, m, i, in); e == nil {
		t.Fatal("arbitrary action")
	}
}

func TestArtifactUploadCachedFinishAndMutationProbe(t *testing.T) {
	m := uploadManager(t)
	a := uploadClient(t, m, "a", "operator")
	b := uploadClient(t, m, "b", "operator")
	data := []byte("abc")
	h := sha256.Sum256(data)
	u := callUpload(t, a, "artifact_upload_begin", ArtifactUploadInput{Name: "file", Size: 3, SHA256: hex.EncodeToString(h[:]), IdempotencyKey: "begin"})
	res, e := a.CallTool(context.Background(), &mcp.CallToolParams{Name: "mutation_probe", Arguments: MutationProbeInput{Tool: "artifact_upload_begin", Key: "begin"}})
	if e != nil || res.IsError {
		t.Fatalf("own plain-key probe failed: %+v %v", res, e)
	}
	res, e = b.CallTool(context.Background(), &mcp.CallToolParams{Name: "mutation_probe", Arguments: MutationProbeInput{Tool: "artifact_upload_begin", Key: "a\x00begin"}})
	if e == nil && !res.IsError {
		t.Fatal("forged principal namespace accepted")
	}
	callUpload(t, a, "artifact_upload_chunk", ArtifactUploadInput{ID: u.ID, DataBase64: base64.StdEncoding.EncodeToString(data), IdempotencyKey: "chunk"})
	finish := ArtifactUploadInput{ID: u.ID, IdempotencyKey: "finish"}
	u = callUpload(t, a, "artifact_upload_finish", finish)
	if e = os.WriteFile(u.Path, []byte("xyz"), 0600); e != nil {
		t.Fatal(e)
	}
	res, e = a.CallTool(context.Background(), &mcp.CallToolParams{Name: "artifact_upload_finish", Arguments: finish})
	if e == nil && !res.IsError {
		t.Fatal("tampered sealed artifact accepted by cached receipt")
	}
}
func TestArtifactCompatReceiptOwnerEnforced(t *testing.T) {
	m := uploadManager(t)
	a := uploadClient(t, m, "a", "operator")
	b := uploadClient(t, m, "b", "operator")
	h := sha256.Sum256([]byte("abc"))
	p := ArtifactUploadInput{Name: "private-name.md", Size: 3, SHA256: hex.EncodeToString(h[:])}
	raw, _ := json.Marshal(p)
	res, e := a.CallTool(context.Background(), &mcp.CallToolParams{Name: "exec_start", Arguments: ExecStartInput{Command: "mcpshka", Args: []string{"artifact-upload", "begin", string(raw)}, Workdir: filepath.Dir(m.StateDir()), IdempotencyKey: "compat-begin"}})
	if e != nil || res.IsError {
		t.Fatalf("compat begin: %+v %v", res, e)
	}
	raw, _ = json.Marshal(res.StructuredContent)
	var j jobs.Snapshot
	if e = json.Unmarshal(raw, &j); e != nil {
		t.Fatal(e)
	}
	for _, tool := range []string{"job_output", "job_status", "job_wait"} {
		res, e = b.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: map[string]any{"job_id": j.ID}})
		if e == nil && !res.IsError {
			t.Fatalf("foreign principal read %s receipt", tool)
		}
	}
}

func TestArtifactCancelRecoveryAfterBlobRemoval(t *testing.T) {
	m := uploadManager(t)
	c := uploadClient(t, m, "owner", "operator")
	h := sha256.Sum256([]byte("abc"))
	u := callUpload(t, c, "artifact_upload_begin", ArtifactUploadInput{Name: "file", Size: 3, SHA256: hex.EncodeToString(h[:]), IdempotencyKey: "begin"})
	key := "artifact_uploads/v1/" + u.ID
	raw, e := m.Database().Get(key)
	if e != nil {
		t.Fatal(e)
	}
	var record map[string]any
	if e = json.Unmarshal(raw, &record); e != nil {
		t.Fatal(e)
	}
	record["state"] = "cancelling"
	record["Pending"] = nil
	raw, _ = json.Marshal(record)
	if e = m.Database().Put(key, raw); e != nil {
		t.Fatal(e)
	}
	if e = os.Remove(filepath.Join(filepath.Dir(m.StateDir()), "artifact-inbox", u.ID+".blob")); e != nil {
		t.Fatal(e)
	}
	state := callUpload(t, c, "artifact_upload_status", ArtifactUploadStatusInput{ID: u.ID})
	if state.State != "cancelling" {
		t.Fatal("lost durable cancellation evidence", state)
	}
	state = callUpload(t, c, "artifact_upload_cancel", ArtifactUploadInput{ID: u.ID, IdempotencyKey: "cancel-reconcile"})
	if state.State != "cancelled" {
		t.Fatal("cancel recovery blocked", state)
	}
}
