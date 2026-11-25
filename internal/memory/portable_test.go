package memory

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// ROADMAP P10 operations: inspect, forget (history kept) and erase (derived
// index and questions removed with the record).
func TestInspectForgetErase(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pref, _, err := s.Write(ctx, Record{Kind: "preference", Title: "Отчёты короткие", Body: "Владелец читает с телефона: ответ в три строки.", Questions: []string{"как писать отчёты"}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Inspect(ctx, pref.ID)
	if err != nil || got.Kind != "preference" || len(got.Questions) != 1 || got.Questions[0] != "как писать отчёты" {
		t.Fatalf("inspect: %+v %v", got, err)
	}
	find := func() int {
		hits, err := s.Recall(ctx, Query{Text: "как писать отчёты"})
		if err != nil {
			t.Fatal(err)
		}
		return len(hits)
	}
	if find() != 1 {
		t.Fatal("record not recalled before forget")
	}
	before := s.now()
	if r, err := s.Forget(ctx, pref.ID, false); err != nil || r.ValidTo == nil || r.SupersededBy != "forgotten" {
		t.Fatalf("forget: %+v %v", r, err)
	}
	if find() != 0 {
		t.Fatal("forgotten record still recalled")
	}
	if hits, _ := s.Recall(ctx, Query{Text: "как писать отчёты", AsOf: &before}); len(hits) != 1 {
		t.Fatal("history lost the forgotten record")
	}
	if _, err := s.Forget(ctx, pref.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, pref.ID); err != os.ErrNotExist {
		t.Fatalf("erased record still stored: %v", err)
	}
	rows, _ := s.exec(ctx, "SELECT (SELECT count(*) FROM mem_word)+(SELECT count(*) FROM mem_tri)+(SELECT count(*) FROM mem_q) AS n;")
	if len(rows) != 1 || rows[0]["n"] != float64(0) {
		t.Fatalf("derived rows left after erase: %v", rows)
	}
}

// ROADMAP P10 export/import: versioned, hashed, without clear-text secrets,
// trust marks not taken on faith, repeatable.
func TestPortableRoundTrip(t *testing.T) {
	ctx := context.Background()
	src, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	src.now = func() time.Time { at = at.Add(time.Hour); return at }
	if _, _, err := src.Write(ctx, Record{Kind: "decision", Title: "Бэкап", Body: "Раз в неделю.", Canon: true}); err != nil {
		t.Fatal(err)
	}
	cur, _, err := src.Write(ctx, Record{Kind: "decision", Title: "Бэкап", Body: "Каждую ночь, token ghp_abcdefghijklmnopqrstuvwxyz123456.", Canon: true, Questions: []string{"когда бэкап"}})
	if err != nil {
		t.Fatal(err)
	}
	var file bytes.Buffer
	rep, err := src.Export(ctx, &file)
	if err != nil || rep.Records != 2 || len(rep.SHA256) != 64 {
		t.Fatalf("export: %+v %v", rep, err)
	}
	if text := file.String(); strings.Contains(text, "ghp_abc") || !strings.HasPrefix(text, `{"format":"mcpshka.memory/v1"`) {
		t.Fatalf("export leaks a secret or lacks the header:\n%s", text)
	}

	dst, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	in, err := dst.Import(ctx, bytes.NewReader(file.Bytes()), false)
	if err != nil || in.Imported != 2 || in.Skipped != 0 {
		t.Fatalf("import: %+v %v", in, err)
	}
	again, err := dst.Import(ctx, bytes.NewReader(file.Bytes()), false)
	if err != nil || again.Imported != 0 || again.Skipped != 2 {
		t.Fatalf("repeat import: %+v %v", again, err)
	}
	hits, err := dst.Recall(ctx, Query{Text: "когда бэкап"})
	if err != nil || len(hits) != 1 || !strings.HasPrefix(hits[0].Body, "Каждую ночь") || hits[0].Canon {
		t.Fatalf("recall after import (one current record, canon not taken on faith): %+v %v", hits, err)
	}
	old := time.Date(2026, 11, 1, 11, 30, 0, 0, time.UTC)
	if past, _ := dst.Recall(ctx, Query{Text: "бэкап", AsOf: &old}); len(past) != 1 || past[0].Body != "Раз в неделю." {
		t.Fatalf("history after import: %+v", past)
	}
	_ = cur

	tampered := strings.Replace(file.String(), "Раз в неделю", "Раз в год", 1)
	if _, err := dst.Import(ctx, strings.NewReader(tampered), false); err == nil {
		t.Fatal("edited file accepted with a stale sha256")
	}
	if _, err := dst.Import(ctx, strings.NewReader("{}\n"), false); err == nil {
		t.Fatal("foreign file accepted")
	}
}

// ROADMAP P11 indexes: one recall narrowed to incidents, user knowledge, a
// device or an artifact.
func TestRecallFiltersByKindAndTag(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []Record{
		{Kind: "problem", Title: "Zigbee упал под нагрузкой", Body: "Координатор отвалился при полном тесте.", Tags: []string{"device:h96"}},
		{Kind: "decision", Title: "Zigbee важнее тяжёлых задач", Body: "Дом получает приоритет перед тестами."},
		{Kind: "preference", Title: "Про Zigbee писать коротко", Body: "Владелец читает с телефона."},
	} {
		if _, _, err := s.Write(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	titles := func(q Query) []string {
		q.Text = "zigbee"
		hits, err := s.Recall(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, h := range hits {
			out = append(out, h.Kind)
		}
		return out
	}
	if all := titles(Query{}); len(all) != 3 {
		t.Fatalf("unfiltered: %v", all)
	}
	for _, c := range []struct {
		q    Query
		want string
	}{{Query{Kind: "problem"}, "problem"}, {Query{Kind: "Preference"}, "preference"}, {Query{Tag: "device:H96"}, "problem"}} {
		if got := titles(c.q); len(got) != 1 || got[0] != c.want {
			t.Fatalf("%+v: %v", c.q, got)
		}
	}
	if got := titles(Query{Kind: "decision", Tag: "device:h96"}); len(got) != 0 {
		t.Fatalf("kind and tag must both hold: %v", got)
	}
}
