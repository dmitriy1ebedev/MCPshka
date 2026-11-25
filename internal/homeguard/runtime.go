package homeguard

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"
)

func runtimeUnits(ctx context.Context, run Exec) (map[string]weight, error) {
	names, err := HomeUnits(ctx, run)
	if err != nil {
		return nil, err
	}
	out := units(ctx, run)
	for _, u := range names {
		out[u] = homeWeight
	}
	return out, nil
}
func rememberRuntime(ctx context.Context, run Exec, b *backup, current map[string]weight) error {
	if b.Runtime == nil {
		b.Runtime = map[string][]string{}
	}
	for u, w := range current {
		if _, ok := b.Runtime[u]; ok {
			continue
		}
		raw, err := run(ctx, "/usr/bin/systemctl", "show", "--property=CPUWeight,IOWeight,CPUQuotaPerSecUSec", "--", u)
		if err != nil {
			return fmt.Errorf("backup priority %s: %w", u, err)
		}
		props, err := previousProperties(string(raw), w.quota != "")
		if err != nil {
			return fmt.Errorf("backup priority %s: %w", u, err)
		}
		b.Runtime[u] = props
	}
	return nil
}
func previousProperties(raw string, quota bool) ([]string, error) {
	values := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("invalid property")
		}
		if _, ok := values[k]; ok {
			return nil, fmt.Errorf("duplicate property")
		}
		values[k] = v
	}
	var out []string
	for _, key := range []string{"CPUWeight", "IOWeight"} {
		v, ok := values[key]
		if !ok {
			return nil, fmt.Errorf("missing %s", key)
		}
		if v == "[not set]" {
			v = ""
		} else if n, err := strconv.Atoi(v); err != nil || n < 1 || n > 10000 {
			return nil, fmt.Errorf("invalid %s", key)
		}
		out = append(out, key+"="+v)
	}
	if quota {
		v, ok := values["CPUQuotaPerSecUSec"]
		if !ok {
			return nil, fmt.Errorf("missing CPU quota")
		}
		q := ""
		if v != "infinity" {
			duration, err := time.ParseDuration(v)
			if err != nil || duration <= 0 {
				return nil, fmt.Errorf("invalid CPU quota")
			}
			q = strconv.FormatFloat(float64(duration)/float64(time.Second)*100, 'f', -1, 64) + "%"
		}
		out = append(out, "CPUQuota="+q)
	}
	return out, nil
}
func restoreRuntime(ctx context.Context, run Exec, b *backup, current map[string]weight) error {
	for u, props := range b.Runtime {
		if _, exists := current[u]; !exists {
			log.Printf("home priority: skip unavailable %s runtime restore", u)
			continue
		}
		if out, err := run(ctx, "/usr/bin/systemctl", append([]string{"set-property", "--runtime", "--", u}, props...)...); err != nil {
			return fmt.Errorf("restore priority %s: %w: %.256s", u, err, out)
		}
	}
	return nil
}

// RuntimePolicy updates only the protected journal and runtime properties inside
// the broker sandbox. Persistent unit files are installed by coordinated-upgrade.
func RuntimePolicy(ctx context.Context, run Exec, state string, off bool) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	return withBackup(state, func(b *backup, exists bool) error {
		if !exists {
			if off {
				return nil
			}
			return fmt.Errorf("home priority policy not installed")
		}
		current, err := runtimeUnits(ctx, run)
		if err != nil {
			return err
		}
		if off {
			return restoreRuntime(ctx, run, b, current)
		}
		if err := rememberRuntime(ctx, run, b, current); err != nil {
			return err
		}
		data, err := json.Marshal(b)
		if err != nil {
			return err
		}
		if err = replaceFile(state, data, 0600); err != nil {
			return err
		}
		return Weights(ctx, run)
	})
}

// HomePriorityUnits includes the live home container scopes as well as services.
func HomePriorityUnits(ctx context.Context, run Exec) ([]string, error) {
	current, err := runtimeUnits(ctx, run)
	if err != nil {
		return nil, err
	}
	var names []string
	for u := range current {
		if _, fixedUnit := fixed[u]; !fixedUnit {
			names = append(names, u)
		}
	}
	sort.Strings(names)
	return names, nil
}
