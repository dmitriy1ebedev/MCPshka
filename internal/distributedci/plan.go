package distributedci

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

const Schema = "mcpshka.distributed-ci/v1"

var ErrDispatchUnknown = errors.New("CI dispatch outcome unknown; reconcile receipt")
var ErrEvidence = errors.New("untrusted distributed evidence rejected")

var Sites = []string{"github", "gitverse", "moshub", "sourcecraft"}

// ShardSuites names the release evidence suites: one shard per site.
// ValidShardIndex accepts one canonical decimal index per distributed site;
// H96 may run any shard as the fallback.
func ValidShardIndex(s string) bool {
	n, err := strconv.Atoi(s)
	return err == nil && strconv.Itoa(n) == s && n >= 0 && n < len(Sites)
}

// PassedSuites is the receipt's suite map for a passed distributed run.
func PassedSuites() map[string]string {
	m := map[string]string{}
	for _, s := range ShardSuites() {
		m[s] = "success"
	}
	return m
}

func ShardSuites() []string {
	s := make([]string, len(Sites))
	for i := range Sites {
		s[i] = "shard-" + strconv.Itoa(i)
	}
	return s
}

var shaRE = regexp.MustCompile("^[a-f0-9]{40}$")
var pkgRE = regexp.MustCompile("^[A-Za-z0-9][A-Za-z0-9_.-]*(?:/[A-Za-z0-9_.-]+)*$")

type Plan struct {
	Commit   string     `json:"commit"`
	Tree     string     `json:"source_tree"`
	Snapshot string     `json:"snapshot"`
	Packages []string   `json:"packages"`
	Shards   [][]string `json:"shards"`
}
type Observation struct {
	Artifacts      []Artifact `json:"artifacts,omitempty"`
	Provider       string     `json:"provider"`
	Shard          int        `json:"shard"`
	Commit         string     `json:"commit"`
	Tree           string     `json:"source_tree"`
	Snapshot       string     `json:"snapshot"`
	PlanHash       string     `json:"plan_hash"`
	Packages       []string   `json:"packages"`
	State          string     `json:"state"`
	FailedPackages []string   `json:"failed_packages,omitempty"`
	Seconds        float64    `json:"seconds"`
	RunID          int64      `json:"run_id"`
}
type Availability struct {
	Provider string `json:"provider"`
	Code     string `json:"code"`
	// Detail is the clipped probe error, so an unavailable site says why
	// (24.11: all three sites reported CI_UNAVAILABLE with no cause).
	Detail string `json:"detail,omitempty"`
}

type Report struct {
	Unavailable    []Availability `json:"unavailable,omitempty"`
	Code           string         `json:"code,omitempty"`
	NextSafeAction string         `json:"next_safe_action,omitempty"`
	Schema         string         `json:"schema"`
	Plan           Plan           `json:"plan"`
	State          string         `json:"state"`
	Shards         []Observation  `json:"shards"`
	Attempts       []Observation  `json:"attempts"`
	Trust          string         `json:"trust"`
}

func NewPlan(commit, tree, snapshot string, packages []string, weights map[string]float64, counts map[string]int, k int) (Plan, error) {
	p := Plan{Commit: commit, Tree: tree, Snapshot: snapshot, Packages: slices.Clone(packages)}
	if !shaRE.MatchString(commit) || !shaRE.MatchString(tree) || !shaRE.MatchString(snapshot) || k < 3 || k > 4 || len(packages) < k {
		return p, errors.New("invalid shard plan")
	}
	sort.Strings(p.Packages)
	for i, s := range p.Packages {
		if !pkgRE.MatchString(s) || strings.Contains("/"+s+"/", "/../") || strings.Contains("/"+s+"/", "/./") || (i > 0 && s == p.Packages[i-1]) {
			return p, errors.New("invalid package inventory")
		}
	}
	weight := func(s string) float64 {
		if w := weights[s]; w > 0 && !math.IsInf(w, 0) && !math.IsNaN(w) {
			return w
		}
		return float64(max(1, counts[s]))
	}
	order := slices.Clone(p.Packages)
	sort.Slice(order, func(i, j int) bool {
		a, b := weight(order[i]), weight(order[j])
		return a > b || a == b && order[i] < order[j]
	})
	loads := make([]float64, k)
	p.Shards = make([][]string, k)
	for _, s := range order {
		n := 0
		for i := 1; i < k; i++ {
			if loads[i] < loads[n] {
				n = i
			}
		}
		p.Shards[n] = append(p.Shards[n], s)
		loads[n] += weight(s)
	}
	for i := range p.Shards {
		sort.Strings(p.Shards[i])
	}
	return p, nil
}
func (p Plan) Hash() string {
	b, _ := json.Marshal(p)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func Assignment(shard int, day time.Time) []string {
	n := int(day.UTC().Unix() / 86400)
	n = (n%len(Sites) + len(Sites)) % len(Sites)
	out := make([]string, len(Sites))
	for i := range out {
		out[i] = Sites[(shard+n+i)%len(Sites)]
	}
	return out
}
func (p Plan) Check(o Observation) error {
	seen := map[string]bool{}
	for _, a := range o.Artifacts {
		if !ValidArtifact(a) || o.Shard != 0 || o.State != "PASS" || seen[a.Path] {
			return errors.New("invalid CI artifact identity")
		}
		seen[a.Path] = true
	}

	if !slices.Contains(append(slices.Clone(Sites), "module", "runner"), o.Provider) || o.Shard < 0 || o.Shard >= len(p.Shards) || o.Commit != p.Commit || o.Tree != p.Tree || o.Snapshot != p.Snapshot || o.PlanHash != p.Hash() || !slices.Equal(o.Packages, p.Shards[o.Shard]) || o.Seconds < 0 || o.Seconds > 6*3600 || math.IsNaN(o.Seconds) || math.IsInf(o.Seconds, 0) || o.RunID <= 0 {
		return errors.New("untrusted shard identity mismatch")
	}
	if o.State != "PASS" && o.State != "CODE_FAIL" && o.State != "INFRA_FAIL" && o.State != "RUNNING" && o.State != "QUEUED" {
		return errors.New("unknown shard state")
	}
	if o.State == "PASS" && len(o.FailedPackages) > 0 {
		return errors.New("contradictory shard result")
	}
	if o.State == "CODE_FAIL" && len(o.FailedPackages) == 0 {
		return errors.New("code failure without package")
	}
	for _, s := range o.FailedPackages {
		if !slices.Contains(o.Packages, s) {
			return errors.New("failure outside shard")
		}
	}
	return nil
}
func (r Report) Verify(p Plan) error {
	if r.Schema != Schema || r.Plan.Hash() != p.Hash() || r.State != "PASS" || len(r.Shards) != len(p.Shards) {
		return errors.New("incomplete distributed evidence")
	}
	all := []string{}
	seen := map[int]bool{}
	for _, o := range append(slices.Clone(r.Attempts), r.Shards...) {
		if err := p.Check(o); err != nil {
			return err
		}
		if o.State == "CODE_FAIL" {
			return fmt.Errorf("FAILED: %s on %s: %v", o.Packages, o.Provider, o.FailedPackages)
		}
	}
	for _, o := range r.Shards {
		if o.State != "PASS" || seen[o.Shard] {
			return errors.New("missing or duplicate shard")
		}
		seen[o.Shard] = true
		all = append(all, o.Packages...)
	}
	sort.Strings(all)
	if !slices.Equal(all, p.Packages) {
		return errors.New("incomplete package coverage")
	}
	return nil
}
