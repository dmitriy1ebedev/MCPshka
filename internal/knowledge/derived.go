package knowledge

func cloneDependencies(in map[string]uint64) map[string]uint64 {
	if in == nil {
		return nil
	}
	out := make(map[string]uint64, len(in))
	for id, rev := range in {
		out[id] = rev
	}
	return out
}

// Invalidation is part of the same CAS as revocation/supersession.
// Archived evidence stays inspectable offline but cannot enter projections.
func invalidateDerived(records map[string]Record) {
	reverse := map[string][]string{}
	queue := []string{}
	for id, r := range records {
		for source, revision := range r.DerivedFrom {
			reverse[source] = append(reverse[source], id)
			parent, ok := records[source]
			if !ok || parent.State != "" || parent.SupersededBy != "" || parent.Revision != revision {
				queue = append(queue, id)
			}
		}
	}
	seen := map[string]bool{}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if seen[id] {
			continue
		}
		seen[id] = true
		r := records[id]
		if r.State == "" {
			before := r
			r.State = "archived"
			records[id] = withRevision(r, before, "invalidate", "source revision no longer active")
		}
		queue = append(queue, reverse[id]...)
	}
}
