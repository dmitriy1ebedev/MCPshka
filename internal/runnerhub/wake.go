package runnerhub

import "time"

// ReserveWake prevents two drivers from opening the same cloud session.
// A lost caller leaves a pending wake until grace expires; it is never replayed.
func (h *Hub) ReserveWake(id, site string, grace time.Duration) (bool, error) {
	accepted := false
	err := h.update(func(s *state) error {
		j := find(s, id)
		if j == nil || j.State != Queued {
			return nil
		}
		latest := map[string]Wake{}
		for _, old := range s.Jobs {
			for _, w := range old.Wakes {
				if !w.At.Before(latest[w.Site].At) {
					latest[w.Site] = w
				}
			}
		}
		for _, w := range latest {
			if w.Err == "" && h.Now().Sub(w.At) < grace {
				return nil
			}
		}
		j.Wakes = append(j.Wakes, Wake{Site: site, At: h.Now()})
		accepted = true
		return nil
	})
	return accepted, err
}
