package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mcpshka/internal/policy"
	"mcpshka/internal/projectlease"
	"os"
)

const mainPath = "/root/repos/mcpshka"

type operatorInput struct {
	Plan                      *projectlease.RootSealedRetirement
	Token, GateSHA, JournalID string
}

func run() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("root operator required; no MCP or sudo entrypoint")
	}
	if len(os.Args) != 2 {
		return fmt.Errorf("requires a fixed operator action")
	}
	action := os.Args[1]
	s := projectlease.New("/var/lib/mcpshka/leases", policy.Default())
	if action == "claim-main" {
		b := make([]byte, 16)
		if _, e := rand.Read(b); e != nil {
			return e
		}
		a, e := s.AcquireTracked(mainPath, "operator-root:mcpshka-one-tree-20261011", hex.EncodeToString(b))
		if e != nil {
			return e
		}
		// The new holder receipt goes only to the root operator's private pipe.
		return json.NewEncoder(os.Stdout).Encode(a)
	}
	if action != "retire" && action != "open-main" && action != "release-main" {
		return fmt.Errorf("action is not allowlisted")
	}
	b, e := io.ReadAll(io.LimitReader(os.Stdin, 32769))
	if e != nil || len(b) > 32768 {
		return fmt.Errorf("operator input exceeds budget")
	}
	var p operatorInput
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e = d.Decode(&p); e != nil {
		return fmt.Errorf("invalid operator input")
	}
	if e = d.Decode(new(any)); e != io.EOF {
		return fmt.Errorf("operator input must contain one object")
	}
	switch action {
	case "retire":
		if p.Plan == nil || p.Plan.GitCommonDir != mainPath+"/.git" || p.Token != "" || p.GateSHA != "" || p.JournalID != "" {
			return fmt.Errorf("only a pinned MCPshka retirement is allowlisted")
		}
		result, e := s.RetireRootSealed(*p.Plan)
		if e != nil {
			return e
		}
		return json.NewEncoder(os.Stdout).Encode(result)
	case "open-main":
		if p.Plan != nil {
			return fmt.Errorf("unexpected retirement plan")
		}
		return s.OpenRootWriterGate(mainPath, p.Token, p.GateSHA, p.JournalID)
	case "release-main":
		if p.Plan != nil || p.GateSHA != "" || p.JournalID != "" {
			return fmt.Errorf("unexpected release fields")
		}
		h, e := s.ValidateHolder(mainPath, p.Token)
		if e != nil {
			return e
		}
		if h.Owner != "operator-root:mcpshka-one-tree-20261011" {
			return fmt.Errorf("operator successor required")
		}
		result, e := s.Release(mainPath, p.Token)
		if e != nil {
			return e
		}
		return json.NewEncoder(os.Stdout).Encode(result)
	}
	return fmt.Errorf("unreachable action")
}
func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
