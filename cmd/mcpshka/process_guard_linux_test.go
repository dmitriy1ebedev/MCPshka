package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
)

func TestControllerProcessRejectsSameUIDEnvironmentRead(t *testing.T) {
	if os.Getenv("MCPSHKA_PROCESS_GUARD_FIXTURE") == "1" {
		if err := protectControllerProcess(); err != nil {
			os.Exit(2)
		}
		fmt.Println("ready")
		var b [1]byte
		_, _ = os.Stdin.Read(b[:])
		os.Exit(0)
	}
	if os.Geteuid() == 0 {
		t.Skip("requires unprivileged same-UID peer")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestControllerProcessRejectsSameUIDEnvironmentRead$")
	cmd.Env = append(os.Environ(), "MCPSHKA_PROCESS_GUARD_FIXTURE=1")
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { input.Close(); cmd.Wait() }()
	if line, err := bufio.NewReader(output).ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("guard startup: %q %v", line, err)
	}
	f, err := os.Open(fmt.Sprintf("/proc/%d/environ", cmd.Process.Pid))
	if f != nil {
		f.Close()
		t.Fatal("same-UID environment read admitted")
	}
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("unexpected proc observation: %v", err)
	}
}
