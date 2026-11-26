package unitbus

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// One connection per process, kept open, through the system bus: the bus
// daemon delivers only the signals a client subscribed to (none here).
// systemd's private socket is the fallback when the bus is absent: it sends
// every signal to every direct connection, and on H96 (25.11, 0.19.0) even
// a fresh one-call connection missed its 3 s reply under a stream of
// transient units — each broker systemctl request took 3.08 s and its
// client had already gone (MCP-256). Any failure closes the connection; the
// next call dials again, and the caller falls back to systemctl for this one.

var (
	privateSocket = "/run/systemd/private"
	systemSocket  = "/run/dbus/system_bus_socket"
	callTimeout   = 3 * time.Second
)

type conn struct {
	nc     net.Conn
	r      *bufio.Reader
	serial uint32
	dest   string
	// direct: systemd's private socket. systemd sends every signal to every
	// direct connection unconditionally (no Subscribe needed, Unsubscribe
	// does not stop it), so a connection kept open between calls fills with
	// unread signals (H96 25.11: 315 KB queued in a minute) and the next call
	// skips 64 of them and fails with "no reply". A direct connection serves
	// one call and is closed.
	direct bool
}

var shared struct {
	sync.Mutex
	c *conn
}

func dial(ctx context.Context) (*conn, error) {
	path, hello := systemSocket, true
	if _, err := os.Stat(systemSocket); err != nil && os.Geteuid() == 0 {
		if _, err := os.Stat(privateSocket); err == nil {
			path, hello = privateSocket, false
		}
	}
	var d net.Dialer
	nc, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return nil, err
	}
	c := &conn{nc: nc, r: bufio.NewReaderSize(nc, 64<<10), direct: !hello}
	if hello {
		c.dest = "org.freedesktop.systemd1"
	}
	if err := c.deadline(ctx); err != nil {
		nc.Close()
		return nil, err
	}
	uid := hex.EncodeToString([]byte(strconv.Itoa(os.Geteuid())))
	if _, err := io.WriteString(nc, "\x00AUTH EXTERNAL "+uid+"\r\n"); err != nil {
		nc.Close()
		return nil, err
	}
	line, err := c.r.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "OK ") {
		nc.Close()
		return nil, errors.Join(err, errors.New("unitbus: authentication refused"))
	}
	if _, err := io.WriteString(nc, "BEGIN\r\n"); err != nil {
		nc.Close()
		return nil, err
	}
	if hello {
		if _, _, err := c.call(ctx, "org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus", "Hello", "", nil); err != nil {
			nc.Close()
			return nil, err
		}
	}
	return c, nil
}

func (c *conn) deadline(ctx context.Context) error {
	dl := time.Now().Add(callTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(dl) {
		dl = d
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.nc.SetDeadline(dl)
}

// call sends one method call and waits for its reply, skipping signals and
// other traffic (NameAcquired after Hello).
func (c *conn) call(ctx context.Context, dest, path, iface, member, sig string, body []byte) (string, []byte, error) {
	if err := c.deadline(ctx); err != nil {
		return "", nil, err
	}
	c.serial++
	if _, err := c.nc.Write(call(c.serial, dest, path, iface, member, sig, body)); err != nil {
		return "", nil, err
	}
	for i := 0; i < 64; i++ {
		h, err := c.read()
		if err != nil {
			return "", nil, err
		}
		if (h.kind != msgReturn && h.kind != msgError) || h.replySerial != c.serial {
			continue
		}
		if h.kind == msgError {
			msg := ""
			if strings.HasPrefix(h.sig, "s") {
				if v, err := (&decoder{b: h.body}).value("s"); err == nil {
					msg = v.(string)
				}
			}
			return "", nil, fmt.Errorf("unitbus: %s: %s", h.errorName, msg)
		}
		return h.sig, h.body, nil
	}
	return "", nil, errors.New("unitbus: no reply")
}

func (c *conn) read() (header, error) {
	prefix := make([]byte, 16)
	if _, err := io.ReadFull(c.r, prefix); err != nil {
		return header{}, err
	}
	if prefix[0] != 'l' || prefix[3] != 1 {
		return header{}, errors.New("unitbus: unsupported message encoding")
	}
	body := binary.LittleEndian.Uint32(prefix[4:])
	fields := binary.LittleEndian.Uint32(prefix[12:])
	if body > maxBody || fields > maxFields {
		return header{}, errors.New("unitbus: message too large")
	}
	padded := (int(fields) + 7) &^ 7
	rest := make([]byte, padded+int(body))
	if _, err := io.ReadFull(c.r, rest); err != nil {
		return header{}, err
	}
	return parse(prefix, rest)
}

// do runs fn on the shared connection, dialing when needed; any error
// drops the connection so a half-read stream is never reused.
func do(ctx context.Context, fn func(*conn) error) error {
	shared.Lock()
	defer shared.Unlock()
	if shared.c == nil {
		c, err := dial(ctx)
		if err != nil {
			return err
		}
		shared.c = c
	}
	err := fn(shared.c)
	if err != nil || shared.c.direct {
		shared.c.nc.Close()
		shared.c = nil
	}
	return err
}

// prop is one property in the order systemd lists it.
type prop struct {
	name string
	v    Variant
}

func (c *conn) unitProps(ctx context.Context, unit string) ([]prop, error) {
	e := &encoder{}
	e.str("")
	sig, body, err := c.call(ctx, c.dest, unitPath(unit), "org.freedesktop.DBus.Properties", "GetAll", "s", e.b)
	if err != nil {
		return nil, err
	}
	if sig != "a{sv}" {
		return nil, fmt.Errorf("unitbus: GetAll returned %q", sig)
	}
	v, err := (&decoder{b: body}).value(sig)
	if err != nil {
		return nil, err
	}
	var out []prop
	for _, entry := range v.([]any) {
		kv := entry.([]any)
		out = append(out, prop{name: kv[0].(string), v: kv[1].(Variant)})
	}
	return out, nil
}

func (c *conn) listUnits(ctx context.Context, patterns []string) ([]string, error) {
	e := &encoder{}
	e.strs(nil)
	e.strs(patterns)
	sig, body, err := c.call(ctx, c.dest, "/org/freedesktop/systemd1", "org.freedesktop.systemd1.Manager", "ListUnitsByPatterns", "asas", e.b)
	if err != nil {
		return nil, err
	}
	if sig != "a(ssssssouso)" {
		return nil, fmt.Errorf("unitbus: ListUnitsByPatterns returned %q", sig)
	}
	v, err := (&decoder{b: body}).value(sig)
	if err != nil {
		return nil, err
	}
	// systemctl expands a pattern like list-units without --all: units
	// that follow another are hidden, inactive ones only with a pending
	// job; the order is systemd's.
	var names []string
	for _, u := range v.([]any) {
		f := u.([]any)
		if f[5].(string) != "" || f[3].(string) == "inactive" && f[7].(uint32) == 0 {
			continue
		}
		names = append(names, f[0].(string))
	}
	return names, nil
}

// unitPath is systemd's object path for a unit name (bus_path_encode).
func unitPath(unit string) string {
	var b strings.Builder
	b.WriteString("/org/freedesktop/systemd1/unit/")
	for i := 0; i < len(unit); i++ {
		c := unit[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' && i > 0 {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "_%02x", c)
	}
	return b.String()
}
