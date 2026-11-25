package dockerterminal

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

type dockerEngine struct {
	client *http.Client
	dial   func(context.Context) (net.Conn, error)
}

func newDockerEngine() *dockerEngine {
	dial := func(ctx context.Context) (net.Conn, error) {
		d := net.Dialer{Timeout: 3 * time.Second}
		return d.DialContext(ctx, "unix", "/run/docker.sock")
	}
	return &dockerEngine{client: &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return dial(ctx) }}}, dial: dial}
}

func (e *dockerEngine) request(ctx context.Context, method, path, contentType string, body io.Reader, out any) error {
	r, err := http.NewRequestWithContext(ctx, method, "http://docker"+path, body)
	if err != nil {
		return err
	}
	r.Header.Set("Content-Type", contentType)
	resp, err := e.client.Do(r)
	if err != nil {
		return fmt.Errorf("Docker response lost: UNKNOWN: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20+1))
	if err != nil {
		return err
	}
	if len(b) > 1<<20 {
		return errors.New("Docker response limit")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Docker HTTP %d", resp.StatusCode)
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}

func (e *dockerEngine) json(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	return e.request(ctx, method, path, "application/json", body, out)
}

func (e *dockerEngine) attach(ctx context.Context, id string) (net.Conn, *bufio.Reader, error) {
	conn, err := e.dial(ctx)
	if err != nil {
		return nil, nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	r, err := http.NewRequest("POST", "http://docker/exec/"+id+"/start", bytes.NewBufferString(`{"Detach":false,"Tty":true}`))
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Connection", "Upgrade")
	r.Header.Set("Upgrade", "tcp")
	if err = r.Write(conn); err != nil {
		conn.Close()
		return nil, nil, err
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, r)
	if err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("Docker exec start outcome UNKNOWN: %w", err)
	}
	if resp.StatusCode != 101 && resp.StatusCode != 200 {
		resp.Body.Close()
		conn.Close()
		return nil, nil, fmt.Errorf("Docker exec start HTTP %d", resp.StatusCode)
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, reader, nil
}
