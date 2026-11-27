package workflow

import (
	"mcpshka/internal/jobs"
	"sync"
	"time"
)

const maxRememberedAborts = 512

type abortKey struct {
	manager *jobs.Manager
	job     string
}
type abortPin struct {
	root, fingerprint, reason string
	at                        time.Time
	supervisor                jobs.OwnerIdentity
}

// This bounded process-local proof records only a returned launch failure. It
// never authorizes replay. A fresh Engine over the same manager can retry the
// aborted snapshot write; process death instead uses the durable supervisor
// fence. Eviction loses availability, never grants authority.
var launchAborts = struct {
	sync.Mutex
	pins map[abortKey]abortPin
}{pins: make(map[abortKey]abortPin)}

func (e *Engine) rememberAbort(r Run, reason string) {
	if len(reason) > 1024 {
		reason = reason[:1024]
	}
	launchAborts.Lock()
	defer launchAborts.Unlock()
	key := abortKey{e.manager, r.JobID}
	if _, ok := launchAborts.pins[key]; !ok && len(launchAborts.pins) >= maxRememberedAborts {
		for old := range launchAborts.pins {
			delete(launchAborts.pins, old)
			break
		}
	}
	launchAborts.pins[key] = abortPin{root: r.Root, fingerprint: r.Fingerprint, reason: reason, at: r.LaunchAt, supervisor: r.Supervisor}
}

func (e *Engine) forgottenAbort(r Run) {
	launchAborts.Lock()
	defer launchAborts.Unlock()
	delete(launchAborts.pins, abortKey{e.manager, r.JobID})
}

func (e *Engine) rememberedAbort(r Run) (string, bool) {
	launchAborts.Lock()
	defer launchAborts.Unlock()
	key := abortKey{e.manager, r.JobID}
	pin, ok := launchAborts.pins[key]
	if !ok {
		return "", false
	}
	if !r.LaunchClaimed || pin.root != r.Root || pin.fingerprint != r.Fingerprint || !pin.at.Equal(r.LaunchAt) || pin.supervisor != r.Supervisor {
		delete(launchAborts.pins, key)
		return "", false
	}
	return pin.reason, true
}
