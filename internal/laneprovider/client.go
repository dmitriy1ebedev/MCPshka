package laneprovider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

const (
	DefaultSocket    = "/run/lane/api.sock"
	callTimeout      = 6 * time.Second
	maxRequestBytes  = 16 * 1024
	maxResponseBytes = 64 * 1024
	ExpectedRollback = 300
	MinRollback      = 30
	MaxRollback      = 900
)

type caller func(context.Context, string, map[string]any) (json.RawMessage, error)

type Service struct {
	call caller
}

type OutcomeUnknownError struct {
	Operation string
	Err       error
}

func (e *OutcomeUnknownError) Error() string {
	if e == nil {
		return "LANE mutation outcome is unknown"
	}
	return fmt.Sprintf("LANE %s outcome is unknown: %v", e.Operation, e.Err)
}

func (e *OutcomeUnknownError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (e *OutcomeUnknownError) UnknownOutcome() bool { return true }

type Preview struct {
	OK        bool     `json:"ok"`
	DryRun    bool     `json:"dry_run"`
	Mutations bool     `json:"mutations"`
	Changes   []string `json:"changes,omitempty"`
	Actual    struct {
		State  string   `json:"state,omitempty"`
		Errors []string `json:"errors,omitempty"`
	} `json:"actual"`
}

type Plan struct {
	OK   bool `json:"ok"`
	Plan struct {
		PlanID    string `json:"plan_id"`
		CreatedAt string `json:"created_at,omitempty"`
		ExpiresAt string `json:"expires_at,omitempty"`
	} `json:"plan"`
}

type StageResult struct {
	OK              bool   `json:"ok"`
	TxID            string `json:"tx_id,omitempty"`
	PlanID          string `json:"plan_id,omitempty"`
	Status          string `json:"status,omitempty"`
	RollbackArmed   bool   `json:"rollback_armed"`
	RollbackSeconds int    `json:"rollback_seconds,omitempty"`
	Idempotent      bool   `json:"idempotent,omitempty"`
}

type Status struct {
	OK              bool     `json:"ok"`
	Active          bool     `json:"active"`
	Status          string   `json:"status,omitempty"`
	TxID            string   `json:"tx_id,omitempty"`
	PlanID          string   `json:"plan_id,omitempty"`
	RollbackArmed   bool     `json:"rollback_armed"`
	RollbackSeconds int      `json:"rollback_seconds,omitempty"`
	LiveState       string   `json:"live_state,omitempty"`
	Errors          []string `json:"errors,omitempty"`
}

type Verify struct {
	OK     bool     `json:"ok"`
	Status string   `json:"status,omitempty"`
	Errors []string `json:"errors,omitempty"`
}

func New() *Service {
	return &Service{call: unixCaller(DefaultSocket)}
}

func newService(call caller) *Service {
	return &Service{call: call}
}

func (s *Service) Preview(ctx context.Context, client string) (Preview, error) {
	params := map[string]any{"dry_run": true}
	if strings.TrimSpace(client) != "" {
		params["client"] = strings.TrimSpace(client)
	}
	var out Preview
	if err := s.decode(ctx, "plan.create", params, &out); err != nil {
		return Preview{}, err
	}
	if !out.OK || !out.DryRun || out.Mutations {
		return Preview{}, errors.New("LANE plan preview did not remain read-only")
	}
	return out, nil
}

func (s *Service) CreatePlan(ctx context.Context, client string) (Plan, error) {
	params := map[string]any{}
	if strings.TrimSpace(client) != "" {
		params["client"] = strings.TrimSpace(client)
	}
	var out Plan
	if err := s.decode(ctx, "plan.create", params, &out); err != nil {
		return Plan{}, err
	}
	if !out.OK || !validPlanID(out.Plan.PlanID) {
		return Plan{}, errors.New("LANE did not return a valid plan")
	}
	return out, nil
}

func (s *Service) Apply(ctx context.Context, planID string) (StageResult, error) {
	if !validPlanID(planID) {
		return StageResult{}, errors.New("invalid LANE plan id")
	}
	var out StageResult
	if err := s.decode(ctx, "canary.action", map[string]any{"action": "apply", "plan": planID}, &out); err != nil {
		return StageResult{}, &OutcomeUnknownError{Operation: "apply", Err: err}
	}
	if !out.OK {
		return StageResult{}, errors.New("LANE canary apply failed")
	}
	return out, nil
}

func (s *Service) Status(ctx context.Context) (Status, error) {
	var raw struct {
		OK    bool `json:"ok"`
		State struct {
			Active          bool   `json:"active"`
			Status          string `json:"status,omitempty"`
			TxID            string `json:"tx_id,omitempty"`
			PlanID          string `json:"plan_id,omitempty"`
			RollbackArmed   bool   `json:"rollback_armed"`
			RollbackSeconds int    `json:"rollback_seconds,omitempty"`
		} `json:"state"`
		Live struct {
			State  string   `json:"state,omitempty"`
			Errors []string `json:"errors,omitempty"`
		} `json:"live"`
	}
	if err := s.decode(ctx, "canary.action", map[string]any{"action": "status"}, &raw); err != nil {
		return Status{}, err
	}
	if !raw.OK {
		return Status{}, errors.New("LANE canary status failed")
	}
	return Status{
		OK: true, Active: raw.State.Active, Status: raw.State.Status,
		TxID: raw.State.TxID, PlanID: raw.State.PlanID,
		RollbackArmed: raw.State.RollbackArmed, RollbackSeconds: raw.State.RollbackSeconds,
		LiveState: raw.Live.State, Errors: raw.Live.Errors,
	}, nil
}

func (s *Service) Verify(ctx context.Context) (Verify, error) {
	var raw map[string]any
	if err := s.decode(ctx, "canary.action", map[string]any{"action": "verify"}, &raw); err != nil {
		return Verify{}, err
	}
	out := Verify{}
	if ok, _ := raw["ok"].(bool); ok {
		out.OK = true
	}
	if status, _ := raw["status"].(string); status != "" {
		out.Status = status
	}
	if values, ok := raw["errors"].([]any); ok {
		for _, value := range values {
			if text, ok := value.(string); ok && len(out.Errors) < 32 {
				out.Errors = append(out.Errors, text)
			}
		}
	}
	return out, nil
}

func (s *Service) Commit(ctx context.Context, txID string) (StageResult, error) {
	return s.txAction(ctx, "commit", txID)
}

func (s *Service) Rollback(ctx context.Context, txID string) (StageResult, error) {
	return s.txAction(ctx, "rollback", txID)
}

func (s *Service) txAction(ctx context.Context, action, txID string) (StageResult, error) {
	if !validTxID(txID) {
		return StageResult{}, errors.New("invalid LANE transaction id")
	}
	var out StageResult
	if err := s.decode(ctx, "canary.action", map[string]any{"action": action, "tx": txID}, &out); err != nil {
		return StageResult{}, &OutcomeUnknownError{Operation: action, Err: err}
	}
	if !out.OK {
		return StageResult{}, fmt.Errorf("LANE canary %s failed", action)
	}
	return out, nil
}

func (s *Service) decode(ctx context.Context, operation string, params map[string]any, dst any) error {
	if s == nil || s.call == nil {
		return errors.New("LANE provider is not configured")
	}
	raw, err := s.call(ctx, operation, params)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("decode LANE %s result: %w", operation, err)
	}
	return nil
}

func validPlanID(id string) bool {
	return validID(id, "plan-")
}

func validTxID(id string) bool {
	return validID(id, "run-")
}

func validID(id, prefix string) bool {
	if !strings.HasPrefix(id, prefix) || len(id) > 128 {
		return false
	}
	for _, r := range id[len(prefix):] {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return len(id) > len(prefix)
}

func unixCaller(socketPath string) caller {
	return boundedCaller(socketPath, maxResponseBytes, callTimeout)
}

func boundedCaller(socketPath string, maxResponse int, timeout time.Duration) caller {
	return func(parent context.Context, operation string, params map[string]any) (json.RawMessage, error) {
		if strings.TrimSpace(operation) == "" {
			return nil, errors.New("LANE operation is required")
		}
		ctx, cancel := context.WithTimeout(parent, timeout)
		defer cancel()
		request := struct {
			APIVersion string         `json:"apiVersion"`
			RequestID  string         `json:"requestId"`
			Operation  string         `json:"operation"`
			Params     map[string]any `json:"params"`
		}{
			APIVersion: "lane.api/v1",
			RequestID:  fmt.Sprintf("mcpshka-%d", time.Now().UTC().UnixNano()),
			Operation:  operation,
			Params:     params,
		}
		data, err := json.Marshal(request)
		if err != nil {
			return nil, err
		}
		if len(data) > maxRequestBytes {
			return nil, errors.New("LANE request exceeds limit")
		}
		dialer := net.Dialer{}
		conn, err := dialer.DialContext(ctx, "unix", socketPath)
		if err != nil {
			return nil, fmt.Errorf("connect LANE API: %w", err)
		}
		defer conn.Close()
		if deadline, ok := ctx.Deadline(); ok {
			_ = conn.SetDeadline(deadline)
		}
		if _, err := conn.Write(append(data, '\n')); err != nil {
			return nil, fmt.Errorf("write LANE API: %w", err)
		}
		line, err := readBoundedLine(bufio.NewReaderSize(conn, 4096), maxResponse)
		if err != nil {
			return nil, fmt.Errorf("read LANE API: %w", err)
		}
		var envelope struct {
			OK     bool            `json:"ok"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error,omitempty"`
		}
		if err := json.Unmarshal(line, &envelope); err != nil {
			return nil, fmt.Errorf("decode LANE envelope: %w", err)
		}
		if !envelope.OK {
			if envelope.Error != nil && envelope.Error.Message != "" {
				return nil, errors.New("LANE API: " + envelope.Error.Message)
			}
			return nil, errors.New("LANE API request failed")
		}
		if len(envelope.Result) == 0 {
			return json.RawMessage("{}"), nil
		}
		return append(json.RawMessage(nil), envelope.Result...), nil
	}
}

func readBoundedLine(r *bufio.Reader, limit int) ([]byte, error) {
	var out bytes.Buffer
	for {
		part, prefix, err := r.ReadLine()
		if err != nil {
			return nil, err
		}
		if out.Len()+len(part) > limit {
			return nil, errors.New("LANE response exceeds limit")
		}
		_, _ = out.Write(part)
		if !prefix {
			return out.Bytes(), nil
		}
	}
}
