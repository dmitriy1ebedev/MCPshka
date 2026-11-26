package main

import (
	"fmt"
	"golang.org/x/sys/unix"
)

func protectControllerProcess() error {
	// Jobs sharing the service UID must not inspect the controller's memory/env.
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return fmt.Errorf("controller process isolation: HOLD: %w", err)
	}
	value, err := unix.PrctlRetInt(unix.PR_GET_DUMPABLE, 0, 0, 0, 0)
	if err != nil || value != 0 {
		return fmt.Errorf("controller process isolation unverified: HOLD")
	}
	return nil
}
