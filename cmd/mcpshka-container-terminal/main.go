package main

import (
	"fmt"
	"mcpshka/internal/containerterminal"
	"os"
	"strconv"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "shell" {
		if err := containerterminal.Shell(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) != 3 || len(os.Args[1]) != 32 {
		os.Exit(2)
	}
	uid, e := strconv.Atoi(os.Args[2])
	if e != nil {
		os.Exit(2)
	}
	if err := containerterminal.Run(uid); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
