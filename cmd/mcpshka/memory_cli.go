package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mcpshka/internal/freellm"
	"mcpshka/internal/issues"
	"mcpshka/internal/memory"
	"mcpshka/internal/runnerhub"
	"mcpshka/internal/runoutcome"
	"mcpshka/internal/statestore"
)

// memoryCommand runs the memory CLI (state dir: MCPSHKA_STATE_DIR, default
// the jobs.Manager state dir):
//
//	memory-import DIR [PROJECT]   Claude Code memory → MCPshka with coverage
//	memory-brief                  one MOTD/hook line
//	memory-consolidate            fold duplicates, write day/week summaries
//	memory-eval                   grade recall on the built-in exam (exit 1 below target)
//	memory-vectors [NAVEC.tar]    build the meaning table (downloads the pinned pack) and reindex
//	memory-questions FILE.json    {"<source>": ["question", …]} → recall questions, reindex
//	memory-inbox                  cut new chat exports in memory-inbox into chunks; coverage
//	memory-recall WORDS…          best three records (UserPromptSubmit hook of Claude Code)
//	memory-export FILE            portable JSON lines (mcpshka.memory/v1): own records, history, questions; secrets masked
//	memory-import-file FILE [keep-canon]   read such a file; ids recomputed, repeats skipped, canon kept only on request
func memoryCommand(ctx context.Context, name string, args []string) {
	dir := os.Getenv("MCPSHKA_STATE_DIR")
	if dir == "" {
		dir = "/var/lib/mcpshka/jobs" // jobs.Manager.StateDir
	}
	if name == "memory-brief" {
		fmt.Println(runnerhub.Default.Brief())
		if line := freellm.Brief(filepath.Join(dir, "fixes")); line != "" {
			fmt.Println(line)
		}
		if line := memory.Brief(ctx, dir); line != "" {
			fmt.Println(line)
		}
		// P29.N3: the night review of the problem cards, one line.
		if line := issues.NightBrief(issues.ReadNight(issues.NightPath(issueRegistry)), time.Now()); line != "" {
			fmt.Println(line)
		}
		return
	}
	st, err := memory.OpenExisting(dir)
	if err == nil && (name == "memory-vectors" || name == "memory-questions" || name == "memory-inbox" || name == "memory-sync" || name == "memory-optimize") {
		st, err = memory.Open(dir) // schema upgrade before a reindex
	}
	if err != nil {
		log.Fatal(err)
	}
	var out any
	switch name {
	case "memory-import":
		if len(args) < 1 || len(args) > 2 {
			log.Fatal("memory-import DIR [PROJECT]")
		}
		project := ""
		if len(args) == 2 {
			project = args[1]
		}
		out, err = st.ImportMarkdown(ctx, args[0], project)
	case "memory-sync":
		// Root session hook: new turns of Claude Code/Codex sessions.
		roots := args
		if len(roots) == 0 {
			roots = []string{"/root/.claude/projects", "/root/.codex/sessions"}
		}
		// The memory files themselves are imported by memory-import (owner
		// run, exam-checked): a blind reimport here moved the exam (23.11).
		out, err = st.SyncSessions(ctx, roots)
	case "memory-audit":
		out, err = st.Audit(ctx)
	case "memory-optimize", "memory-undo":
		// Root on the box acts as the owner; MCP clients need Face ID.
		if len(args) != 1 {
			log.Fatal("usage: mcpshka " + name + " <plan_sha|op>")
		}
		if name == "memory-optimize" {
			out, err = st.Optimize(ctx, args[0])
		} else {
			out, err = st.Undo(ctx, args[0])
		}
	case "memory-consolidate":
		out, err = st.Consolidate(ctx)
	case "memory-export":
		if len(args) != 1 {
			log.Fatal("memory-export FILE")
		}
		out, err = st.ExportFile(ctx, args[0])
	case "memory-import-file":
		if len(args) < 1 || len(args) > 2 || (len(args) == 2 && args[1] != "keep-canon") {
			log.Fatal("memory-import-file FILE [keep-canon]")
		}
		f, e := os.Open(args[0])
		if e != nil {
			log.Fatal(e)
		}
		out, err = st.Import(ctx, f, len(args) == 2)
		f.Close()
	case "memory-inbox":
		out, err = st.InboxScan(ctx)
	case "memory-recall":
		hits, e := st.Recall(ctx, memory.Query{Text: strings.Join(args, " "), Limit: 3})
		if e != nil {
			log.Fatal(e)
		}
		for _, h := range hits {
			body := []rune(strings.Join(strings.Fields(h.Body), " "))
			fmt.Printf("- %s [%s, %s]: %s\n", h.Title, h.Source, h.ValidFrom.Format("2006-01-02"), string(body[:min(len(body), 300)]))
		}
		return
	case "memory-eval":
		exam, e := memory.BuiltinExam()
		if e != nil {
			log.Fatal(e)
		}
		rep, e := st.Eval(ctx, exam)
		if e == nil && !rep.Pass {
			_ = json.NewEncoder(os.Stdout).Encode(rep)
			os.Exit(1)
		}
		out, err = rep, e
	case "memory-vectors":
		pack, fetched := "", len(args) == 0
		if !fetched {
			pack = args[0]
		} else if pack, err = fetchNavec(ctx, dir); err != nil {
			log.Fatal(err)
		}
		n, e := memory.BuildVectors(pack, dir)
		if fetched {
			_ = os.Remove(pack) // the 53 MB pack is not kept; the table is
		}
		if e == nil {
			e = st.Reindex(ctx)
		}
		out, err = map[string]any{"words": n, "pack_sha256": memory.NavecSHA256}, e
	case "memory-questions":
		if len(args) != 1 {
			log.Fatal("memory-questions FILE.json")
		}
		raw, e := os.ReadFile(args[0])
		if e != nil {
			log.Fatal(e)
		}
		sets := map[string][]string{}
		if e := json.Unmarshal(raw, &sets); e != nil {
			log.Fatal(e)
		}
		err = st.SetQuestions(ctx, sets)
		out = map[string]int{"sources": len(sets)}
	}
	var outcome runoutcome.Result
	switch r := out.(type) {
	case memory.SyncReport:
		outcome = r.Result
	case memory.InboxReport:
		outcome = r.Result
	case memory.ConsolidateReport:
		outcome = r.Result
	case memory.ImportReport:
		outcome = r.Result
	}
	if outcome.State != "" {
		if db, dbErr := statestore.Active(filepath.Dir(dir)); dbErr == nil && db != nil {
			now := time.Now()
			if recErr := runoutcome.Record(db, name, now.UTC().Format(time.RFC3339Nano), outcome, now); recErr != nil {
				log.Printf("record run outcome: %v", recErr)
			}
		} else if dbErr != nil {
			log.Printf("open run outcome state: %v", dbErr)
		}
		_ = json.NewEncoder(os.Stdout).Encode(out)
	}
	if err != nil {
		log.Fatal(err)
	}
	if outcome.State == "" {
		_ = json.NewEncoder(os.Stdout).Encode(out)
	}
}

// fetchNavec downloads the pinned navec pack next to the memory; BuildVectors
// checks its SHA-256.
func fetchNavec(ctx context.Context, dir string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, memory.NavecURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("navec download: %s", resp.Status)
	}
	file := filepath.Join(dir, "navec.tar")
	f, err := os.OpenFile(file, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	_, err = io.Copy(f, io.LimitReader(resp.Body, 64<<20))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return file, err
}

// issueRegistry is the server's card registry (issuetools.New StatePath).
const issueRegistry = "/var/lib/mcpshka/issues/issues.json"
