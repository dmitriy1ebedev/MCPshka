package statestore

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadSmallPrefixContextBudgetAndIntegrity(t *testing.T) {
	for _, mode := range []string{"valid", "oversized", "overflow", "corrupt", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			db, err := Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Put("backups/one", []byte("receipt")); err != nil {
				t.Fatal(err)
			}
			if err := db.Put("backups-other/unrelated", []byte("unrelated")); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "oversized":
				err = db.Put("backups/two", []byte(strings.Repeat("x", 4097)))
			case "corrupt":
				_, err = db.run("UPDATE records SET sha256='bad' WHERE path='backups/one';")
			case "overflow":
				var sql strings.Builder
				sql.WriteString("BEGIN;")
				for i := 0; i < 1000; i++ {
					fmt.Fprintf(&sql, "INSERT INTO records(path,payload,sha256) VALUES(%s,%s,%s);", text(fmt.Sprintf("backups/%04d", i)), blob([]byte("x")), text(checksum([]byte("x"))))
				}
				sql.WriteString("COMMIT;")
				_, err = db.run(sql.String())
			}
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancelled" {
				cancel()
			}
			records, err := db.ReadSmallPrefixContext(ctx, "backups/")
			if mode == "valid" {
				if err != nil || len(records) != 1 || string(records["backups/one"]) != "receipt" {
					t.Fatal(records, err)
				}
			} else if err == nil || records != nil {
				t.Fatal("incomplete/untrusted scan accepted", len(records), err)
			}
		})
	}
}
