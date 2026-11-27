// Linux pull runner for the MCPshka runnerhub protocol.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

type config struct {
	URL string `json:"url"`
	ID  string `json:"id"`
	Key string `json:"key"`
}

var invalidName = regexp.MustCompile(`[^A-Za-z0-9._-]`)

func run(ctx context.Context, args []string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	host, _ := os.Hostname()
	if host == "" {
		host = "runner"
	}
	flags := flag.NewFlagSet("mcpshka-runner", flag.ContinueOnError)
	origin := flags.String("url", "", "MCPshka origin (defaults to the saved URL)")
	token := flags.String("token", "", "one-time registration token")
	name := flags.String("name", host[:min(40, len(host))], "runner name")
	path := flags.String("conf", filepath.Join(home, ".mcpshka-runner.json"), "runner credential file")
	idle := flags.Int("idle-exit", 10, "idle timeout in minutes (0 disables it)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *idle < 0 || *idle > 24*60 || flags.NArg() != 0 {
		return errors.New("expected flags only and --idle-exit in 0..1440")
	}
	abs, err := filepath.Abs(*path)
	if err != nil {
		return err
	}
	lock, err := os.OpenFile(abs+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil
		}
		return err
	}

	var cfg config
	switch raw, err := os.ReadFile(abs); {
	case err == nil:
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return err
		}
	case !errors.Is(err, os.ErrNotExist):
		return err
	}
	if *origin == "" {
		*origin = cfg.URL
	}
	base := strings.TrimRight(*origin, "/")
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" ||
		(u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "::1" || u.Hostname() == "localhost"))) {
		return errors.New("expected an HTTPS origin")
	}

	r := runner{base: base, tools: newToolchain(home, filepath.Dir(abs)), heartbeat: 30 * time.Second}
	if strings.TrimRight(cfg.URL, "/") != base || cfg.Key == "" {
		if *token == "" {
			*token = os.Getenv("MCPSHKA_RUNNER_TOKEN")
		}
		if *token == "" {
			return errors.New("not registered: pass --token from MCPshka «Раннеры»")
		}
		cfg = config{URL: base}
		clean := invalidName.ReplaceAllString(*name, "-")
		if err := r.call(ctx, "register", map[string]string{"token": *token, "name": clean[:min(48, len(clean))]}, &cfg, time.Minute); err != nil {
			return err
		}
		if cfg.ID == "" || cfg.Key == "" {
			return errors.New("incomplete registration reply")
		}
		if err := saveConfig(abs, cfg); err != nil {
			return err
		}
	}
	r.key = cfg.Key
	r.tools.prepare(ctx)
	return r.poll(ctx, time.Duration(*idle)*time.Minute)
}

func saveConfig(path string, cfg config) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".runner-key-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	err = json.NewEncoder(f).Encode(cfg)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil && !errors.Is(err, flag.ErrHelp) && ctx.Err() == nil {
		log.Print(err)
		os.Exit(1)
	}
}
