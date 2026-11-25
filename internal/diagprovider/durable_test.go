package diagprovider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type uncertain struct {
	fake
	fail bool
}

func (u *uncertain) Start(ctx context.Context, key string, s Scope) (string, error) {
	u.starts++
	if u.fail {
		return "", errors.New("transport lost")
	}
	return "run", nil
}
func TestDurableReplayAndUnknown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.json")
	f := &uncertain{fake: fake{cap: Capabilities{Protocol: 1, ReadOnly: true}}}
	p, e := NewDurable(f, path)
	if e != nil {
		t.Fatal(e)
	}
	id, e := p.Start(context.Background(), "key", scope())
	if e != nil || id != "run" {
		t.Fatalf("%s %v", id, e)
	}
	p, e = NewDurable(f, path)
	if e != nil {
		t.Fatal(e)
	}
	id, e = p.Start(context.Background(), "key", scope())
	if e != nil || id != "run" || f.starts != 1 {
		t.Fatalf("%s %v %d", id, e, f.starts)
	}
	f.fail = true
	_, e = p.Start(context.Background(), "unknown", scope())
	if !errors.Is(e, ErrUnknown) {
		t.Fatal(e)
	}
	p, e = NewDurable(f, path)
	if e != nil {
		t.Fatal(e)
	}
	_, e = p.Start(context.Background(), "unknown", scope())
	if !errors.Is(e, ErrUnknown) || f.starts != 2 {
		t.Fatalf("%v %d", e, f.starts)
	}
}
func TestScopeBoundReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.json")
	f := &fake{cap: Capabilities{Protocol: 1, ReadOnly: true}}
	p, err := NewDurable(f, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Start(context.Background(), "same", scope()); err != nil {
		t.Fatal(err)
	}
	changed := scope()
	changed.Target = "other-target"
	if _, err = p.Start(context.Background(), "same", changed); !errors.Is(err, ErrUnsafe) {
		t.Fatal("scope drift accepted:", err)
	}
	p, err = NewDurable(f, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Start(context.Background(), "same", changed); !errors.Is(err, ErrUnsafe) {
		t.Fatal("scope drift after restart:", err)
	}
	if id, err := p.Start(context.Background(), "same", scope()); err != nil || id != "run-1" || f.starts != 1 {
		t.Fatalf("replay %s %v %d", id, err, f.starts)
	}
}

func TestLegacyRunScopeUnknown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.json")
	if err := os.WriteFile(path, []byte(`{"legacy":"run-legacy"}`), 0600); err != nil {
		t.Fatal(err)
	}
	f := &fake{cap: Capabilities{Protocol: 1, ReadOnly: true}}
	p, err := NewDurable(f, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Start(context.Background(), "legacy", scope()); !errors.Is(err, ErrUnknown) || f.starts != 0 {
		t.Fatalf("unsafe replay %v %d", err, f.starts)
	}
}

func TestStrictScope(t *testing.T) {
	s := scope()
	s.Duration = 11 * time.Minute
	if e := validate(s); !errors.Is(e, ErrUnsafe) {
		t.Fatal(e)
	}
}

func TestRejectCorruptLedgerIdentifiers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.json")
	if e := os.WriteFile(path, []byte(`{"":"run"}`), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := NewDurable(&fake{}, path); !errors.Is(e, ErrUnsafe) {
		t.Fatal(e)
	}
}
