// Package unitbus answers the read-only `systemctl show` queries MCPshka
// makes all the time (health of executor units, job scopes, control path,
// service status) over one kept-open D-Bus connection instead of starting
// systemctl each time (MCP-164, MCP-159: an idle broker and server forked
// systemctl ~20 times a minute; each fork on a swapping H96 costs major
// faults). The output is byte-for-byte what systemctl prints for the same
// arguments; anything this package cannot render exactly — other flags,
// timestamps, unknown property types — returns ok=false and the caller runs
// systemctl as before. No dependency beyond the standard library.
package unitbus

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// Disabled turns the bus path off (MCPSHKA_UNITBUS=0): every query goes to
// systemctl.
func Disabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MCPSHKA_UNITBUS"))) {
	case "0", "false", "off":
		return true
	}
	return false
}

type query struct {
	units []string
	props []string
	value bool
}

// parseArgs accepts `show [--no-pager] [--value] --property=A,B | -p A ... [--] UNIT...`.
func parseArgs(args []string) (query, bool) {
	var q query
	if len(args) < 2 || args[0] != "show" {
		return q, false
	}
	operands := false
	for i := 1; i < len(args); i++ {
		a := args[i]
		switch {
		case operands || !strings.HasPrefix(a, "-"):
			q.units = append(q.units, a)
		case a == "--":
			operands = true
		case a == "--no-pager":
		case a == "--value":
			q.value = true
		case strings.HasPrefix(a, "--property="):
			q.props = append(q.props, strings.Split(strings.TrimPrefix(a, "--property="), ",")...)
		case a == "-p" || a == "--property":
			if i+1 >= len(args) {
				return q, false
			}
			i++
			q.props = append(q.props, strings.Split(args[i], ",")...)
		default:
			return q, false
		}
	}
	if len(q.units) == 0 || len(q.props) == 0 {
		return q, false
	}
	for _, p := range q.props {
		if p == "" {
			return q, false
		}
	}
	for _, u := range q.units {
		// systemctl appends ".service" to a bare name and mangles odd ones;
		// only full, plain unit names (or globs of them) are answered here.
		dot := strings.LastIndexByte(u, '.')
		if dot <= 0 || dot == len(u)-1 || len(u) > 256 || strings.ContainsAny(u, "/\\ \t\n") {
			return q, false
		}
	}
	return q, true
}

func glob(u string) bool { return strings.ContainsAny(u, "*?[") }

// Show returns what `systemctl args...` would print, or ok=false.
func Show(ctx context.Context, args ...string) ([]byte, bool) {
	q, ok := parseArgs(args)
	if !ok || Disabled() {
		return nil, false
	}
	var out []byte
	err := do(ctx, func(c *conn) error {
		var units, patterns []string
		for _, u := range q.units {
			if glob(u) {
				patterns = append(patterns, u)
			} else {
				units = append(units, u)
			}
		}
		if len(patterns) > 0 {
			names, err := c.listUnits(ctx, patterns)
			if err != nil {
				return err
			}
			units = append(units, names...)
		}
		var b bytes.Buffer
		for i, u := range units {
			props, err := c.unitProps(ctx, u)
			if err != nil {
				return err
			}
			if i > 0 {
				// systemctl separates units with an empty line, --value too.
				b.WriteByte('\n')
			}
			if err := render(&b, props, q); err != nil {
				return err
			}
		}
		out = b.Bytes()
		return nil
	})
	if err != nil {
		return nil, false
	}
	return out, true
}

// errUnsupported marks a property this package will not guess the
// systemctl spelling of; the whole query then goes to systemctl.
var errUnsupported = fmt.Errorf("unitbus: property format unsupported")

func render(b *bytes.Buffer, props []prop, q query) error {
	want := map[string]bool{}
	for _, p := range q.props {
		want[p] = true
	}
	for _, p := range props {
		if !want[p.name] {
			continue
		}
		v, err := format(p.name, p.v)
		if err != nil {
			return err
		}
		if !q.value {
			b.WriteString(p.name)
			b.WriteByte('=')
		}
		b.WriteString(v)
		b.WriteByte('\n')
	}
	return nil
}

const infinity = ^uint64(0)

const plainItem = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_.@:+"

// format spells a value the way systemd 257 bus_print_property does, for
// the types and names checked against systemctl on H96.
func format(name string, v Variant) (string, error) {
	switch x := v.Value.(type) {
	case string:
		if v.Sig != "s" && v.Sig != "o" {
			return "", errUnsupported
		}
		if strings.ContainsAny(x, "\n\x00") {
			return "", errUnsupported
		}
		return x, nil
	case bool:
		if x {
			return "yes", nil
		}
		return "no", nil
	case int32:
		return strconv.FormatInt(int64(x), 10), nil
	case uint32:
		if strings.Contains(name, "UMask") || strings.Contains(name, "Mode") || name == "UID" || name == "GID" {
			return "", errUnsupported
		}
		return strconv.FormatUint(uint64(x), 10), nil
	case uint64:
		switch {
		case strings.HasSuffix(name, "Monotonic") || name == "NRestarts":
			return strconv.FormatUint(x, 10), nil
		case strings.HasSuffix(name, "USec") && !strings.HasSuffix(name, "Timestamp"):
			return timespan(x), nil
		}
		return "", errUnsupported
	case []byte:
		if name == "InvocationID" {
			if len(x) == 0 {
				return "", nil
			}
			if len(x) == 16 {
				return hex.EncodeToString(x), nil
			}
		}
		return "", errUnsupported
	case []any:
		if v.Sig != "as" {
			return "", errUnsupported
		}
		parts := make([]string, 0, len(x))
		for _, s := range x {
			// systemctl quotes and escapes list items with anything beyond a
			// plain unit-name alphabet (system-systemd\\x2dfsck.slice).
			str := s.(string)
			if str == "" || strings.Trim(str, plainItem) != "" {
				return "", errUnsupported
			}
			parts = append(parts, str)
		}
		return strings.Join(parts, " "), nil
	}
	return "", errUnsupported
}

var spans = []struct {
	suffix string
	usec   uint64
}{
	{"y", 31557600 * 1000000},
	{"month", 2629800 * 1000000},
	{"w", 7 * 86400 * 1000000},
	{"d", 86400 * 1000000},
	{"h", 3600 * 1000000},
	{"min", 60 * 1000000},
	{"s", 1000000},
	{"ms", 1000},
	{"us", 1},
}

// timespan is systemd's format_timespan with 1 µs accuracy.
func timespan(t uint64) string {
	if t == infinity {
		return "infinity"
	}
	if t == 0 {
		return "0"
	}
	var b strings.Builder
	for _, s := range spans {
		if t == 0 {
			break
		}
		if t < s.usec {
			continue
		}
		a, rem := t/s.usec, t%s.usec
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		if t < 60*1000000 && rem > 0 {
			digits := 0
			for c := s.usec; c > 1; c /= 10 {
				digits++
			}
			if digits > 0 {
				fmt.Fprintf(&b, "%d.%0*d%s", a, digits, rem, s.suffix)
				break
			}
		}
		fmt.Fprintf(&b, "%d%s", a, s.suffix)
		t = rem
	}
	return b.String()
}

// Output is exec.Command("/usr/bin/systemctl", args...).Output() that
// answers show queries over the bus when it can.
func Output(ctx context.Context, args ...string) ([]byte, error) {
	if out, ok := Show(ctx, args...); ok {
		return out, nil
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/systemctl", args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
	return cmd.Output()
}

// ErrTooLarge is a systemctl answer over the caller's evidence budget.
var ErrTooLarge = errors.New("systemctl output over budget")

type capped struct {
	b     bytes.Buffer
	limit int
}

func (c *capped) Write(p []byte) (int, error) {
	if c.b.Len()+len(p) > c.limit {
		return 0, ErrTooLarge
	}
	return c.b.Write(p)
}

// Bounded is Output for evidence: stdout only, at most limit bytes either
// way (the executor fence and the broker's executor health).
func Bounded(ctx context.Context, limit int, args ...string) ([]byte, error) {
	if out, ok := Show(ctx, args...); ok {
		if len(out) > limit {
			return nil, ErrTooLarge
		}
		return out, nil
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/systemctl", args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
	out := &capped{limit: limit}
	cmd.Stdout = out
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return out.b.Bytes(), nil
}
