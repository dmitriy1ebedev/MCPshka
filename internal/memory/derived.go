package memory

// Summaries are bounded disposable projections; source changes invalidate all.
// This deliberately avoids a second authority for per-summary dependency edges.
func invalidateSummariesSQL() string {
	where := "source='memory consolidation'"
	return "DELETE FROM mem_word WHERE rowid IN (SELECT rid FROM mem WHERE " + where + ");\n" +
		"DELETE FROM mem_tri WHERE rowid IN (SELECT rid FROM mem WHERE " + where + ");\n" +
		"DELETE FROM mem WHERE " + where + ";\n"
}
