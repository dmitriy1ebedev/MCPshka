// Package archreport builds a read-only architecture report of a Go module
// from its sources (MCP-061): package import graph, size, tests and
// fan-in/fan-out, plus import cycles. It parses import clauses only and runs
// no external tools.
package archreport

import (
	"bufio"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type Package struct {
	Path    string   `json:"path"`
	Files   int      `json:"files"`
	Lines   int      `json:"lines"`
	Tests   int      `json:"tests"`
	Imports []string `json:"imports"`
	FanIn   int      `json:"fan_in"`
	FanOut  int      `json:"fan_out"`
}

type Report struct {
	Module   string     `json:"module"`
	Packages []Package  `json:"packages"`
	Cycles   [][]string `json:"cycles"`
	Lines    int        `json:"lines"`
	Tests    int        `json:"tests"`
}

const maxFiles = 20000

// Build reads root/go.mod and every non-vendor .go file below root.
func Build(root string) (Report, error) {
	module, err := modulePath(filepath.Join(root, "go.mod"))
	if err != nil {
		return Report{}, err
	}
	pkgs := map[string]*Package{}
	files := 0
	err = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if p != root && (name == "vendor" || name == "testdata" || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if files++; files > maxFiles {
			return errors.New("module too large for arch report")
		}
		rel, _ := filepath.Rel(root, filepath.Dir(p))
		ip := module
		if rel != "." {
			ip = module + "/" + filepath.ToSlash(rel)
		}
		pkg := pkgs[ip]
		if pkg == nil {
			pkg = &Package{Path: ip}
			pkgs[ip] = pkg
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		pkg.Files++
		pkg.Lines += strings.Count(string(src), "\n")
		f, err := parser.ParseFile(token.NewFileSet(), p, src, parser.ImportsOnly)
		if err != nil {
			return err
		}
		test := strings.HasSuffix(p, "_test.go")
		if test {
			for _, line := range strings.Split(string(src), "\n") {
				if strings.HasPrefix(line, "func Test") {
					pkg.Tests++
				}
			}
			return nil // test-only imports do not shape the production graph
		}
		for _, im := range f.Imports {
			path, _ := strconv.Unquote(im.Path.Value)
			if (path == module || strings.HasPrefix(path, module+"/")) && !contains(pkg.Imports, path) {
				pkg.Imports = append(pkg.Imports, path)
			}
		}
		return nil
	})
	if err != nil {
		return Report{}, err
	}
	r := Report{Module: module}
	for _, p := range pkgs {
		sort.Strings(p.Imports)
		p.FanOut = len(p.Imports)
		for _, dep := range p.Imports {
			if q := pkgs[dep]; q != nil {
				q.FanIn++
			}
		}
	}
	for _, p := range pkgs {
		r.Packages = append(r.Packages, *p)
		r.Lines += p.Lines
		r.Tests += p.Tests
	}
	sort.Slice(r.Packages, func(i, j int) bool { return r.Packages[i].Path < r.Packages[j].Path })
	r.Cycles = cycles(pkgs)
	return r, nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func modulePath(gomod string) (string, error) {
	f, err := os.Open(gomod)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if m, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "module "); ok {
			return strings.Trim(strings.TrimSpace(m), `"`), nil
		}
	}
	return "", errors.New("go.mod has no module line")
}

// cycles returns each strongly connected component larger than one package.
func cycles(pkgs map[string]*Package) [][]string {
	index, low, onStack := map[string]int{}, map[string]int{}, map[string]bool{}
	var stack []string
	var out [][]string
	next := 0
	var visit func(string)
	visit = func(v string) {
		index[v], low[v] = next, next
		next++
		stack = append(stack, v)
		onStack[v] = true
		for _, w := range pkgs[v].Imports {
			if pkgs[w] == nil {
				continue
			}
			if _, seen := index[w]; !seen {
				visit(w)
				low[v] = min(low[v], low[w])
			} else if onStack[w] {
				low[v] = min(low[v], index[w])
			}
		}
		if low[v] == index[v] {
			var comp []string
			for {
				w := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				onStack[w] = false
				comp = append(comp, w)
				if w == v {
					break
				}
			}
			if len(comp) > 1 {
				sort.Strings(comp)
				out = append(out, comp)
			}
		}
	}
	names := make([]string, 0, len(pkgs))
	for n := range pkgs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if _, seen := index[n]; !seen {
			visit(n)
		}
	}
	return out
}
