package dockerterminal

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func terminalFixture(t *testing.T) (*terminalManager, []string, terminalReceipt) {
	t.Helper()
	parent := t.TempDir()
	if e := os.Chmod(parent, 0700); e != nil {
		t.Fatal(e)
	}
	m := &terminalManager{root: filepath.Join(parent, "terminals"), uid: uint32(os.Getuid()), live: map[string]*liveTerminal{}}
	if e := m.privateDir(); e != nil {
		t.Fatal(e)
	}
	r := terminalReceipt{Owner: "alice", ContainerID: strings.Repeat("c", 64), Status: TerminalStatus{ID: strings.Repeat("a", 32), Container: "homeassistant", ExecID: strings.Repeat("b", 64), State: "running"}, StartAcknowledged: true, Operations: map[string]string{}}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		if q.Method == "GET" && strings.HasPrefix(q.URL.Path, "/containers/") {
			fmt.Fprintf(w, `{"Id":%q}`, r.ContainerID)
			return
		}
		if q.Method != "GET" || q.URL.Path != "/exec/"+r.Status.ExecID+"/json" {
			t.Errorf("unexpected request: %s %s", q.Method, q.URL.Path)
			w.WriteHeader(400)
			return
		}
		fmt.Fprintf(w, `{"ID":%q,"ContainerID":%q,"Running":true,"Pid":123}`, r.Status.ExecID, r.ContainerID)
	}))
	t.Cleanup(s.Close)
	m.engine = &dockerEngine{client: &http.Client{Transport: rewriteDockerTransport{s.URL, http.DefaultTransport}}}
	if e := m.save(r); e != nil {
		t.Fatal(e)
	}
	return m, []string{"homeassistant"}, r
}

type rewriteDockerTransport struct {
	url  string
	base http.RoundTripper
}

func (r rewriteDockerTransport) RoundTrip(q *http.Request) (*http.Response, error) {
	clone := q.Clone(q.Context())
	clone.URL.Scheme = "http"
	clone.URL.Host = strings.TrimPrefix(r.url, "http://")
	return r.base.RoundTrip(clone)
}

func TestTerminalOwnershipAndUnknownNeverRespawns(t *testing.T) {
	m, cfg, r := terminalFixture(t)
	ctx := context.Background()
	if _, e := m.run(ctx, cfg, TerminalInput{Action: "status", Owner: "bob", ID: r.Status.ID}); e == nil {
		t.Fatal("foreign terminal read")
	}
	if _, e := m.run(ctx, cfg, TerminalInput{Action: "write", Owner: "bob", ID: r.Status.ID, Key: "once", Data: "eA=="}); e == nil {
		t.Fatal("foreign terminal write")
	}
	out, e := m.run(ctx, cfg, TerminalInput{Action: "status", Owner: "alice", ID: r.Status.ID})
	if e != nil || out.State != "unknown" || out.Fenced {
		t.Fatalf("lost transport: %+v %v", out, e)
	}
	if _, e = m.run(ctx, cfg, TerminalInput{Action: "open", Owner: "alice", Container: "homeassistant", Key: "replacement"}); e == nil {
		t.Fatal("unknown session replaced")
	}
	if _, e = m.run(ctx, cfg, TerminalInput{Action: "open", Owner: "alice", Container: "homeassistant-secret", Key: "sibling"}); e == nil {
		t.Fatal("sibling container admitted")
	}
}

func TestTerminalInputRetryDoesNotReplayAndIntentHolds(t *testing.T) {
	m, cfg, r := terminalFixture(t)
	a, b := net.Pipe()
	t.Cleanup(func() { a.Close(); b.Close() })
	m.live[r.Status.ID] = &liveTerminal{conn: a}
	in := TerminalInput{Action: "write", Owner: "alice", ID: r.Status.ID, Key: "input-1", Data: base64.StdEncoding.EncodeToString([]byte("id\n"))}
	read := make(chan string, 1)
	go func() { data := make([]byte, 3); n, _ := b.Read(data); read <- string(data[:n]) }()
	if _, e := m.run(context.Background(), cfg, in); e != nil {
		t.Fatal(e)
	}
	if <-read != "id\n" {
		t.Fatal("input mismatch")
	}
	if _, e := m.run(context.Background(), cfg, in); e != nil {
		t.Fatal(e)
	}
	in.Data = "eA=="
	if _, e := m.run(context.Background(), cfg, in); e == nil {
		t.Fatal("key conflict admitted")
	}
	loaded, e := m.load(r.Status.ID)
	if e != nil {
		t.Fatal(e)
	}
	loaded.Operations["lost"] = "intent:unverified"
	if e = m.save(loaded); e != nil {
		t.Fatal(e)
	}
	in.Key = "lost"
	if _, e = m.run(context.Background(), cfg, in); e == nil {
		t.Fatal("unknown input retransmitted")
	}
	data, e := os.ReadFile(filepath.Join(m.root, r.Status.ID+".json"))
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(data), "id\\n") || strings.Contains(string(data), "aWQK") {
		t.Fatal("stdin persisted")
	}
}

func TestTerminalReceiptRejectsSymlinkAndDirectoryAlias(t *testing.T) {
	m, _, r := terminalFixture(t)
	p := filepath.Join(m.root, r.Status.ID+".json")
	b, e := json.Marshal(r)
	if e != nil {
		t.Fatal(e)
	}
	target := filepath.Join(filepath.Dir(m.root), "target")
	if e = os.WriteFile(target, b, 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.Remove(p); e != nil {
		t.Fatal(e)
	}
	if e = os.Symlink(target, p); e != nil {
		t.Fatal(e)
	}
	if _, e = m.load(r.Status.ID); e == nil {
		t.Fatal("receipt alias admitted")
	}
	parent := filepath.Dir(m.root)
	if e = os.Chmod(parent, 0755); e != nil {
		t.Fatal(e)
	}
	if e = m.privateDir(); e == nil {
		t.Fatal("readable terminal parent admitted")
	}
	if e = os.Chmod(parent, 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.Chmod(m.root, 0777); e != nil {
		t.Fatal(e)
	}
	if e = m.privateDir(); e == nil {
		t.Fatal("writable state directory admitted")
	}
}

func TestSupervisorStatusRequiresDedicatedIdentityAndNarrowCapabilities(t *testing.T) {
	good := "Uid:\t65533\t65533\t65533\t65533\nCapEff:\t00000000000000e0\nTracerPid:\t0\n"
	if e := parseSupervisorStatus(good); e != nil {
		t.Fatal(e)
	}
	for _, bad := range []string{strings.ReplaceAll(good, "65533", "0"), strings.Replace(good, "00e0", "ffff", 1), strings.Replace(good, "TracerPid:\t0", "TracerPid:\t123", 1), ""} {
		if e := parseSupervisorStatus(bad); e == nil {
			t.Fatal("untrusted supervisor identity admitted")
		}
	}
}

func TestTerminalWriterScopeUsesImmutableContainerID(t *testing.T) {
	m, cfg, r := terminalFixture(t)
	cfg = append(cfg, "torrserver")
	out, e := m.scope(context.Background(), "alice", "torrserver")
	if e != nil || out.ID != r.Status.ID || out.State == "free" {
		t.Fatalf("rename released writer: %+v %v", out, e)
	}
}

func TestSupervisorCapabilitiesSurviveArchiveEncoding(t *testing.T) {
	capability := make([]byte, 20)
	binary.LittleEndian.PutUint32(capability, 0x02000001)
	binary.LittleEndian.PutUint32(capability[4:], uint32(supervisorCaps))
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	if e := w.WriteHeader(&tar.Header{Name: "helper", Mode: 0555, Typeflag: tar.TypeReg, Xattrs: map[string]string{"security.capability": string(capability)}}); e != nil {
		t.Fatal(e)
	}
	if e := w.Close(); e != nil {
		t.Fatal(e)
	}
	h, e := tar.NewReader(&b).Next()
	if e != nil {
		t.Fatal(e)
	}
	if h.Xattrs["security.capability"] != string(capability) {
		t.Fatal("capability metadata changed")
	}
}

func TestShellSlotReservationAndReceiptLimit(t *testing.T) {
	m, _, r := terminalFixture(t)
	r.Status.ShellUID = 60000
	if e := m.save(r); e != nil {
		t.Fatal(e)
	}
	uid, e := m.shellSlot()
	if e != nil || uid == 60000 || uid < 60000 || uid >= 60008 {
		t.Fatalf("occupied UID reused: %d %v", uid, e)
	}
	for i := 0; i < 127; i++ {
		r.Status.ID = fmt.Sprintf("%032x", i)
		r.Status.Fenced = true
		if e = m.save(r); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = m.shellSlot(); e == nil {
		t.Fatal("record129 admission permitted")
	}
}
