package runnerhub

import (
	"fmt"
	"strings"
	"time"
)

type RunnerView struct {
	Runner
	Alive    bool   `json:"alive"`
	LastTask string `json:"last_task,omitempty"`
}

func (h *Hub) Status() ([]RunnerView, []Job, error) {
	rs, js, err := h.Snapshot()
	out := make([]RunnerView, 0, len(rs))
	for _, r := range rs {
		v := RunnerView{Runner: r, Alive: !r.Disabled && h.Now().Sub(r.LastSeen) <= 2*time.Minute}
		for _, j := range js {
			if j.Runner == r.ID {
				v.LastTask = j.ID
				break
			}
		}
		out = append(out, v)
	}
	return out, js, err
}
func (h *Hub) Brief() string {
	rs, _, err := h.Status()
	if err != nil {
		return "Раннеры: статус недоступен"
	}
	parts := []string{}
	for _, r := range rs {
		state := "offline"
		if r.Alive {
			state = "online"
		}
		task := r.Job
		if task == "" {
			task = r.LastTask
		}
		parts = append(parts, fmt.Sprintf("%s %s %s", r.Name, state, task))
	}
	if len(parts) == 0 {
		return "Раннеры: нет зарегистрированных"
	}
	return "Раннеры: " + strings.Join(parts, "; ")
}
