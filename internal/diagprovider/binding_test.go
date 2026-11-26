package diagprovider

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestDurableScopeBindingAndRequestedOutputLimit(t *testing.T) {
	f := &fake{cap: Capabilities{Protocol: 1, ReadOnly: true}, data: make([]byte, 4097)}
	p, e := NewDurable(f, filepath.Join(t.TempDir(), "ledger"))
	if e != nil {
		t.Fatal(e)
	}
	in := scope()
	if _, e = p.Start(context.Background(), "key", in); e != nil {
		t.Fatal(e)
	}
	changed := in
	changed.Target = "different"
	if _, e = p.Start(context.Background(), "key", changed); !errors.Is(e, ErrUnsafe) {
		t.Fatal("scope rebound", e)
	}
	if _, e = p.Status(context.Background(), "run-1"); !errors.Is(e, ErrUnsafe) {
		t.Fatal("requested budget ignored", e)
	}
}
