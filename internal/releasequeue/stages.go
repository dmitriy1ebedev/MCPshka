package releasequeue

// Stages of one release, never collapsed into one "done" (ROADMAP S1).
type Stages struct {
	ImplementationComplete bool   `json:"implementation_complete" jsonschema:"The commit is queued for release (code done on its branch)."`
	Integrated             bool   `json:"integrated" jsonschema:"The commit is on protected main (past SHIP)."`
	ProductionVerified     bool   `json:"production_verified" jsonschema:"Installed and project_verify passed (queue DONE)."`
	Accepted               bool   `json:"accepted" jsonschema:"Always false here: acceptance is per ROADMAP step (acceptance_status) or issue (issue_accept)."`
	Rollback               string `json:"rollback_version,omitempty"`
}

// StagesOf reads the four stages from the queue and the live version.
func StagesOf(q *View, installed, version string) Stages {
	st := Stages{}
	if q == nil {
		return st
	}
	st.Rollback = q.PreviousVersion
	st.ImplementationComplete = q.Stage != "CANCELLED"
	switch q.Stage {
	case "BUILD", "STAGE", "APPLY", "VERIFY", "DONE":
		st.Integrated = true
	}
	st.ProductionVerified = q.Stage == "DONE" && installed == version
	return st
}
