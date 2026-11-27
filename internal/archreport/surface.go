package archreport

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Surface lists the exported API and the test names of a module, one sorted
// line each (MCP-068). A pure structural move (rename of files, split of a
// package file) must leave this list unchanged: CI diffs Surface of the base
// and the head of a refactoring change and refuses any difference.
func Surface(root string) ([]string, error) {
	module, err := modulePath(filepath.Join(root, "go.mod"))
	if err != nil {
		return nil, err
	}
	var out []string
	err = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != root && (d.Name() == "vendor" || d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") {
			return nil
		}
		rel, _ := filepath.Rel(root, filepath.Dir(p))
		pkg := module
		if rel != "." {
			pkg += "/" + filepath.ToSlash(rel)
		}
		f, err := parser.ParseFile(token.NewFileSet(), p, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		test := strings.HasSuffix(p, "_test.go")
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				name := d.Name.Name
				if test {
					if strings.HasPrefix(name, "Test") || strings.HasPrefix(name, "Benchmark") || strings.HasPrefix(name, "Fuzz") {
						out = append(out, pkg+" test "+name)
					}
					continue
				}
				if !ast.IsExported(name) {
					continue
				}
				if d.Recv != nil && len(d.Recv.List) == 1 {
					recv := typeName(d.Recv.List[0].Type)
					if !ast.IsExported(recv) {
						continue
					}
					name = recv + "." + name
				}
				out = append(out, pkg+" func "+name+" "+signature(d.Type))
			case *ast.GenDecl:
				if test {
					continue
				}
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						if ast.IsExported(s.Name.Name) {
							out = append(out, pkg+" type "+s.Name.Name)
						}
					case *ast.ValueSpec:
						for _, n := range s.Names {
							if ast.IsExported(n.Name) {
								out = append(out, pkg+" "+d.Tok.String()+" "+n.Name)
							}
						}
					}
				}
			}
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

func typeName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return typeName(t.X)
	case *ast.IndexExpr:
		return typeName(t.X)
	case *ast.IndexListExpr:
		return typeName(t.X)
	case *ast.Ident:
		return t.Name
	}
	return ""
}

// signature is the parameter/result arity; types are compared by the
// compiler, this guards accidental exported signature changes cheaply.
func signature(t *ast.FuncType) string {
	count := func(fl *ast.FieldList) int {
		if fl == nil {
			return 0
		}
		n := 0
		for _, f := range fl.List {
			if len(f.Names) == 0 {
				n++
			} else {
				n += len(f.Names)
			}
		}
		return n
	}
	return "(" + itoa(count(t.Params)) + ")" + itoa(count(t.Results))
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}
