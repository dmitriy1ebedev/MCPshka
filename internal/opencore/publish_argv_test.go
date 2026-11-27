package opencore

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func TestGitCommandKeepsTheTokenOutOfArgv(t *testing.T) {
	const header = "Authorization: Bearer secret-token-value"
	cmd := gitCommand(context.Background(), "/tmp/x.git", header, []string{"A=1"}, []string{"ls-remote", "https://example.invalid/r.git"})
	if strings.Contains(strings.Join(cmd.Args, " "), "secret-token-value") {
		t.Fatal("token visible in argv:", cmd.Args)
	}
	if !slices.Contains(cmd.Env, "GIT_CONFIG_VALUE_0="+header) || !slices.Contains(cmd.Env, "GIT_CONFIG_KEY_0=http.extraHeader") || !slices.Contains(cmd.Env, "A=1") {
		t.Fatal("header or caller env missing:", cmd.Env)
	}
	if plain := gitCommand(context.Background(), "/tmp/x.git", "", nil, []string{"status"}); slices.ContainsFunc(plain.Env, func(e string) bool { return strings.HasPrefix(e, "GIT_CONFIG_") }) {
		t.Fatal("no auth must not set git config")
	}
}
