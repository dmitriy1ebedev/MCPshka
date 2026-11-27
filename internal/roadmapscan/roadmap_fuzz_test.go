package roadmapscan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"testing"
)

func FuzzRoadmapSerialization(f *testing.F) {
	for _, seed := range []string{"## P4 — Recovery\n- [ ] verify\n", "## P1 — DONE\n- [x] gate\n## P2 — Next\n- [ ] deploy\n", "```\n## P9 — injected\n```\n", "## P1 — one\n## P1 — duplicate", "## P1 — \xff\n- [ ] verify\n"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > MaxBytes {
			return
		}
		h := sha256.Sum256([]byte(raw))
		digest := hex.EncodeToString(h[:])
		s, err := Parse(raw, digest)
		if err != nil {
			return
		}
		if s.SHA256 != digest || len(s.Phases) > MaxPhases {
			t.Fatal("lost pin or phase budget")
		}
		checks := 0
		seen := map[string]bool{}
		for _, p := range s.Phases {
			if seen[p.ID] || p.Completed < 0 || p.Completed > p.Total {
				t.Fatal("ambiguous phase evidence")
			}
			seen[p.ID] = true
			checks += p.Total
		}
		if checks > MaxChecks {
			t.Fatal("checkbox budget exceeded")
		}
		if s.Next != nil && (s.Next.DeclaredDone || s.Next.Deferred || s.Next.Total > 0 && s.Next.Completed == s.Next.Total) {
			t.Fatal("completed phase selected")
		}
		encoded, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		var recovered Snapshot
		if json.Unmarshal(encoded, &recovered) != nil || !reflect.DeepEqual(s, recovered) {
			t.Fatal("checkpoint serialization lost evidence")
		}
	})
}
