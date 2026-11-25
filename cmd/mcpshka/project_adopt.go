package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"strconv"

	"mcpshka/internal/repoadopt"
)

// project-adopt preview|apply REPO SOURCE|- [PLAN_SHA256]
func runProjectAdopt(args []string) {
	if len(args) < 3 || len(args) > 4 || (args[0] == "apply") != (len(args) == 4) || (args[0] != "preview" && args[0] != "apply") {
		fmt.Fprintln(os.Stderr, "usage: mcpshka project-adopt preview|apply <repo> <source|-> [plan_sha256]")
		os.Exit(2)
	}
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "project-adopt must run as root")
		os.Exit(2)
	}
	account, err := user.Lookup("mcpshka")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	uid, _ := strconv.Atoi(account.Uid)
	gid, _ := strconv.Atoi(account.Gid)
	source := args[2]
	if source == "-" {
		source = ""
	}
	var plan repoadopt.Plan
	if args[0] == "preview" {
		plan, err = repoadopt.Preview(args[1], source, uid, gid)
	} else {
		plan, err = repoadopt.Apply(args[1], source, uid, gid, args[3])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_ = json.NewEncoder(os.Stdout).Encode(plan)
}
