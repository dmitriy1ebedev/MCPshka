package gitprovider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"mcpshka/internal/distributedci"
)

// Artifact URLs are untrusted. Bearer credentials only reach the fixed API;
// signed object downloads get no Authorization header and no redirects.
func (p *CIProvider) sourceCraftArtifact(ctx context.Context, path string) ([]byte, error) {
	c := p.Client
	if strings.HasPrefix(path, "/") && !strings.HasPrefix(path, "//") {
		return c.ciRequest(ctx, "GET", path, nil)
	}
	u, e := url.Parse(path)
	if e != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return nil, distributedci.ErrEvidence
	}
	if u.Host == "api.sourcecraft.tech" {
		return c.ciRequest(ctx, "GET", u.RequestURI(), nil)
	}
	if u.Host != "storage.yandexcloud.net" && !strings.HasSuffix(u.Host, ".storage.yandexcloud.net") {
		return nil, distributedci.ErrEvidence
	}
	req, e := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if e != nil {
		return nil, distributedci.ErrEvidence
	}
	res, e := c.http.Do(req)
	if e != nil {
		return nil, errors.New("CI artifact unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, errors.New("CI artifact unavailable")
	}
	b, e := io.ReadAll(io.LimitReader(res.Body, 65537))
	if e != nil || len(b) > 65536 {
		return nil, distributedci.ErrEvidence
	}
	return b, nil
}

func (p *CIProvider) loadSourceCraft(ctx context.Context, plan distributedci.Plan) error {
	var data struct {
		Runs []struct {
			Status    string `json:"status"`
			Workflows []struct {
				Tasks []struct {
					Cubes []struct {
						Artifacts []struct {
							Path   string `json:"local_path"`
							Status string `json:"status"`
							URL    string `json:"download_url"`
						} `json:"artifacts"`
					} `json:"cubes"`
				} `json:"tasks"`
			} `json:"workflows"`
		} `json:"runs"`
	}
	if e := p.Client.ciJSON(ctx, "GET", p.Client.ciRoute()+"/cicd/runs?page_size=20", nil, &data); e != nil {
		return e
	}
	for _, r := range data.Runs {
		for _, w := range r.Workflows {
			for _, t := range w.Tasks {
				for _, c := range t.Cubes {
					for _, a := range c.Artifacts {
						if a.Status != "success" || (a.Path != "report.json" && !strings.HasSuffix(a.Path, "/report.json")) {
							continue
						}
						raw, e := p.sourceCraftArtifact(ctx, a.URL)
						if e != nil {
							return e
						}
						var o distributedci.Observation
						if json.Unmarshal(raw, &o) != nil {
							return distributedci.ErrEvidence
						}
						// Other snapshots and the independent complement workflow are unrelated.
						if o.Commit != plan.Commit || o.Provider != "sourcecraft" {
							continue
						}
						if plan.Check(o) != nil || o.State == "PASS" && r.Status != "success" {
							return distributedci.ErrEvidence
						}
						prev, ok := p.cache[o.Shard]
						if !ok || o.State == "CODE_FAIL" || prev.State != "CODE_FAIL" && prev.State != "PASS" {
							p.cache[o.Shard] = o
						}
					}
				}
			}
		}
	}
	return nil
}

func (p *CIProvider) fetchSourceCraftBuild(ctx context.Context, plan distributedci.Plan, o distributedci.Observation, a distributedci.Artifact) ([]byte, error) {
	var data struct {
		Runs []struct {
			Status    string
			Workflows []struct {
				Tasks []struct {
					Cubes []struct {
						Artifacts []struct {
							Path   string `json:"local_path"`
							Status string
							URL    string `json:"download_url"`
						}
					}
				}
			}
		}
	}
	if e := p.Client.ciJSON(ctx, "GET", p.Client.ciRoute()+"/cicd/runs?page_size=20", nil, &data); e != nil {
		return nil, e
	}
	for _, r := range data.Runs {
		if r.Status != "success" {
			continue
		}
		for _, w := range r.Workflows {
			for _, t := range w.Tasks {
				for _, c := range t.Cubes {
					matched := false
					for _, art := range c.Artifacts {
						if art.Status == "success" && (art.Path == "report.json" || strings.HasSuffix(art.Path, "/report.json")) {
							raw, e := p.sourceCraftArtifact(ctx, art.URL)
							var report distributedci.Observation
							if e == nil && json.Unmarshal(raw, &report) == nil && plan.Check(report) == nil && report.Shard == o.Shard && report.State == "PASS" {
								matched = true
							}
						}
					}
					if !matched {
						continue
					}
					for _, art := range c.Artifacts {
						if art.Status == "success" && (art.Path == a.Path || strings.TrimSuffix(art.Path, "/") == "out/distributed") {
							// Reuse the same credential boundary, with the binary output budget.
							raw, e := p.sourceCraftDownloadBuild(ctx, art.URL)
							if e != nil || art.Path == a.Path {
								return raw, e
							}
							return unpackCIArtifact(raw, a)
						}
					}
				}
			}
		}
	}
	return nil, errors.New("SourceCraft build artifact unavailable")
}
func (p *CIProvider) sourceCraftDownloadBuild(ctx context.Context, path string) ([]byte, error) {
	u, e := url.Parse(path)
	if e != nil {
		return nil, distributedci.ErrEvidence
	}
	if strings.HasPrefix(path, "/") && !strings.HasPrefix(path, "//") {
		return p.Client.downloadCI(ctx, path)
	}
	if u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return nil, distributedci.ErrEvidence
	}
	if u.Host == "api.sourcecraft.tech" {
		return p.Client.downloadCI(ctx, u.RequestURI())
	}
	if u.Host != "storage.yandexcloud.net" && !strings.HasSuffix(u.Host, ".storage.yandexcloud.net") {
		return nil, distributedci.ErrEvidence
	}
	req, e := http.NewRequestWithContext(ctx, "GET", path, nil)
	if e != nil {
		return nil, e
	}
	res, e := p.Client.http.Do(req)
	if e != nil {
		return nil, errors.New("CI artifact download unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, errors.New("CI artifact unavailable")
	}
	raw, e := io.ReadAll(io.LimitReader(res.Body, (64<<20)+1))
	if e != nil || len(raw) > 64<<20 {
		return nil, distributedci.ErrEvidence
	}
	return raw, nil
}
