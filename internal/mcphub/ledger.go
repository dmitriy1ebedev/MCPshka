package mcphub

import (
	"bufio"
	"encoding/json"
	"os"
	"sort"
	"time"
)

// Call is one ledger line: what went where, never a value or a secret.
type Call struct {
	At       time.Time `json:"at"`
	Server   string    `json:"server"`
	Tool     string    `json:"tool"`
	Caller   string    `json:"caller,omitempty"` // client that asked (chatgpt, claude, …)
	Args     string    `json:"args"`             // Redact(): keys, types, sizes
	BytesOut int       `json:"bytes_out"`
	BytesIn  int       `json:"bytes_in"`
	Millis   int64     `json:"ms"`
	Status   string    `json:"status"` // OK, TOOL_ERROR, SECRET_BLOCKED, APPROVAL_REQUIRED, ERROR, TIMEOUT
	Error    string    `json:"error,omitempty"`
}

// ledgerMax rotates calls.jsonl to calls.1.jsonl (one old file kept).
const ledgerMax = 4 << 20

func (h *Hub) record(c Call) {
	raw, err := json.Marshal(c)
	if err != nil {
		return
	}
	h.calls.Lock()
	defer h.calls.Unlock()
	path := h.path("calls.jsonl")
	if st, err := os.Stat(path); err == nil && st.Size() > ledgerMax {
		_ = os.Rename(path, h.path("calls.1.jsonl"))
	}
	if err := os.MkdirAll(h.Dir, 0o700); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(raw, '\n'))
}

// Calls returns the newest calls first, at most limit, optionally of one server.
func (h *Hub) Calls(server string, limit int) []Call {
	all := h.readCalls()
	out := []Call{}
	for i := len(all) - 1; i >= 0 && len(out) < limit; i-- {
		if server == "" || all[i].Server == server {
			out = append(out, all[i])
		}
	}
	return out
}

func (h *Hub) readCalls() []Call {
	h.calls.Lock()
	defer h.calls.Unlock()
	var all []Call
	for _, name := range []string{"calls.1.jsonl", "calls.jsonl"} {
		f, err := os.Open(h.path(name))
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			var c Call
			if json.Unmarshal(sc.Bytes(), &c) == nil {
				all = append(all, c)
			}
		}
		f.Close()
	}
	return all
}

// ToolStat is the use of one tool.
type ToolStat struct {
	Tool   string    `json:"tool"`
	Calls  int       `json:"calls"`
	Errors int       `json:"errors"`
	Last   time.Time `json:"last"`
}

// Stat is the use of one server.
type Stat struct {
	Server   string     `json:"server"`
	Calls    int        `json:"calls"`
	Day      int        `json:"day"` // last 24 h
	Errors   int        `json:"errors"`
	Blocked  int        `json:"blocked"` // SECRET_BLOCKED
	BytesOut int        `json:"bytes_out"`
	BytesIn  int        `json:"bytes_in"`
	AvgMs    int64      `json:"avg_ms"`
	Last     time.Time  `json:"last"`
	Tools    []ToolStat `json:"tools"`
}

// Stats sums the ledger per server (only what the ledger still holds).
func (h *Hub) Stats() map[string]*Stat {
	now := h.Now()
	out := map[string]*Stat{}
	tools := map[string]map[string]*ToolStat{}
	var ms = map[string]int64{}
	for _, c := range h.readCalls() {
		s := out[c.Server]
		if s == nil {
			s = &Stat{Server: c.Server}
			out[c.Server] = s
			tools[c.Server] = map[string]*ToolStat{}
		}
		s.Calls++
		if now.Sub(c.At) < 24*time.Hour {
			s.Day++
		}
		bad := c.Status != "OK"
		if bad {
			s.Errors++
		}
		if c.Status == "SECRET_BLOCKED" {
			s.Blocked++
		}
		s.BytesOut += c.BytesOut
		s.BytesIn += c.BytesIn
		ms[c.Server] += c.Millis
		if c.At.After(s.Last) {
			s.Last = c.At
		}
		t := tools[c.Server][c.Tool]
		if t == nil {
			t = &ToolStat{Tool: c.Tool}
			tools[c.Server][c.Tool] = t
		}
		t.Calls++
		if bad {
			t.Errors++
		}
		if c.At.After(t.Last) {
			t.Last = c.At
		}
	}
	for id, s := range out {
		s.AvgMs = ms[id] / int64(s.Calls)
		for _, t := range tools[id] {
			s.Tools = append(s.Tools, *t)
		}
		sort.Slice(s.Tools, func(a, b int) bool { return s.Tools[a].Calls > s.Tools[b].Calls })
	}
	return out
}
