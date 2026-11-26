package hosttriage

import (
	"fmt"
	"strings"
	"syscall"

	"mcpshka/internal/heavyqueue"
	"mcpshka/internal/hostload"
)

type PhaseGateCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"` // PASS, FAIL, MISSING, RUNNING (blocking); WARN, INFO
	Detail string `json:"detail"`
}

// Pressure limits match the DIAG/H96 policy: per logical CPU, not absolute.
const (
	gateMinDiskBytes = 3 << 30
)

func Pressure() PhaseGateCheck {
	var reasons, facts []string
	r := hostload.Read()
	facts = append(facts, r.String())
	if r.Busy || r.Load1/float64(r.CPUs) > hostload.Enter {
		reasons = append(reasons, "CPU")
	}
	mem := heavyqueue.Memory()
	if mem.Available < 0 || mem.Total <= 0 {
		reasons = append(reasons, "memory measurement unavailable")
	} else {
		facts = append(facts, "memory "+mem.String())
		if mem.Available*10 < mem.Total || mem.Low() {
			reasons = append(reasons, "memory")
		}
	}
	var fs syscall.Statfs_t
	if syscall.Statfs("/", &fs) == nil {
		free := fs.Bavail * uint64(fs.Bsize)
		need := uint64(gateMinDiskBytes)
		if tenth := fs.Blocks * uint64(fs.Bsize) / 10; tenth > need {
			need = tenth
		}
		facts = append(facts, fmt.Sprintf("disk free %.1f GiB (need %.1f GiB)", float64(free)/(1<<30), float64(need)/(1<<30)))
		if free < need {
			reasons = append(reasons, "disk")
		}
	} else {
		reasons = append(reasons, "disk measurement unavailable")
	}
	c := PhaseGateCheck{Name: "host_pressure", Status: "PASS", Detail: strings.Join(facts, "; ")}
	if len(reasons) > 0 {
		// Momentary load never fails an accepted phase; it only defers new
		// heavy jobs (tests, builds, deploys) until the host calms down.
		c.Status, c.Detail = "WARN", "defer heavy jobs, over limit: "+strings.Join(reasons, ", ")+"; "+c.Detail
	}
	return c
}
