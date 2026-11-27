// Package quality computes a CI quality gate (MCP-059): per-function
// cyclomatic complexity, statement coverage from a Go cover profile and the
// CRAP score comp²·(1-cov)³+comp. It refuses to report coverage at all when
// the test run failed, so a red suite can never look "covered".
package quality

import (
	"bufio"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// ErrTestsFailed means coverage of a failing run is not evidence.
var ErrTestsFailed = errors.New("QUALITY_REFUSED: tests failed; coverage of a red run is not reported")

type Func struct {
	File       string  `json:"file"`
	Name       string  `json:"name"`
	Line       int     `json:"line"`
	Complexity int     `json:"complexity"`
	Coverage   float64 `json:"coverage"` // 0..1
	CRAP       float64 `json:"crap"`
}

type block struct {
	startLine, endLine, stmts, count int
}

// Report scores the functions of the given repo-relative files.
func Report(root, module, profile string, testsPassed bool, files []string) ([]Func, error) {
	if !testsPassed {
		return nil, ErrTestsFailed
	}
	blocks, err := readProfile(profile, module)
	if err != nil {
		return nil, err
	}
	var out []Func
	for _, rel := range files {
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, filepath.Join(root, rel), nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			start, end := fset.Position(fn.Pos()).Line, fset.Position(fn.End()).Line
			total, covered := 0, 0
			for _, b := range blocks[filepath.ToSlash(rel)] {
				if b.startLine >= start && b.endLine <= end {
					total += b.stmts
					if b.count > 0 {
						covered += b.stmts
					}
				}
			}
			cov := 0.0
			if total > 0 {
				cov = float64(covered) / float64(total)
			}
			c := complexity(fn)
			miss := 1 - cov
			out = append(out, Func{File: rel, Name: fn.Name.Name, Line: start, Complexity: c, Coverage: cov,
				CRAP: float64(c*c)*miss*miss*miss + float64(c)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CRAP > out[j].CRAP })
	return out, nil
}

// Gate fails for functions above the CRAP threshold.
func Gate(funcs []Func, threshold float64) error {
	var bad []string
	for _, f := range funcs {
		if f.CRAP > threshold {
			bad = append(bad, fmt.Sprintf("%s:%d %s CRAP %.1f (complexity %d, coverage %.0f%%)", f.File, f.Line, f.Name, f.CRAP, f.Complexity, f.Coverage*100))
		}
	}
	if len(bad) > 0 {
		return errors.New("QUALITY_GATE: " + strings.Join(bad, "; "))
	}
	return nil
}

func complexity(fn *ast.FuncDecl) int {
	c := 1
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt, *ast.CaseClause, *ast.CommClause:
			c++
		case *ast.BinaryExpr:
			if x.Op == token.LAND || x.Op == token.LOR {
				c++
			}
		case *ast.FuncLit:
			return false // closures are scored by their own size, not here
		}
		return true
	})
	return c
}

// readProfile parses "mode:" + "path/file.go:l.c,l.c stmts count" lines,
// keyed by repo-relative path.
func readProfile(path, module string) (map[string][]block, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string][]block{}
	sc := bufio.NewScanner(f)
	first := true
	for sc.Scan() {
		line := sc.Text()
		if first {
			first = false
			if !strings.HasPrefix(line, "mode:") {
				return nil, errors.New("not a Go cover profile")
			}
			continue
		}
		colon := strings.LastIndex(line, ":")
		if colon < 0 {
			continue
		}
		file := strings.TrimPrefix(strings.TrimPrefix(line[:colon], module), "/")
		fields := strings.Fields(line[colon+1:])
		if len(fields) != 3 {
			continue
		}
		span := strings.Split(fields[0], ",")
		if len(span) != 2 {
			continue
		}
		sl, _ := strconv.Atoi(strings.Split(span[0], ".")[0])
		el, _ := strconv.Atoi(strings.Split(span[1], ".")[0])
		stmts, _ := strconv.Atoi(fields[1])
		count, _ := strconv.Atoi(fields[2])
		out[file] = append(out[file], block{sl, el, stmts, count})
	}
	return out, sc.Err()
}
