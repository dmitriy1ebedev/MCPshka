package rootbroker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mcpshka/internal/brokerwire"
	"mcpshka/internal/tools/lanetools"
	"net"
	"strings"
	"syscall"
	"time"
)

const (
	DefaultSocketPath = "/run/mcpshka-broker/broker.sock"
	MaxRequestBytes   = 16 * 1024
	MaxResponseBytes  = brokerwire.MaxResponseBytes
)

type Request = brokerwire.Request

type Response = brokerwire.Response

type BrokerRunner func(context.Context, Config, string, []string) ([]byte, error)

type Server struct {
	Config        Config
	AllowedUID    uint32
	AllowedPID    func() (int32, error)
	MaxConcurrent int
	Run           BrokerRunner
}

func (s *Server) Serve(ctx context.Context, ln *net.UnixListener) error {
	if ln == nil {
		return errors.New("nil broker listener")
	}
	if s.Run == nil {
		s.Run = func(ctx context.Context, cfg Config, family string, args []string) ([]byte, error) {
			return Run(ctx, cfg, family, args)
		}
	}
	limit := s.MaxConcurrent
	if limit <= 0 {
		limit = 8
	}
	if limit > 32 {
		limit = 32
	}
	sem := make(chan struct{}, limit)
	for {
		if err := ln.SetDeadline(time.Now().Add(time.Second)); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		conn, err := ln.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return err
		}
		select {
		case sem <- struct{}{}:
			go func() {
				defer func() { <-sem }()
				s.handle(conn)
			}()
		default:
			_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
			_ = brokerwire.WriteResponse(conn, Response{OK: false, Code: "broker_busy_before_dispatch", Error: "broker busy"})
			_ = conn.Close()
		}
	}
}

func (s *Server) handle(conn *net.UnixConn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(35 * time.Second))

	cred, err := peerCred(conn)
	if err != nil {
		_ = brokerwire.WriteResponse(conn, Response{OK: false, Error: "peer authentication failed"})
		return
	}
	if err := authorizePeer(cred, s.AllowedUID, s.AllowedPID); err != nil {
		_ = brokerwire.WriteResponse(conn, Response{OK: false, Error: err.Error()})
		return
	}

	data, err := io.ReadAll(io.LimitReader(conn, MaxRequestBytes+1))
	if err != nil {
		_ = brokerwire.WriteResponse(conn, Response{OK: false, Error: "read request failed"})
		return
	}
	if len(data) == 0 || len(data) > MaxRequestBytes {
		_ = brokerwire.WriteResponse(conn, Response{OK: false, Error: "request size invalid"})
		return
	}
	var req Request
	if err := json.Unmarshal(data, &req); err != nil {
		_ = brokerwire.WriteResponse(conn, Response{OK: false, Error: "invalid request json"})
		return
	}
	req.Family = strings.TrimSpace(req.Family)
	if len(req.Args) > 32 {
		_ = brokerwire.WriteResponse(conn, Response{OK: false, Error: "too many arguments"})
		return
	}
	for _, arg := range req.Args {
		if len(arg) > 4096 || strings.ContainsRune(arg, '\x00') {
			_ = brokerwire.WriteResponse(conn, Response{OK: false, Error: "invalid argument"})
			return
		}
	}

	ctx, cancel := lanetools.RequestContext(context.Background(), conn, req.Family, req.Args, commandTimeout)
	defer cancel()
	started := time.Now()
	out, runErr := s.Run(ctx, s.Config, req.Family, req.Args)
	if len(out) > maxOutputBytes {
		out = out[:maxOutputBytes]
		if runErr == nil {
			runErr = errors.New("broker output exceeded limit")
		}
	}
	resp := Response{OK: runErr == nil, Output: out}
	if runErr != nil {
		resp.Error = boundedError(runErr.Error())
	}
	brokerwire.WriteOutcome(ctx, conn, req.Family, started, resp, runErr)
}

func authorizePeer(cred *syscall.Ucred, allowedUID uint32, allowedPID func() (int32, error)) error {
	if cred == nil {
		return errors.New("peer authentication failed")
	}
	if cred.Uid == 0 {
		return nil
	}
	if cred.Uid != allowedUID {
		return errors.New("peer uid not authorized")
	}
	if allowedPID == nil {
		return errors.New("peer pid policy unavailable")
	}
	pid, err := allowedPID()
	if err != nil || pid <= 0 || cred.Pid != pid {
		return errors.New("peer pid not authorized")
	}
	return nil
}

func boundedError(v string) string {
	v = strings.TrimSpace(v)
	if len(v) > 2048 {
		v = v[:2048]
	}
	return v
}

func peerCred(conn *net.UnixConn) (*syscall.Ucred, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return nil, err
	}
	var cred *syscall.Ucred
	var sockErr error
	if err := raw.Control(func(fd uintptr) {
		cred, sockErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return nil, err
	}
	if sockErr != nil {
		return nil, sockErr
	}
	if cred == nil {
		return nil, fmt.Errorf("missing peer credentials")
	}
	return cred, nil
}
