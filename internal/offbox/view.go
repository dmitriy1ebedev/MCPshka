package offbox

import (
	"strings"

	"mcpshka/internal/heavyqueue"
	"mcpshka/internal/jobs"
	"mcpshka/internal/runnerhub"
)

// LocalJobs is the H96 job manager a given-up test falls back to.
type LocalJobs interface {
	Get(id string) (jobs.Snapshot, error)
	Read(id, stream string, cursor int64, limit int) (jobs.Output, error)
}

// View shows a runner job as an ordinary MCPshka job (jobs.Foreign): one id
// for job_status/job_wait/job_output wherever the test ran; after a fallback
// it is the H96 job's state and output.
type View struct {
	Hub   *runnerhub.Hub
	Local LocalJobs
}

func (v View) Get(id string) (jobs.Snapshot, error) {
	j, err := v.Hub.Peek(id)
	if err != nil {
		return jobs.Snapshot{}, jobs.ErrNotFound
	}
	if j.Fallback != "" && v.Local != nil {
		return v.Local.Get(j.Fallback)
	}
	return snapshot(j), nil
}

func (v View) Read(id, stream string, cursor int64, limit int) (jobs.Output, error) {
	j, err := v.Hub.Peek(id)
	if err != nil {
		return jobs.Output{}, jobs.ErrNotFound
	}
	if j.Fallback != "" && v.Local != nil {
		return v.Local.Read(j.Fallback, stream, cursor, limit)
	}
	text := j.Output
	if stream == "stderr" {
		text = j.Error
	}
	return jobs.ReadText(id, stream, text, cursor, limit, snapshot(j).State), nil
}

// Cancel implements jobs.ForeignCanceller: job_cancel on an rj- id stops the
// runner job and, after a fallback, the H96 job that took it over.
func (v View) Cancel(id string) (jobs.Snapshot, error) {
	j, err := v.Hub.Cancel(id)
	if err != nil {
		return jobs.Snapshot{}, jobs.ErrNotFound
	}
	if j.Fallback != "" && v.Local != nil {
		if c, ok := v.Local.(interface {
			Cancel(string) (jobs.Snapshot, error)
		}); ok {
			return c.Cancel(j.Fallback)
		}
		return v.Local.Get(j.Fallback)
	}
	return snapshot(j), nil
}

func snapshot(j runnerhub.Job) jobs.Snapshot {
	s := jobs.Snapshot{ID: j.ID, Command: "go", Args: j.Argv, Workdir: j.Workdir, StartedAt: j.Created, FinishedAt: j.Finished, TimeoutSeconds: int64(j.Timeout), Error: j.Error}
	if s.Workdir == "" {
		s.Workdir = j.Project
	}
	var woke []string
	for _, w := range j.Wakes {
		if w.Err != "" {
			woke = append(woke, w.Site+" (refused)")
		} else {
			woke = append(woke, w.Site)
		}
	}
	q := &heavyqueue.Status{Label: "offbox:" + j.ID, QueuedAt: j.Created}
	switch j.State {
	case runnerhub.Queued:
		s.State, q.State, q.Reason = jobs.Queued, "QUEUED", "waiting for an off-box runner"
		if len(woke) > 0 {
			q.Reason += "; woke " + strings.Join(woke, ", ")
		}
	case runnerhub.Leased:
		s.State, q.State, q.Reason = jobs.Running, "RUNNING", "running off-box on "+orDefault(j.Site, "runner "+j.Runner)
	case runnerhub.Done, runnerhub.Failed:
		s.State = jobs.Done
		if j.State == runnerhub.Failed || j.ExitCode != 0 {
			s.State = jobs.Failed
		}
		if j.State == runnerhub.Failed && strings.HasPrefix(j.Error, runnerhub.CancelledPrefix) {
			s.State = jobs.Cancelled
		}
		code := j.ExitCode
		s.ExitCode = &code
		q.State, q.Reason = "DONE", "ran off-box on "+orDefault(j.Site, "runner "+j.Runner)
	default: // NO_RUNNER: H96 fallback is starting or failed to start
		s.State, q.State, q.Reason = jobs.Queued, "QUEUED", "no off-box runner in time; starting on H96"
		if strings.Contains(j.Error, "fallback failed") {
			s.State = jobs.Failed
		}
	}
	s.Queue = q
	return s
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
