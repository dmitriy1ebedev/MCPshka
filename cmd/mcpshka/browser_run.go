package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"mcpshka/internal/browser"
)

// browser-run SPEC_FILE: the spec file (written by browser_run, 0600) is
// removed once read, since imported cookies are secrets.
func runBrowser(args []string) {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: mcpshka browser-run <spec.json>")
		os.Exit(2)
	}
	// Only browser_run writes specs there (outside every fs_write root), so
	// its Face ID check for click/type cannot be bypassed via exec_start.
	if filepath.Dir(filepath.Clean(args[0])) != filepath.Join(browser.StateDir, "specs") {
		fmt.Fprintln(os.Stderr, "spec must come from browser_run")
		os.Exit(2)
	}
	raw, err := os.ReadFile(args[0])
	_ = os.Remove(args[0])
	var spec browser.Spec
	if err == nil {
		err = json.Unmarshal(raw, &spec)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, spec.ProcessTimeout())
	defer cancel()
	res := browser.Run(ctx, spec)
	_ = json.NewEncoder(os.Stdout).Encode(res)
	if res.State == "blocked" {
		os.Exit(3) // the browser could not run: not an application failure
	}
	if res.State != "done" {
		os.Exit(1)
	}
}
