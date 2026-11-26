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

var ErrPinMismatch = errors.New("roadmap digest changed: HOLD for reconciliation")

type Phase struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Superseded   int    `json:"superseded,omitempty"`
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
	primary     bool
}

// Headings lists recognized roadmap sections, skipping fenced examples.
func Headings(content string) []Heading {
	lines, _ := ContentLines(content)
	return headings(lines)
}

func headings(lines []string) []Heading {
	var out []Heading
	for i, line := range lines {
		if m, primary := sectionHeading(line); m != nil {
			out = append(out, Heading{ID: m[1], Title: strings.TrimSpace(m[2]), Line: i, Level: HeadingLevel(line), primary: primary})
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
	if len(content) > MaxBytes {
		return Snapshot{}, errors.New("roadmap exceeds size limit")
	}
	if !utf8.ValidString(content) || !utf8.ValidString(sha) {
		return Snapshot{}, errors.New("roadmap evidence must be valid UTF-8")
	}
	lines, inFence := ContentLines(content)
	if inFence {
		return Snapshot{}, errors.New("unterminated fenced code")
	}
	heads := headings(lines)
	if len(heads) == 0 {
		return Snapshot{}, errors.New("no roadmap phases")
	}
	if len(heads) > MaxPhases {
		return Snapshot{}, errors.New("too many roadmap phases")
	}
	result := Snapshot{SHA256: sha, Phases: make([]Phase, 0, len(heads))}
	seen := make(map[string]bool)
	checks := 0
	for _, h := range heads {
		if seen[h.ID] {
			return Snapshot{}, fmt.Errorf("duplicate roadmap phase %s", h.ID)
		}
		seen[h.ID] = true
		if len(h.Title) > 320 {
			return Snapshot{}, errors.New("phase title too long")
		}
		phase := Phase{ID: h.ID, Title: h.Title, DeclaredDone: strings.Contains(h.Title, " — DONE") || strings.HasSuffix(h.Title, " DONE")}
		for _, line := range lines[h.Line+1:] {
			if level := HeadingLevel(line); level > 0 && level <= h.Level {
				break
			}
			if strings.Contains(line, "DEFERRED") {
				phase.Deferred = true
			}
			if m := Checkbox.FindStringSubmatch(line); m != nil && strings.TrimSpace(m[4]) != "" {
				checks++
				if checks > MaxChecks {
					return Snapshot{}, errors.New("too many roadmap checkboxes")
				}
				phase.Total++
				if m[2] == "x" || m[2] == "X" {
					phase.Completed++
				} else if Superseded(m[2], m[4]) != "" {
					phase.Superseded++
				}
			}
		}
		result.Phases = append(result.Phases, phase)
	}
	// Keep existing P-phase priority. Once those are finished, select the
	// first stream with unfinished work, then sections without a checklist.
	for rank := range 3 {
		for i, phase := range result.Phases {
			primary := heads[i].primary
			if (rank == 0 && !primary) || (rank == 1 && (primary || phase.Total == 0)) || (rank == 2 && (primary || phase.Total != 0)) {
				continue
			}
			if !phase.DeclaredDone && !phase.Deferred && (phase.Total == 0 || phase.Completed+phase.Superseded < phase.Total) {
				result.Next = &result.Phases[i]
				return result, nil
			}
		}
	}
	return result, nil
}
