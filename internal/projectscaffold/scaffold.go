// Package projectscaffold writes the starting files of a new project: README,
// ROADMAP with machine-readable gates, a test and a Makefile whose verify
// target project_onboard turns into the profile.
package projectscaffold

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var Name = regexp.MustCompile(`^[a-z][a-z0-9-]{1,39}$`)

var Kinds = map[string]map[string]string{
	"go": {
		"go.mod":       "module {{name}}\n\ngo 1.24\n",
		"main.go":      "package main\n\nimport \"fmt\"\n\nfunc greeting() string { return \"{{name}}\" }\n\nfunc main() { fmt.Println(greeting()) }\n",
		"main_test.go": "package main\n\nimport \"testing\"\n\nfunc TestGreeting(t *testing.T) {\n\tif greeting() != \"{{name}}\" {\n\t\tt.Fatal(greeting())\n\t}\n}\n",
		"Makefile":     ".PHONY: build test verify\nbuild:\n\tgo build ./...\ntest:\n\tgo test ./...\nverify: build test\n\tgo vet ./...\n",
	},
	"python": {
		"pyproject.toml":         "[project]\nname = \"{{name}}\"\nversion = \"0.1.0\"\nrequires-python = \">=3.11\"\n",
		"app/__init__.py":        "def greeting() -> str:\n    return \"{{name}}\"\n",
		"tests/test_greeting.py": "import unittest\n\nfrom app import greeting\n\n\nclass Greeting(unittest.TestCase):\n    def test_name(self):\n        self.assertEqual(greeting(), \"{{name}}\")\n",
		"Makefile":               ".PHONY: test verify\ntest:\n\tpython3 -m unittest discover -s tests -t .\nverify: test\n",
	},
	"generic": {
		"Makefile": ".PHONY: test verify\ntest:\n\t@test -f README.md\nverify: test\n",
	},
}

func common(name, kind string) map[string]string {
	return map[string]string{
		"README.md":  "# " + name + "\n\nСоздан MCPshka (" + kind + "). Сборка и проверка: `make verify` (профиль project_onboard).\n",
		"ROADMAP.md": "# " + name + " — ROADMAP\n\n## P01 Каркас\n\nGates: engineering, build\n\n- [ ] первая рабочая функция\n",
		".gitignore": "/bin/\n__pycache__/\n",
	}
}

// Write creates the files of kind in dir, refusing to overwrite anything.
func Write(dir, name, kind string) ([]string, error) {
	files, ok := Kinds[kind]
	if !ok || !Name.MatchString(name) {
		return nil, errors.New("kind must be go, python or generic and name [a-z][a-z0-9-]{1,39}")
	}
	all := common(name, kind)
	for k, v := range files {
		all[k] = v
	}
	var written []string
	for rel, body := range all {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return written, err
		}
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return written, err
		}
		_, err = f.WriteString(strings.ReplaceAll(body, "{{name}}", name))
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return written, err
		}
		written = append(written, rel)
	}
	return written, nil
}
