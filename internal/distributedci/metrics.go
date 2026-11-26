package distributedci

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"
)

const MetricsPath = "/var/lib/mcpshka/distributed-ci/metrics.json"

type ProviderTiming struct {
	Provider   string    `json:"provider"`
	Runs       int       `json:"runs"`
	Seconds    float64   `json:"seconds"`
	ObservedAt time.Time `json:"observed_at"`
	Trust      string    `json:"trust"`
}

func SaveMetrics(r Report) error {
	m := map[string]ProviderTiming{}
	seen := map[string]bool{}
	for _, o := range append(slices.Clone(r.Attempts), r.Shards...) {
		key := fmt.Sprintf("%s:%d:%d", o.Provider, o.Shard, o.RunID)
		if seen[key] || o.Seconds <= 0 {
			continue
		}
		seen[key] = true
		v := m[o.Provider]
		v.Provider = o.Provider
		v.Runs++
		v.Seconds += o.Seconds
		v.ObservedAt = time.Now().UTC()
		v.Trust = "UNTRUSTED"
		m[o.Provider] = v
	}
	out := []ProviderTiming{}
	for _, v := range m {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Provider < out[j].Provider })
	b, e := json.Marshal(out)
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(MetricsPath), 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(MetricsPath), ".metrics-")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if _, e = f.Write(b); e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	return os.Rename(name, MetricsPath)
}
func ReadMetrics() []ProviderTiming { return readMetrics(MetricsPath) }
func readMetrics(path string) []ProviderTiming {
	f, e := os.Open(path)
	if e != nil {
		return nil
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil || st.Size() > 16384 {
		return nil
	}
	b := make([]byte, st.Size())
	if _, e = io.ReadFull(f, b); e != nil {
		return nil
	}
	var out []ProviderTiming
	if json.Unmarshal(b, &out) != nil || len(out) > 4 {
		return nil
	}
	return out
}
