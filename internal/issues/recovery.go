package issues

// Recovery is a closed look-alike's fix: what worked last time (ROADMAP
// P19 level 2: similar incident retrieval, preferred recovery path).
type Recovery struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Commit  string `json:"commit,omitempty"`
	Release string `json:"release,omitempty"`
	Lesson  string `json:"lesson,omitempty"`
	Note    string `json:"note,omitempty"` // acceptance note or the last status note
}

// Recoveries lists up to three accepted cards similar to i, best match first.
func (s *Store) Recoveries(i Issue) []Recovery {
	var out []Recovery
	for _, c := range s.Related(i) {
		if c.Status != Closed || len(out) == 3 {
			continue
		}
		full, err := s.Get(c.ID)
		if err != nil {
			continue
		}
		r := Recovery{ID: full.ID, Title: full.Title, Commit: full.Commit, Release: full.Release, Lesson: full.Lesson, Note: full.StatusNote}
		if full.Acceptance != nil && full.Acceptance.Note != "" {
			r.Note = full.Acceptance.Note
		}
		out = append(out, r)
	}
	return out
}
