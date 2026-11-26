// A bounded unprivileged bootstrap for controllers predating the native worker.
package main

import (
	"context"
	"encoding/json"
	"log"
	"mcpshka/internal/releaseprep"
	"os"
)

func main() {
	if len(os.Args) != 2 {
		log.Fatal("release preparation requires bounded metadata")
	}
	result, err := releaseprep.Run(context.Background(), []byte(os.Args[1]))
	if err != nil {
		log.Fatal(err)
	}
	if json.NewEncoder(os.Stdout).Encode(result) != nil {
		log.Fatal("release result unavailable; reconcile original job")
	}
}
