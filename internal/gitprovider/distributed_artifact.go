package gitprovider

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	"mcpshka/internal/distributedci"
)

// downloadCI follows only signed storage redirects, with no bearer forwarding.
func (c *Client) downloadCI(ctx context.Context, path string) ([]byte, error) {
	token, e := c.token(c.site)
	if e != nil {
		return nil, e
	}
	req, e := http.NewRequestWithContext(ctx, "GET", c.base+path, nil)
	if e != nil {
		return nil, e
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if c.site == MosHub {
		req.Header.Del("Authorization")
		req.Header.Set("PRIVATE-TOKEN", token)
	}
	if c.site == GitVerse {
		req.Header.Set("Accept", "application/vnd.gitverse.object+json;version=latest")
	}
	res, e := c.http.Do(req)
	if e != nil {
		return nil, errors.New("CI artifact download unavailable")
	}
	if res.StatusCode == 302 {
		location := res.Header.Get("Location")
		res.Body.Close()
		u, e := url.Parse(location)
		if e != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || !(strings.HasSuffix(u.Hostname(), ".blob.core.windows.net") || strings.HasSuffix(u.Hostname(), ".actions.githubusercontent.com") || u.Hostname() == "storage.yandexcloud.net" || strings.HasSuffix(u.Hostname(), ".storage.yandexcloud.net")) {
			return nil, distributedci.ErrEvidence
		}
		req, e = http.NewRequestWithContext(ctx, "GET", location, nil)
		if e != nil {
			return nil, e
		}
		res, e = c.http.Do(req)
		if e != nil {
			return nil, errors.New("CI artifact download unavailable")
		}
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, errors.New("CI artifact download refused")
	}
	raw, e := io.ReadAll(io.LimitReader(res.Body, (64<<20)+1))
	if e != nil || len(raw) > 64<<20 {
		return nil, errors.New("CI artifact download exceeds budget")
	}
	return raw, nil
}

func (p *CIProvider) FetchArtifact(ctx context.Context, plan distributedci.Plan, o distributedci.Observation, a distributedci.Artifact) ([]byte, error) {
	if plan.Check(o) != nil || o.Provider != p.Name() || o.State != "PASS" || !distributedci.ValidArtifact(a) {
		return nil, distributedci.ErrEvidence
	}
	c := p.Client
	route := c.ciRoute()
	if c.site == SourceCraft {
		return p.fetchSourceCraftBuild(ctx, plan, o, a)
	}
	if c.site == MosHub {
		var jobs []ciJob
		if e := c.ciJSON(ctx, "GET", fmt.Sprintf("%s/pipelines/%d/jobs?per_page=100&include_retried=false", route, o.RunID), nil, &jobs); e != nil {
			return nil, e
		}
		for _, j := range jobs {
			if j.Name == "distributed-shard-0" && j.Status == "success" && j.Commit.ID == plan.Snapshot && j.Pipeline.ID == o.RunID {
				return c.downloadCI(ctx, fmt.Sprintf("%s/jobs/%d/artifacts/%s", route, j.ID, a.Path))
			}
		}
		return nil, errors.New("CI build job unavailable")
	}
	var data struct {
		Artifacts []struct {
			ID      int64  `json:"id"`
			Name    string `json:"name"`
			Expired bool   `json:"expired"`
		} `json:"artifacts"`
	}
	if e := c.ciJSON(ctx, "GET", fmt.Sprintf("%s/actions/runs/%d/artifacts?per_page=100", route, o.RunID), nil, &data); e != nil {
		return nil, e
	}
	for _, artifact := range data.Artifacts {
		if artifact.Name != "project-build" || artifact.Expired || artifact.ID <= 0 {
			continue
		}
		raw, e := c.downloadCI(ctx, fmt.Sprintf("%s/actions/artifacts/%d/zip", route, artifact.ID))
		if e != nil {
			return nil, e
		}
		return unpackCIArtifact(raw, a)
	}
	return nil, errors.New("CI build artifact unavailable")
}
func unpackCIArtifact(raw []byte, a distributedci.Artifact) ([]byte, error) {
	z, e := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if e != nil {
		return nil, distributedci.ErrEvidence
	}
	var found []byte
	for _, f := range z.File {
		if f.Name != a.Path && f.Name != filepath.Base(a.Path) {
			continue
		}
		if found != nil || !f.Mode().IsRegular() || f.UncompressedSize64 != uint64(a.Bytes) {
			return nil, distributedci.ErrEvidence
		}
		r, e := f.Open()
		if e != nil {
			return nil, e
		}
		b, e := io.ReadAll(io.LimitReader(r, a.Bytes+1))
		r.Close()
		if e != nil || int64(len(b)) != a.Bytes {
			return nil, distributedci.ErrEvidence
		}
		found = b
	}
	if found == nil {
		return nil, errors.New("CI artifact missing from archive")
	}
	return found, nil
}
