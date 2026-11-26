package memory

// K2.2: possible contradictions — current facts/decisions on the same topic
// (near-equal titles) whose bodies differ. Read-only: listed in the audit
// for the owner, never in the optimize plan.

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxConflicts = 50

// Conflict is a pair that may contradict: A is the newer record.
type Conflict struct {
	A     string `json:"a"`
	B     string `json:"b"`
	Title string `json:"title"`
}

func normalizeTitle(title string) map[string]struct{} {
	title = strings.ToLower(title)
	var words []string
	for _, word := range strings.FieldsFunc(title, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if utf8.RuneCountInString(word) >= 3 {
			words = append(words, word)
		}
	}
	set := make(map[string]struct{}, len(words))
	for _, w := range words {
		set[w] = struct{}{}
	}
	return set
}

func jaccard(a, b map[string]struct{}) float64 {
	if len(a)+len(b) == 0 {
		return 0
	}
	var intersection int
	for k := range a {
		if _, ok := b[k]; ok {
			intersection++
		}
	}
	return float64(intersection) / float64(len(a)+len(b)-intersection)
}

func normalizeBody(body string) string {
	body = strings.ToLower(body)
	body = spaces.ReplaceAllString(body, " ")
	return strings.TrimSpace(body)
}

func conflicts(recs []auditRec) []Conflict {
	type topic struct {
		rec   *auditRec
		words map[string]struct{}
		body  string
	}
	var topics []topic
	for i := range recs {
		r := &recs[i]
		if r.kind != "fact" && r.kind != "decision" {
			continue
		}
		if w := normalizeTitle(r.title); len(w) >= 3 {
			topics = append(topics, topic{r, w, normalizeBody(r.body)})
		}
	}
	var result []Conflict
	for i, a := range topics {
		for _, b := range topics[i+1:] {
			if a.body == b.body || jaccard(a.words, b.words) < 0.75 {
				continue
			}
			newer, older := a.rec, b.rec
			if older.from.After(newer.from) {
				newer, older = older, newer
			}
			result = append(result, Conflict{A: newer.id, B: older.id, Title: newer.title})
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].A == result[j].A {
			return result[i].B < result[j].B
		}
		return result[i].A < result[j].A
	})
	return result[:min(len(result), maxConflicts)]
}
