package haprovider

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Home Assistant keeps its registries (entities, devices, areas) behind the
// WebSocket API only. This is the smallest RFC 6455 client that API needs:
// one connection per call batch, masked text frames, ping/pong, fragments.

const maxFrame = 8 << 20

type wsConn struct {
	c  net.Conn
	rd *bufio.Reader
}

func (w *wsConn) write(op byte, payload []byte) error {
	hdr := []byte{0x80 | op}
	n := len(payload)
	switch {
	case n < 126:
		hdr = append(hdr, 0x80|byte(n))
	case n < 1<<16:
		hdr = append(hdr, 0x80|126, byte(n>>8), byte(n))
	default:
		hdr = append(hdr, 0x80|127)
		hdr = binary.BigEndian.AppendUint64(hdr, uint64(n))
	}
	mask := make([]byte, 4)
	rand.Read(mask)
	hdr = append(hdr, mask...)
	body := make([]byte, n)
	for i := range payload {
		body[i] = payload[i] ^ mask[i%4]
	}
	_, err := w.c.Write(append(hdr, body...))
	return err
}

// read returns the next complete text message, answering pings.
func (w *wsConn) read() ([]byte, error) {
	var msg []byte
	for {
		h := make([]byte, 2)
		if _, err := io.ReadFull(w.rd, h); err != nil {
			return nil, err
		}
		fin, op := h[0]&0x80 != 0, h[0]&0x0f
		n := uint64(h[1] & 0x7f)
		switch n {
		case 126:
			b := make([]byte, 2)
			if _, err := io.ReadFull(w.rd, b); err != nil {
				return nil, err
			}
			n = uint64(binary.BigEndian.Uint16(b))
		case 127:
			b := make([]byte, 8)
			if _, err := io.ReadFull(w.rd, b); err != nil {
				return nil, err
			}
			n = binary.BigEndian.Uint64(b)
		}
		if n > maxFrame || uint64(len(msg))+n > maxFrame {
			return nil, errors.New("home assistant websocket message too large")
		}
		data := make([]byte, n)
		if _, err := io.ReadFull(w.rd, data); err != nil {
			return nil, err
		}
		switch op {
		case 0x8:
			return nil, errors.New("home assistant closed the websocket")
		case 0x9:
			if err := w.write(0xA, data); err != nil {
				return nil, err
			}
			continue
		case 0xA:
			continue
		}
		msg = append(msg, data...)
		if fin {
			return msg, nil
		}
	}
}

func (c *Client) dialWS(ctx context.Context) (*wsConn, error) {
	u, err := url.Parse(c.URL)
	if err != nil || u.Scheme != "http" {
		return nil, errors.New("home assistant websocket needs a local http:// URL")
	}
	d := net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", u.Host)
	if err != nil {
		return nil, err
	}
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	} else {
		conn.SetDeadline(time.Now().Add(30 * time.Second))
	}
	keyRaw := make([]byte, 16)
	rand.Read(keyRaw)
	key := base64.StdEncoding.EncodeToString(keyRaw)
	fmt.Fprintf(conn, "GET /api/websocket HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n", u.Host, key)
	rd := bufio.NewReader(conn)
	resp, err := http.ReadResponse(rd, &http.Request{Method: http.MethodGet})
	if err != nil {
		conn.Close()
		return nil, err
	}
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	if resp.StatusCode != http.StatusSwitchingProtocols || resp.Header.Get("Sec-WebSocket-Accept") != base64.StdEncoding.EncodeToString(sum[:]) {
		conn.Close()
		return nil, fmt.Errorf("home assistant websocket upgrade failed: %s", resp.Status)
	}
	return &wsConn{c: conn, rd: rd}, nil
}

// WS sends commands (each a {"type": ..., ...} object) on one authenticated
// connection and returns their results in order.
func (c *Client) WS(ctx context.Context, commands ...map[string]any) ([]json.RawMessage, error) {
	token, err := c.token()
	if err != nil {
		return nil, err
	}
	w, err := c.dialWS(ctx)
	if err != nil {
		return nil, err
	}
	defer w.c.Close()
	var hello struct {
		Type string `json:"type"`
	}
	if raw, err := w.read(); err != nil || json.Unmarshal(raw, &hello) != nil || hello.Type != "auth_required" {
		return nil, errors.New("home assistant websocket handshake failed")
	}
	auth, _ := json.Marshal(map[string]string{"type": "auth", "access_token": token})
	if err := w.write(0x1, auth); err != nil {
		return nil, err
	}
	if raw, err := w.read(); err != nil || json.Unmarshal(raw, &hello) != nil || hello.Type != "auth_ok" {
		return nil, errors.New("home assistant rejected the token")
	}
	out := make([]json.RawMessage, len(commands))
	for i, cmd := range commands {
		msg := map[string]any{"id": i + 1}
		for k, v := range cmd {
			msg[k] = v
		}
		raw, _ := json.Marshal(msg)
		if err := w.write(0x1, raw); err != nil {
			return nil, err
		}
		for {
			reply, err := w.read()
			if err != nil {
				return nil, err
			}
			var r struct {
				ID      int             `json:"id"`
				Type    string          `json:"type"`
				Success bool            `json:"success"`
				Result  json.RawMessage `json:"result"`
				Error   *struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal(reply, &r) != nil || r.ID != i+1 || r.Type != "result" {
				continue
			}
			if !r.Success {
				msg := "unknown error"
				if r.Error != nil {
					msg = r.Error.Code + ": " + r.Error.Message
				}
				return out, fmt.Errorf("home assistant %v: %s", cmd["type"], strings.TrimSpace(msg))
			}
			out[i] = r.Result
			break
		}
	}
	return out, nil
}

// REST performs an authenticated JSON request against the HA REST API.
func (c *Client) REST(ctx context.Context, method, path string, body, out any) error {
	return c.do(ctx, method, path, body, out)
}
