package projectinsights

import (
	"net/url"
	"regexp"
	"slices"
	"strings"

	"mcpshka/internal/gitops"
)

type AuthStates struct {
	API    string `json:"api"`
	Git    string `json:"git"`
	Plugin string `json:"plugin"`
}
type Lane struct {
	Provider    string     `json:"provider"`
	State       string     `json:"state"`
	Remote      string     `json:"remote,omitempty"`
	Repository  string     `json:"repository,omitempty"`
	LocalBranch string     `json:"local_branch,omitempty"`
	TrackingRef string     `json:"tracking_ref,omitempty"`
	Revision    string     `json:"revision,omitempty"`
	CIState     string     `json:"ci_state"`
	Auth        AuthStates `json:"auth"`
}
type LaneReport struct {
	State      string `json:"state"`
	Source     string `json:"source"`
	SourceTree string `json:"source_tree,omitempty"`
	Lanes      []Lane `json:"lanes"`
}

var repositoryPath = regexp.MustCompile("^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$")

// ProviderLanes separates configured remotes from independently verified auth.
// Git tree identity survives parentless snapshots; it is not a CI receipt.
func ProviderLanes(git *gitops.Service, root, branch, upstream, revision string) LaneReport {
	out := LaneReport{State: "observed", Source: "local_git_config", Lanes: []Lane{}}
	out.SourceTree, _ = git.Tree(root)
	for _, provider := range []string{"github", "gitverse", "local"} {
		lane := Lane{Provider: provider, State: "not_configured", CIState: "UNKNOWN", Auth: AuthStates{"UNKNOWN", "UNKNOWN", "UNKNOWN"}}
		if provider == "local" {
			lane.State, lane.LocalBranch, lane.Revision = "observed", branch, revision
		}
		out.Lanes = append(out.Lanes, lane)
	}
	remotes, err := git.Remotes(root)
	if err != nil {
		out.State = "unknown"
		return out
	}
	slices.SortFunc(remotes, func(a, b gitops.Remote) int {
		if a.Name == "origin" && b.Name != "origin" {
			return -1
		}
		if b.Name == "origin" && a.Name != "origin" {
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	for _, remote := range remotes {
		raw := remote.URL
		if !strings.Contains(raw, "://") {
			if at := strings.IndexByte(raw, '@'); at >= 0 {
				raw = raw[at+1:]
			}
			host, path, ok := strings.Cut(raw, ":")
			if !ok {
				continue
			}
			raw = "ssh://" + host + "/" + path
		}
		u, err := url.Parse(raw)
		if err != nil {
			continue
		}
		index := -1
		switch strings.ToLower(u.Hostname()) {
		case "github.com":
			index = 0
		case "gitverse.ru":
			index = 1
		}
		repo := strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
		if index < 0 || !repositoryPath.MatchString(repo) || out.Lanes[index].State == "configured" {
			continue
		}
		out.Lanes[index].State, out.Lanes[index].Remote, out.Lanes[index].Repository = "configured", remote.Name, repo
		if strings.HasPrefix(upstream, remote.Name+"/") {
			out.Lanes[index].TrackingRef = upstream
		}
	}
	return out
}
