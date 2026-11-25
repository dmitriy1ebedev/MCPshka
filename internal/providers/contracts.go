package providers

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"
)

type Budget struct {
	ContextTokens *int     `json:"context_tokens"`
	WallSeconds   *int     `json:"wall_seconds"`
	CostLimit     *float64 `json:"cost_limit"`
}
type Identity struct {
	Budgets       Budget   `json:"budgets"`
	ID            string   `json:"id"`
	Kind          string   `json:"kind"`
	Trust         string   `json:"trust"`
	Capabilities  []string `json:"capabilities"`
	Authenticated Signal   `json:"authenticated"`
	Health        Signal   `json:"health"`
	Quota         Quota    `json:"quota"`
}
type Quota struct {
	State      string     `json:"state"`
	Remaining  *float64   `json:"remaining"`
	ResetAt    *time.Time `json:"reset_at"`
	ObservedAt *time.Time `json:"observed_at"`
	Source     string     `json:"source"`
}

func ObservedQuota(remaining *float64, reset *time.Time, observed time.Time, source string, now time.Time) Quota {
	q := Quota{State: "UNKNOWN", Source: source}
	if remaining == nil || source == "" || observed.IsZero() || observed.After(now) || now.Sub(observed) > 5*time.Minute || math.IsNaN(*remaining) || math.IsInf(*remaining, 0) || *remaining < 0 || reset != nil && !reset.After(observed) {
		return q
	}
	value := *remaining
	q.Remaining = &value
	at := observed.UTC()
	q.ObservedAt = &at
	if reset != nil {
		r := reset.UTC()
		q.ResetAt = &r
		if !r.After(now) {
			return Quota{State: "UNKNOWN", Source: source}
		}
	}
	q.State = "AVAILABLE"
	if value == 0 {
		q.State = "EXHAUSTED"
	}
	return q
}

type Request struct {
	ID              string
	PlanFingerprint string
	Payload         json.RawMessage
}
type Observation struct {
	ID              string
	PlanFingerprint string
	State           string
	EvidenceRefs    []string
	Advisory        bool
}
type Provider interface {
	Identity(context.Context) (Identity, error)
}
type ReasonerProvider interface {
	Provider
	Models(context.Context) ([]string, error)
	Ask(context.Context, Request) (Observation, error)
	Review(context.Context, Request) (Observation, error)
}
type ExecutionProvider interface {
	Provider
	Dispatch(context.Context, Request) (Observation, error)
	Reconcile(context.Context, string) (Observation, error)
}
type ConnectorProvider interface {
	Provider
	Read(context.Context, Request) (Observation, error)
}
type ObserverProvider interface {
	Provider
	Observe(context.Context, Request) (Observation, error)
}

// A model's output can inform a reviewer, never satisfy an execution gate.
func AdvisoryReview(r Request, o Observation) (Observation, error) {
	if r.ID == "" || r.PlanFingerprint == "" || o.ID != r.ID || o.PlanFingerprint != r.PlanFingerprint {
		return Observation{}, errors.New("PROVIDER_PLAN_MISMATCH")
	}
	o.Advisory = true
	return o, nil
}
