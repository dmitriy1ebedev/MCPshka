package unitbus

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// The D-Bus wire format, only as far as reading systemd unit properties
// needs: little-endian messages, method calls with string arguments, and a
// generic reader for any reply signature. Alignment is relative to the start
// of the slice being read; a body always starts 8-aligned in its message.

const (
	msgCall   = 1
	msgReturn = 2
	msgError  = 3

	maxBody   = 8 << 20
	maxFields = 64 << 10
	maxDepth  = 32
)

// Variant is a D-Bus variant: its signature and the decoded value.
type Variant struct {
	Sig   string
	Value any
}

type encoder struct{ b []byte }

func (e *encoder) align(n int) {
	for len(e.b)%n != 0 {
		e.b = append(e.b, 0)
	}
}

func (e *encoder) uint32(v uint32) {
	e.align(4)
	e.b = binary.LittleEndian.AppendUint32(e.b, v)
}

func (e *encoder) str(s string) {
	e.uint32(uint32(len(s)))
	e.b = append(e.b, s...)
	e.b = append(e.b, 0)
}

func (e *encoder) sig(s string) {
	e.b = append(e.b, byte(len(s)))
	e.b = append(e.b, s...)
	e.b = append(e.b, 0)
}

func (e *encoder) strs(v []string) {
	e.uint32(0)
	at := len(e.b) - 4
	start := len(e.b)
	for _, s := range v {
		e.str(s)
	}
	binary.LittleEndian.PutUint32(e.b[at:], uint32(len(e.b)-start))
}

// call builds one method call; body is already encoded for signature sig.
func call(serial uint32, dest, path, iface, member, sig string, body []byte) []byte {
	e := &encoder{b: make([]byte, 0, 256+len(body))}
	e.b = append(e.b, 'l', msgCall, 0, 1)
	e.uint32(uint32(len(body)))
	e.uint32(serial)
	e.uint32(0)
	at := len(e.b) - 4
	e.align(8)
	start := len(e.b)
	field := func(code byte, s string, put func()) {
		e.align(8)
		e.b = append(e.b, code)
		e.sig(s)
		put()
	}
	field(1, "o", func() { e.str(path) })
	if iface != "" {
		field(2, "s", func() { e.str(iface) })
	}
	field(3, "s", func() { e.str(member) })
	if dest != "" {
		field(6, "s", func() { e.str(dest) })
	}
	if sig != "" {
		field(8, "g", func() { e.sig(sig) })
	}
	binary.LittleEndian.PutUint32(e.b[at:], uint32(len(e.b)-start))
	e.align(8)
	return append(e.b, body...)
}

type decoder struct {
	b     []byte
	off   int
	depth int
}

var errShort = errors.New("unitbus: truncated message")

func (d *decoder) align(n int) error {
	for d.off%n != 0 {
		if d.off >= len(d.b) || d.b[d.off] != 0 {
			return errors.New("unitbus: bad padding")
		}
		d.off++
	}
	return nil
}

func (d *decoder) take(n int) ([]byte, error) {
	if n < 0 || d.off+n > len(d.b) {
		return nil, errShort
	}
	p := d.b[d.off : d.off+n]
	d.off += n
	return p, nil
}

func (d *decoder) fixed(n int) ([]byte, error) {
	if err := d.align(n); err != nil {
		return nil, err
	}
	return d.take(n)
}

func (d *decoder) u32() (uint32, error) {
	p, err := d.fixed(4)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(p), nil
}

func (d *decoder) text(lenBytes int) (string, error) {
	var n int
	if lenBytes == 1 {
		p, err := d.take(1)
		if err != nil {
			return "", err
		}
		n = int(p[0])
	} else {
		v, err := d.u32()
		if err != nil {
			return "", err
		}
		if v > maxBody {
			return "", errShort
		}
		n = int(v)
	}
	p, err := d.take(n + 1)
	if err != nil {
		return "", err
	}
	if p[n] != 0 {
		return "", errors.New("unitbus: unterminated string")
	}
	return string(p[:n]), nil
}

// single splits the first complete type off a signature.
func single(sig string) (string, string, error) {
	if sig == "" {
		return "", "", errors.New("unitbus: empty signature")
	}
	switch sig[0] {
	case 'y', 'b', 'n', 'q', 'i', 'u', 'x', 't', 'd', 'h', 's', 'o', 'g', 'v':
		return sig[:1], sig[1:], nil
	case 'a':
		elem, rest, err := single(sig[1:])
		if err != nil {
			return "", "", err
		}
		return "a" + elem, rest, nil
	case '(', '{':
		open, close := sig[0], byte(')')
		if open == '{' {
			close = '}'
		}
		depth := 0
		for i := 0; i < len(sig); i++ {
			switch sig[i] {
			case '(', '{':
				depth++
			case ')', '}':
				depth--
				if depth == 0 {
					if sig[i] != close || i == 1 || open == '{' && !dictEntry(sig[1:i]) {
						return "", "", errors.New("unitbus: bad container signature")
					}
					return sig[:i+1], sig[i+1:], nil
				}
			}
		}
	}
	return "", "", fmt.Errorf("unitbus: bad signature %q", sig)
}

// dictEntry: a basic key type and exactly one value type.
func dictEntry(inner string) bool {
	k, rest, err := single(inner)
	if err != nil || len(k) != 1 || k == "v" {
		return false
	}
	_, rest, err = single(rest)
	return err == nil && rest == ""
}

func alignment(t byte) int {
	switch t {
	case 'y', 'g', 'v':
		return 1
	case 'n', 'q':
		return 2
	case 'x', 't', 'd', '(', '{':
		return 8
	}
	return 4
}

// values decodes every complete type of sig in order.
func (d *decoder) values(sig string) ([]any, error) {
	var out []any
	for sig != "" {
		t, rest, err := single(sig)
		if err != nil {
			return nil, err
		}
		v, err := d.value(t)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		sig = rest
	}
	return out, nil
}

// value decodes one complete type t.
func (d *decoder) value(t string) (any, error) {
	d.depth++
	defer func() { d.depth-- }()
	if d.depth > maxDepth {
		return nil, errors.New("unitbus: nesting too deep")
	}
	switch t[0] {
	case 'y':
		p, err := d.take(1)
		if err != nil {
			return nil, err
		}
		return p[0], nil
	case 'b':
		v, err := d.u32()
		if err != nil || v > 1 {
			return nil, errors.Join(err, errors.New("unitbus: bad boolean"))
		}
		return v == 1, nil
	case 'n', 'q':
		p, err := d.fixed(2)
		if err != nil {
			return nil, err
		}
		v := binary.LittleEndian.Uint16(p)
		if t[0] == 'n' {
			return int16(v), nil
		}
		return v, nil
	case 'i', 'u', 'h':
		v, err := d.u32()
		if err != nil {
			return nil, err
		}
		if t[0] == 'i' {
			return int32(v), nil
		}
		return v, nil
	case 'x', 't', 'd':
		p, err := d.fixed(8)
		if err != nil {
			return nil, err
		}
		v := binary.LittleEndian.Uint64(p)
		switch t[0] {
		case 'x':
			return int64(v), nil
		case 'd':
			return nil, errors.New("unitbus: double unsupported")
		}
		return v, nil
	case 's', 'o':
		return d.text(4)
	case 'g':
		return d.text(1)
	case 'v':
		s, err := d.text(1)
		if err != nil {
			return nil, err
		}
		if one, rest, err := single(s); err != nil || rest != "" || one == "" {
			return nil, errors.New("unitbus: bad variant signature")
		}
		v, err := d.value(s)
		if err != nil {
			return nil, err
		}
		return Variant{Sig: s, Value: v}, nil
	case 'a':
		n, err := d.u32()
		if err != nil {
			return nil, err
		}
		if n > maxBody {
			return nil, errShort
		}
		elem := t[1:]
		if err := d.align(alignment(elem[0])); err != nil {
			return nil, err
		}
		end := d.off + int(n)
		if end > len(d.b) {
			return nil, errShort
		}
		if elem == "y" {
			p, _ := d.take(int(n))
			return append([]byte(nil), p...), nil
		}
		out := []any{}
		for d.off < end {
			v, err := d.value(elem)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		if d.off != end {
			return nil, errors.New("unitbus: array overrun")
		}
		return out, nil
	case '(', '{':
		if err := d.align(8); err != nil {
			return nil, err
		}
		return d.values(t[1 : len(t)-1])
	}
	return nil, fmt.Errorf("unitbus: unsupported type %q", t)
}

type header struct {
	kind        byte
	serial      uint32
	replySerial uint32
	errorName   string
	sig         string
	body        []byte
}

// parse reads one complete message: the 16-byte prefix tells both lengths.
func parse(prefix []byte, rest []byte) (header, error) {
	h := header{kind: prefix[1], serial: binary.LittleEndian.Uint32(prefix[8:])}
	d := &decoder{b: append(append([]byte(nil), prefix...), rest...), off: 12}
	fields, err := d.value("a(yv)")
	if err != nil {
		return h, err
	}
	for _, f := range fields.([]any) {
		st := f.([]any)
		code, v := st[0].(byte), st[1].(Variant)
		switch {
		case code == 4 && v.Sig == "s":
			h.errorName = v.Value.(string)
		case code == 5 && v.Sig == "u":
			h.replySerial = v.Value.(uint32)
		case code == 8 && v.Sig == "g":
			h.sig = v.Value.(string)
		case code == 9:
			return h, errors.New("unitbus: file descriptors unsupported")
		}
	}
	if err := d.align(8); err != nil {
		return h, err
	}
	h.body = d.b[d.off:]
	return h, nil
}
