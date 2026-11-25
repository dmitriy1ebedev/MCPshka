package jobs

import "path/filepath"

func heavyJobPriority(command string, args []string, opts StartOptions) bool {
	return !opts.DisableLowPriority && (opts.LowPriority || filepath.Base(command) == "go" && len(args) > 0 && (args[0] == "test" || args[0] == "build" || args[0] == "vet"))
}

func lowPriorityCommand(command string, args []string) (string, []string) {
	// Scope units inherit process scheduling; apply it inside the scope before exec.
	return "/usr/bin/ionice", append([]string{"-c", "3", "/usr/bin/nice", "-n", "10", "--", command}, args...)
}
