package runnerhub

import (
	"errors"
	"math"
)

type CloudBudget struct {
	Month             string          `json:"month"`
	ReservedVCPUHours float64         `json:"reserved_vcpu_hours"`
	Jobs              map[string]bool `json:"jobs"`
}

// ReserveCloudWake charges a bounded session before waking the workspace.
// Unknown outcomes retain the reservation; the next UTC month resets it.
func (h *Hub) ReserveCloudWake(id string, limit, hours float64) error {
	if limit <= 0 || hours <= 0 || math.IsNaN(limit) || math.IsInf(limit, 0) || math.IsNaN(hours) || math.IsInf(hours, 0) {
		return errors.New("GIGAIDE_QUOTA_UNKNOWN")
	}
	return h.update(func(s *state) error {
		if find(s, id) == nil {
			return errors.New("runner job not found")
		}
		month := h.Now().UTC().Format("2006-01")
		if s.CloudBudget.Month != month {
			s.CloudBudget = CloudBudget{Month: month, Jobs: map[string]bool{}}
		}
		if s.CloudBudget.Jobs == nil {
			s.CloudBudget.Jobs = map[string]bool{}
		}
		if s.CloudBudget.Jobs[id] {
			return errors.New("GIGAIDE_WAKE_ALREADY_RESERVED")
		}
		if len(s.CloudBudget.Jobs) >= 1024 || s.CloudBudget.ReservedVCPUHours+hours > limit {
			return errors.New("GIGAIDE_QUOTA_EXHAUSTED")
		}
		s.CloudBudget.ReservedVCPUHours += hours
		s.CloudBudget.Jobs[id] = true
		return nil
	})
}
