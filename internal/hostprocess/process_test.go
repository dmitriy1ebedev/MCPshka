package hostprocess

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestParseStatAndList(t *testing.T) {
	f := make([]string, 22)
	for i := range f {
		f[i] = "0"
	}
	f[0] = "S"
	f[1] = "7"
	f[11] = "100"
	f[12] = "100"
	f[19] = "1000"
	f[21] = "2"
	p, err := ParseStat("42 (name with ) parentheses) "+strings.Join(f, " "), 20)
	if err != nil || p.PID != 42 || p.PPID != 7 || p.CPU != 20 || p.RSS != 2*int64(os.Getpagesize()) {
		t.Fatal(p, err)
	}
	for _, raw := range []string{"", "1 (x) S", "1 (x) " + strings.Repeat("bad ", 22)} {
		if _, err := ParseStat(raw, 0); err == nil {
			t.Fatal("accepted bad stat", raw)
		}
	}
	if got := Cgroups("0::/system.slice/new.service/child\n"); len(got) != 1 || got[0] != "/system.slice/new.service/child" {
		t.Fatal(got)
	}
	out, err := List(fmt.Sprint(os.Getpid()), "pid", 1)
	if err != nil || len(out) != 1 || out[0].PID != os.Getpid() {
		t.Fatal(out, err)
	}
	if _, err = List("", "bad", 1); err == nil {
		t.Fatal("bad sort")
	}
}
