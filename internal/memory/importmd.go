package memory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mcpshka/internal/runoutcome"
)

// ImportReport is the coverage of a markdown memory import: every file and
// section is written, then recalled by its own summary; Recall3 is the share
// found in the top three (acceptance R@3 ≥ 0.9). Nothing is deleted here:
// the source goes away only after the owner reads this report.
type ImportReport struct {
	runoutcome.Result
	Files     int      `json:"files"`
	Records   int      `json:"records"`
	Changed   int      `json:"changed"`
	Recall3   float64  `json:"recall_at_3"`
	Archived  int      `json:"archived,omitempty"` // older log sections kept as kind archive, not probed
	Closed    int      `json:"closed,omitempty"`   // records of these files no longer in the source
	Misses    []string `json:"misses,omitempty"`
	Skipped   []string `json:"skipped,omitempty"`
	SourceDir string   `json:"source_dir"`
}

// Claude Code memory types → record kinds.
var mdKind = map[string]string{"user": "fact", "feedback": "decision", "project": "fact", "reference": "note"}

type mdPart struct {
	title, body, probe, source string
	section                    int
}

// ImportMarkdown imports a Claude Code style memory directory: one fact per
// file with name/description/type frontmatter; long logs are cut at "## "
// sections and then at paragraphs so no text is lost.
func (s *Store) ImportMarkdown(ctx context.Context, dir, project string) (rep ImportReport, runErr error) {
	rep.SourceDir = dir
	blocker, next := "", ""
	defer func() {
		if blocker == "" {
			switch {
			case runErr != nil || len(rep.Skipped) > 0:
				blocker, next = "memory_import_read_or_write_failed", "inspect skipped files and memory storage before retrying"
			case rep.Files > 0 && rep.Records == 0:
				blocker, next = "memory_import_has_no_usable_sections", "inspect the source Markdown sections"
			}
		}
		rep.Result = runoutcome.Resolve(rep.Changed+rep.Closed, rep.Files > 0 && rep.Records == 0, blocker, next)
	}()
	// An unreadable directory is an error, never "0 files" (MCP-168: the
	// service user could not read /root/.claude and reported success).
	if _, err := os.ReadDir(dir); err != nil {
		blocker, next = "memory_import_source_unavailable", "restore directory access before retrying memory-import"
		return rep, fmt.Errorf("memory import: %w", err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		return rep, err
	}
	sort.Strings(files)
	type probe struct{ id, text, label string }
	probes := []probe{}
	for _, f := range files {
		if filepath.Base(f) == "MEMORY.md" {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			rep.Skipped = append(rep.Skipped, filepath.Base(f)+": "+err.Error())
			continue
		}
		meta, body := frontmatter(string(data))
		name := strings.TrimSuffix(filepath.Base(f), ".md")
		if meta["name"] != "" {
			name = meta["name"]
		}
		// S5.TIDY.2: a checkpoint archive file is history the owner keeps
		// on disk; it stays out of the index, and what an older import put
		// there is closed.
		if strings.Contains(name, "checkpoint-archive") {
			n, err := s.closeOthers(ctx, "claude-memory:"+name, nil)
			if err != nil {
				return rep, err
			}
			rep.Closed += n
			continue
		}
		kind := mdKind[meta["type"]]
		if kind == "" {
			kind = "note"
		}
		rep.Files++
		parts := splitMarkdown(name, meta["description"], body)
		last := 0
		for _, p := range parts {
			last = max(last, p.section)
		}
		kept := []string{}
		isLog := strings.Contains(name, "checkpoint") || strings.Contains(name, "log")
		for _, p := range parts {
			k := kind
			// A log (checkpoint, journal) keeps only its newest section
			// current; older sections are history (0.11.3 item 3).
			if isLog && p.section < last {
				k = "archive"
			}
			rec, changed, err := s.Write(ctx, Record{Kind: k, Project: project, Title: p.title, Body: p.body, Source: "claude-memory:" + name + p.source, Tags: []string{name}})
			if err != nil {
				rep.Skipped = append(rep.Skipped, p.title+": "+err.Error())
				continue
			}
			rep.Records++
			kept = append(kept, lit(rec.ID))
			if changed {
				rep.Changed++
			}
			if k == "archive" {
				rep.Archived++
				continue
			}
			probes = append(probes, probe{rec.ID, p.probe, p.title})
		}
		n, err := s.closeOthers(ctx, "claude-memory:"+name, kept)
		if err != nil {
			return rep, err
		}
		rep.Closed += n
	}
	found := 0
	for _, p := range probes {
		hits, err := s.Recall(ctx, Query{Text: p.text, Limit: 3})
		ok := false
		for _, h := range hits {
			ok = ok || err == nil && h.ID == p.id
		}
		if ok {
			found++
		} else {
			rep.Misses = append(rep.Misses, p.label)
		}
	}
	if len(probes) > 0 {
		rep.Recall3 = float64(found) / float64(len(probes))
	}
	return rep, nil
}

// closeOthers ends the current own records of one source file that this
// import did not write again (renamed or re-cut sections), so stale chunks
// stop answering; their history stays.
func (s *Store) closeOthers(ctx context.Context, source string, kept []string) (int, error) {
	now := s.now().UTC()
	cond := "origin='own' AND valid_to IS NULL AND (source=" + lit(source) + " OR substr(source,1," + fmt.Sprint(len(source)+1) + ")=" + lit(source+"#") + ")"
	if len(kept) > 0 {
		cond += " AND id NOT IN (" + strings.Join(kept, ",") + ")"
	}
	rows, err := s.exec(ctx, "UPDATE mem SET valid_to="+litTime(&now)+" WHERE "+cond+";\nSELECT changes() AS n;")
	if err != nil || len(rows) != 1 {
		return 0, err
	}
	n, _ := rows[0]["n"].(float64)
	return int(n), nil
}

// frontmatter reads the simple YAML header (top-level and metadata.* keys).
func frontmatter(text string) (map[string]string, string) {
	meta := map[string]string{}
	if !strings.HasPrefix(text, "---\n") {
		return meta, text
	}
	end := strings.Index(text[4:], "\n---")
	if end < 0 {
		return meta, text
	}
	for _, line := range strings.Split(text[4:4+end], "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), ":")
		if ok && v != "" {
			meta[k] = strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return meta, strings.TrimLeft(text[4+end+4:], "\n")
}

// splitMarkdown keeps a short file whole and cuts a long one into sections
// and then paragraph chunks of at most maxBody bytes.
func splitMarkdown(name, desc, body string) []mdPart {
	title := strings.ReplaceAll(name, "-", " ")
	probe := desc
	if probe == "" {
		probe = title
	}
	// The description is the file's own summary: keep it searchable.
	if desc != "" && len(desc)+len(body)+2 <= maxBody {
		body = strings.TrimSpace(desc + "\n\n" + body)
	}
	if len(body) <= maxBody {
		return []mdPart{{title: title, body: body, probe: probe}}
	}
	out, seen := []mdPart{}, map[string]int{}
	sections := strings.Split("\n"+body, "\n## ")
	for i, sec := range sections {
		head, text, _ := strings.Cut(sec, "\n")
		head = strings.TrimSpace(head)
		if i == 0 {
			head, text = desc, sec
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		if seen[head]++; seen[head] > 1 {
			head = fmt.Sprintf("%s [%d]", head, seen[head])
		}
		chunks := chunk(text, maxBody)
		for j, c := range chunks {
			t := fmt.Sprintf("%s: %s", title, head)
			if len(chunks) > 1 {
				t += fmt.Sprintf(" (часть %d)", j+1)
			}
			if len(t) > maxTitle {
				t = truncate(t, maxTitle)
			}
			out = append(out, mdPart{title: t, body: c, probe: firstLine(c), source: fmt.Sprintf("#%d.%d", i, j+1), section: i})
		}
	}
	return out
}

func chunk(text string, limit int) []string {
	out, cur := []string{}, ""
	for _, para := range strings.Split(text, "\n\n") {
		for len(para) > limit {
			cut := truncate(para, limit)
			out, para = append(out, cut), para[len(cut):]
		}
		if len(cur)+len(para)+2 > limit && cur != "" {
			out, cur = append(out, cur), ""
		}
		if cur != "" {
			cur += "\n\n"
		}
		cur += para
	}
	if strings.TrimSpace(cur) != "" {
		out = append(out, cur)
	}
	return out
}

// truncate cuts at a rune boundary.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimLeft(s, "-*# \n"), "\n")
	return truncate(line, 200)
}
