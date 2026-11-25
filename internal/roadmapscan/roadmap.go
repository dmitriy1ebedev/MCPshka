// Package roadmapscan provides a bounded, read-only roadmap inventory.
// A parsed ROADMAP is UNTRUSTED evidence, never an authorization to mutate.
package roadmapscan

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"syscall"
	"unicode/utf8"
)

// MaxBytes bounds one ROADMAP read. 256 KiB was reached by a living
// roadmap with its release/bug journal (MCP-011: 98% on 08.11, workflow
// degraded); 512 KiB keeps the read bounded with room for growth.
const MaxBytes = 512 * 1024
const MaxPhases = 100
const MaxChecks = 1000

// "## P0 — Title" (MCPshka, DIAG) and "## P0: Title" (Lane). roadmap_next
// already read both; the sealed path rejected the colon form as "no roadmap
// phases", so every Lane roadmap_run stopped UNKNOWN at its test gate.
var heading = regexp.MustCompile(`^## (P[0-9]{1,2})(?: [—-]|:) (.+)$`)

// Without a single "## P" phase the roadmap names its phases its own way:
// Lane 1.3.0 writes "### Приоритет L0 — Title". Any short letter+digit ID at
// level 2–3 counts then, except P: a "### P0" is a mistyped P-phase and stays
// rejected. P-roadmaps never reach this, so their Next is unchanged.
var anyHeading = regexp.MustCompile(`^#{2,3} (?:Приоритет |Фаза |Phase )?([A-OQ-Z][A-Z]{0,2}[0-9]{1,2}[a-z]?|P[A-Z]{1,2}[0-9]{1,2}[a-z]?)(?: [—-]|:) (.+)$`)
var checkbox = regexp.MustCompile(`^- \[([xX ])\] \S`)

var ErrPinMismatch = errors.New("roadmap digest changed: HOLD for reconciliation")

type Phase struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Completed    int    `json:"completed"`
	Total        int    `json:"total"`
	DeclaredDone bool   `json:"declared_done"`
	Deferred     bool   `json:"deferred"`
}
type Snapshot struct {
	SHA256 string  `json:"sha256"`
	Phases []Phase `json:"phases"`
	Next   *Phase  `json:"next,omitempty"`
}

// Headroom reports how close the roadmap is to MaxBytes. Past WarnPercent
// the next release notes may push it over and integration_enqueue refuses
// every merge (BUG-20261024-100), so closed journal rows must move to the
// archive before that.
const WarnPercent = 90

type Room struct {
	Bytes   int64  `json:"bytes"`
	Limit   int64  `json:"limit_bytes"`
	Percent int64  `json:"percent"`
	Warn    bool   `json:"warn"`
	Hint    string `json:"hint,omitempty"`
}

func Headroom(path string) (Room, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return Room{}, err
	}
	r := Room{Bytes: info.Size(), Limit: MaxBytes, Percent: info.Size() * 100 / MaxBytes}
	if r.Percent >= WarnPercent {
		r.Warn = true
		r.Hint = "move closed BUG rows and old releases to roadmap/JOURNAL_ARCHIVE.md before the next release"
	}
	return r, nil
}

// Heading is a phase heading as Parse sees it; Line is 0-based.
type Heading struct {
	ID, Title   string
	Line, Level int
}

// Headings lists the phase headings in the style Parse settles on ("## P0"
// phases, else any short letter+digit id), fenced code skipped.
func Headings(content string) []Heading {
	for _, re := range []*regexp.Regexp{heading, anyHeading} {
		var out []Heading
		inFence := false
		for i, raw := range strings.Split(content, "\n") {
			line := strings.TrimSuffix(raw, "\r")
			if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
				inFence = !inFence
			} else if m := re.FindStringSubmatch(line); m != nil && !inFence {
				out = append(out, Heading{ID: m[1], Title: strings.TrimSpace(m[2]), Line: i, Level: len(line) - len(strings.TrimLeft(line, "#"))})
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return nil
}

// ItemHeadings is Headings plus, in a "## P" roadmap, the cross-cutting
// stream sections ("## S5 — …", "## C2 — …"): their checkboxes are items
// that roadmap_close must find (MCP-278), but they never count as phases.
func ItemHeadings(content string) []Heading {
	var out []Heading
	inFence := false
	for i, raw := range strings.Split(content, "\n") {
		line := strings.TrimSuffix(raw, "\r")
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		m := heading.FindStringSubmatch(line)
		if m == nil {
			m = anyHeading.FindStringSubmatch(line)
		}
		if m != nil {
			out = append(out, Heading{ID: m[1], Title: strings.TrimSpace(m[2]), Line: i, Level: len(line) - len(strings.TrimLeft(line, "#"))})
		}
	}
	return out
}

// Load does not modify files or derive permissions from their contents.
// expectedSHA256 is required for a previously approved/pinned plan.
// The returned Next is advisory only: DONE still requires acceptance evidence.
func Load(path, expectedSHA256 string) (Snapshot, error) {
	snap, _, err := load(path, expectedSHA256)
	return snap, err
}

// load also returns the text it parsed, for Analyze.
func load(path, expectedSHA256 string) (Snapshot, string, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return Snapshot{}, "", fmt.Errorf("open roadmap: %w", err)
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return Snapshot{}, "", errors.New("roadmap must be a regular file")
	}
	if info.Size() > MaxBytes {
		return Snapshot{}, "", errors.New("roadmap exceeds size limit")
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxBytes+1))
	if err != nil {
		return Snapshot{}, "", err
	}
	if len(b) > MaxBytes {
		return Snapshot{}, "", errors.New("roadmap exceeds size limit")
	}
	digest := sha256.Sum256(b)
	sha := hex.EncodeToString(digest[:])
	if expectedSHA256 != "" && expectedSHA256 != sha {
		return Snapshot{}, "", ErrPinMismatch
	}
	snap, err := Parse(string(b), sha)
	return snap, string(b), err
}

// Parse keeps interpretation deliberately narrow. Unknown or ambiguous data
// fail closed rather than selecting or marking roadmap phases automatically.
func Parse(content, sha string) (Snapshot, error) {
	snap, err := parseWith(content, sha, heading)
	if err != nil && err.Error() == "no roadmap phases" {
		return parseWith(content, sha, anyHeading)
	}
	return snap, err
}

func parseWith(content, sha string, heading *regexp.Regexp) (Snapshot, error) {
	if len(content) > MaxBytes {
		return Snapshot{}, errors.New("roadmap exceeds size limit")
	}
	if !utf8.ValidString(content) || !utf8.ValidString(sha) {
		return Snapshot{}, errors.New("roadmap evidence must be valid UTF-8")
	}
	result := Snapshot{SHA256: sha, Phases: make([]Phase, 0, 24)}
	seen := make(map[string]bool)
	inFence := false
	checks := 0
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSuffix(raw, "\r")
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if m := heading.FindStringSubmatch(line); m != nil {
			if seen[m[1]] {
				return Snapshot{}, fmt.Errorf("duplicate roadmap phase %s", m[1])
			}
			if len(result.Phases) >= MaxPhases {
				return Snapshot{}, errors.New("too many roadmap phases")
			}
			seen[m[1]] = true
			title := strings.TrimSpace(m[2])
			if len(title) > 320 {
				return Snapshot{}, errors.New("phase title too long")
			}
			declared := strings.Contains(title, " — DONE") || strings.HasSuffix(title, " DONE")
			result.Phases = append(result.Phases, Phase{ID: m[1], Title: title, DeclaredDone: declared})
			continue
		}
		if len(result.Phases) > 0 && strings.Contains(line, "DEFERRED") {
			result.Phases[len(result.Phases)-1].Deferred = true
		}
		if len(result.Phases) > 0 && checkbox.MatchString(line) {
			checks++
			if checks > MaxChecks {
				return Snapshot{}, errors.New("too many roadmap checkboxes")
			}
			n := len(result.Phases) - 1
			result.Phases[n].Total++
			if line[3] == 'x' || line[3] == 'X' {
				result.Phases[n].Completed++
			}
		}
	}
	if inFence {
		return Snapshot{}, errors.New("unterminated fenced code")
	}
	if len(result.Phases) == 0 {
		return Snapshot{}, errors.New("no roadmap phases")
	}
	for _, phase := range result.Phases {
		if !phase.DeclaredDone && !phase.Deferred && (phase.Total == 0 || phase.Completed < phase.Total) {
			p := phase
			result.Next = &p
			break
		}
	}
	return result, nil
}
