package main

import (
	"context"
	"encoding/json"
	"fmt"
	"mcpshka/internal/distributedci"
	"os"
	"strconv"
	"time"
)

func main() {
	if len(os.Args) == 2 && (os.Args[1] == "sourcecraft" || os.Args[1] == "sourcecraft-start") {
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
		defer cancel()
		run := distributedci.SourceCraftWorker
		if os.Args[1] == "sourcecraft-start" {
			run = distributedci.SourceCraftStarted
		}
		o, e := run(ctx, ".", os.Getenv("SOURCECRAFT_COMMIT_REF"))
		if e != nil {
			fmt.Fprintln(os.Stderr, "CI shard identity or inventory invalid")
			os.Exit(2)
		}
		b, _ := json.Marshal(o)
		if os.WriteFile("report.json", b, 0644) != nil {
			os.Exit(2)
		}
		fmt.Println(distributedci.ResultMarker + string(b))
		// Keep the report artifact available for CODE_FAIL and INFRA_FAIL too.
		return
	}

	if len(os.Args) != 8 {
		fmt.Fprintln(os.Stderr, "ci-shard requires shard provider commit tree snapshot plan-hash run-id")
		os.Exit(2)
	}
	n, e := strconv.Atoi(os.Args[1])
	if e != nil {
		os.Exit(2)
	}
	id, e := strconv.ParseInt(os.Args[7], 10, 64)
	if e != nil {
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	defer cancel()
	o, e := distributedci.Worker(ctx, ".", n, os.Args[2], os.Args[3], os.Args[4], os.Args[5], os.Args[6], id)
	if e != nil {
		fmt.Fprintln(os.Stderr, "CI shard identity or inventory invalid")
		os.Exit(2)
	}
	b, _ := json.Marshal(o)
	if os.WriteFile("report.json", b, 0644) != nil {
		os.Exit(2)
	}
	fmt.Println(distributedci.ResultMarker + string(b))
	if o.State != "PASS" {
		os.Exit(1)
	}
}
