package project

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type boundedOutput struct{ bytes.Buffer }

func (b *boundedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 4<<20 {
		return 0, errors.New("change selection output budget")
	}
	return b.Buffer.Write(p)
}
func selectionCommand(ctx context.Context, root, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local", "GOWORK=off", "GOFLAGS=-mod=readonly", "GOMAXPROCS=1")
	var out boundedOutput
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

type listedPackage struct {
	Dir, ImportPath                    string
	Imports, TestImports, XTestImports []string
	Error                              *struct{ Err string }
}

// changedTest selects the merge-base diff and transitive reverse dependencies.
func changedTest(plan TestPlan) TestPlan {
	fallback := func(reason string) TestPlan {
		plan.Reason += "; полный обязательный прогон: " + reason
		return plan
	}
	if !fileExists(filepath.Join(plan.Root, "go.mod")) {
		return plan
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var base []byte
	var err error
	for _, ref := range []string{"main", "master"} {
		base, err = selectionCommand(ctx, plan.Root, "git", "merge-base", "HEAD", ref)
		if err == nil {
			break
		}
	}
	if err != nil {
		return fallback("merge-base недоступен")
	}
	diff, err := selectionCommand(ctx, plan.Root, "git", "diff", "--name-only", "-z", strings.TrimSpace(string(base)), "--")
	if err != nil {
		return fallback("diff недоступен")
	}
	extra, err := selectionCommand(ctx, plan.Root, "git", "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return fallback("список новых файлов недоступен")
	}
	files := strings.Split(string(append(diff, extra...)), "\x00")
	for _, p := range files {
		if p == "go.mod" || p == "go.sum" || p == "Makefile" || strings.HasPrefix(p, "packaging/") || strings.HasPrefix(p, ".github/") || strings.HasPrefix(p, ".gitverse/") {
			return fallback("изменены входы сборки или политики")
		}
	}
	raw, err := selectionCommand(ctx, plan.Root, "go", "list", "-json", "./...")
	if err != nil {
		return fallback("go list недоступен без сети")
	}
	graph := []listedPackage{}
	dec := json.NewDecoder(bytes.NewReader(raw))
	for {
		var p listedPackage
		err := dec.Decode(&p)
		if err == io.EOF {
			break
		}
		if err != nil || p.Error != nil {
			return fallback("неполный граф go list")
		}
		graph = append(graph, p)
		if len(graph) > 4096 {
			return fallback("превышен лимит графа пакетов")
		}
	}
	selected := map[string]bool{}
	for _, file := range files {
		if file == "" {
			continue
		}
		dir := filepath.Clean(filepath.Join(plan.Root, filepath.Dir(file)))
		matched := false
		for _, p := range graph {
			if p.Dir == dir {
				selected[p.ImportPath] = true
				matched = true
			}
		}
		if !matched && !strings.HasPrefix(file, "docs/") && !strings.HasSuffix(file, ".md") {
			return fallback("изменён вход вне известных пакетов")
		}
	}
	for changed := true; changed; {
		changed = false
		for _, p := range graph {
			if selected[p.ImportPath] {
				continue
			}
			deps := append(append(append([]string{}, p.Imports...), p.TestImports...), p.XTestImports...)
			for _, dep := range deps {
				if selected[dep] {
					selected[p.ImportPath] = true
					changed = true
					break
				}
			}
		}
	}
	packages := []string{}
	for _, p := range graph {
		rel, err := filepath.Rel(plan.Root, p.Dir)
		if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
			return fallback("пакет вне модуля")
		}
		if selected[p.ImportPath] || rel == "internal/archlint" {
			packages = append(packages, "./"+filepath.ToSlash(rel))
		}
	}
	if len(selected) == 0 || len(packages) > 128 {
		return fallback("пустой diff или превышен лимит выборки")
	}
	sort.Strings(packages)
	reason := "merge-base " + strings.TrimSpace(string(base)) + " diff + обратные зависимости go list: " + strings.Join(packages, ", ")
	if plan.Kind != "go" {
		plan.Reason += "; " + reason + "; обязательный workflow сохранён"
		return plan
	}
	plan.Args = append([]string{"test", "-p", "1"}, packages...)
	plan.Reason = reason + "; обязательные архитектурные проверки включены"
	return plan
}
