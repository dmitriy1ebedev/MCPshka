package releasecleanup

import "time"

type Upgrade struct {
	Version             string    `json:"version"`
	CurrentVersion      string    `json:"current_version"`
	Operation           string    `json:"operation_id"`
	Image               string    `json:"controller_sha256"`
	Previous            string    `json:"current_controller_sha256"`
	Tree                string    `json:"source_tree"`
	Broker              string    `json:"broker_sha256"`
	PreviousBroker      string    `json:"current_broker_sha256"`
	ControllerCandidate string    `json:"controller_candidate,omitempty"`
	BrokerCandidate     string    `json:"broker_candidate,omitempty"`
	State               string    `json:"-"`
	At                  time.Time `json:"-"`
}

// Input is ordered by terminal journal mtime, newest first. Unknown/unfinished
// operations pin their own version and rollback, not unrelated completed releases.
func ObsoleteVersions(upgrades []Upgrade, current, rollback string, keep int) map[string]bool {
	obsolete := map[string]bool{}
	retained := map[string]bool{current: true, rollback: true}
	recent := map[string]bool{}
	for _, u := range upgrades {
		if u.State != "SUCCEEDED" && u.State != "ROLLED_BACK" {
			retained[u.Version] = true
			retained[u.CurrentVersion] = true
		}
	}
	for _, u := range upgrades {
		if u.State != "SUCCEEDED" && u.State != "ROLLED_BACK" {
			continue
		}
		if len(recent) < keep {
			recent[u.Version] = true
		}
		if recent[u.Version] {
			retained[u.Version] = true
		}
	}
	for _, u := range upgrades {
		if !retained[u.Version] {
			obsolete[u.Version] = true
		}
	}
	return obsolete
}
