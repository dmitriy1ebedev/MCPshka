package memory

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The memory exam (0.11.3 item 1) ships inside the binary so every install
// can grade its own memory: phase_gate blocks a release below the targets,
// `mcpshka memory-eval` prints the misses.
//
//go:embed exam.json
var examJSON []byte

// Exam targets: the right record first in 85% of questions, in the top three
// in 95%.
const (
	ExamR1 = 0.85
	ExamR3 = 0.95
)

type Exam struct {
	Version   int            `json:"version"`
	Questions []ExamQuestion `json:"questions"`
}

type ExamQuestion struct {
	Q      string   `json:"q"`
	Expect []string `json:"expect"`
}

// ExamMiss is one question whose answer was not first.
type ExamMiss struct {
	Q    string `json:"q"`
	Rank int    `json:"rank"` // 0: not in the top three
	Top  string `json:"top"`  // title of the record that came first
}

type ExamReport struct {
	Questions int        `json:"questions"`
	R1        float64    `json:"recall_at_1"`
	R3        float64    `json:"recall_at_3"`
	Pass      bool       `json:"pass"`
	Misses    []ExamMiss `json:"misses,omitempty"`
}

// BuiltinExam is the exam shipped with this build.
func BuiltinExam() (Exam, error) {
	var e Exam
	err := json.Unmarshal(examJSON, &e)
	if err == nil && len(e.Questions) == 0 {
		err = fmt.Errorf("memory exam: no questions")
	}
	return e, err
}

// answers reports whether a hit is one of the expected records: a source
// (with its #sections), a knowledge title or id.
func answers(h Hit, expect []string) bool {
	for _, e := range expect {
		if h.Source == e || strings.HasPrefix(h.Source, e+"#") || h.Title == e || h.ID == "knowledge:"+e {
			return true
		}
	}
	return false
}

// Eval grades recall on the exam.
func (s *Store) Eval(ctx context.Context, e Exam) (ExamReport, error) {
	rep := ExamReport{Questions: len(e.Questions)}
	one, three := 0, 0
	for _, q := range e.Questions {
		hits, err := s.Recall(ctx, Query{Text: q.Q, Limit: 3})
		if err != nil {
			return rep, err
		}
		rank := 0
		for i, h := range hits {
			if answers(h, q.Expect) {
				rank = i + 1
				break
			}
		}
		if rank == 1 {
			one++
		}
		if rank > 0 {
			three++
		}
		if rank != 1 {
			top := ""
			if len(hits) > 0 {
				top = hits[0].Title
			}
			rep.Misses = append(rep.Misses, ExamMiss{Q: q.Q, Rank: rank, Top: clean(top, 80)})
		}
	}
	if rep.Questions > 0 {
		rep.R1 = float64(one) / float64(rep.Questions)
		rep.R3 = float64(three) / float64(rep.Questions)
	}
	rep.Pass = rep.R1 >= ExamR1 && rep.R3 >= ExamR3
	return rep, nil
}

// Gate is the phase_gate check of an MCPshka tree at root: it grades the
// memory of dir on the built-in exam (a release does not go out while recall
// is below the targets); ok is false for other trees or without a memory.
func Gate(dir, root string) (pass bool, detail string, ok bool) {
	if _, err := os.Stat(filepath.Join(root, "internal", "memory", "exam.json")); err != nil {
		return false, "", false
	}
	st, err := OpenExisting(dir)
	if err != nil {
		return false, "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	exam, err := BuiltinExam()
	if err != nil {
		return false, err.Error(), true
	}
	rep, err := st.Eval(ctx, exam)
	if err != nil {
		return false, err.Error(), true
	}
	detail = fmt.Sprintf("R@1 %.2f (≥%.2f), R@3 %.2f (≥%.2f) on %d questions", rep.R1, ExamR1, rep.R3, ExamR3, rep.Questions)
	if !rep.Pass && len(rep.Misses) > 0 {
		detail += fmt.Sprintf("; first miss «%s» → %s; see mcpshka memory-eval", rep.Misses[0].Q, rep.Misses[0].Top)
	}
	return rep.Pass, detail, true
}
