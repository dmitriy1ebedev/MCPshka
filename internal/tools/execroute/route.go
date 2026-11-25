package execroute

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"mcpshka/internal/failure"
)

// FileList sends directory discovery to the native bounded fs_list
// instead of spawning ls repeatedly (MCP-242). This is a no-effect admission
// refusal: it does not use an exec_start job or consume a retry key.
func FileList(command string, args []string, workdir string) error {
	if filepath.Base(command) != "ls" {
		return nil
	}
	path := workdir
	for _, arg := range args {
		if arg == "--" {
			continue
		}
		if arg != "" && !strings.HasPrefix(arg, "-") {
			if filepath.IsAbs(arg) {
				path = arg
			} else {
				path = filepath.Join(workdir, arg)
			}
			break
		}
	}
	e := failure.NewHold(failure.PolicyDenied, "FILESYSTEM_ROUTE_FS_LIST", "mcpserver.exec_start",
		"directory listings use fs_list with typed path/depth/max_entries; ls via exec_start is not required", nil)
	e.NoEffect = true
	e.NextSafeAction = failure.Action{Tool: "fs_list", Arguments: map[string]any{"path": path, "depth": 1, "max_entries": 100}}
	return e
}

// FileContentSearch routes ordinary grep file searches to fs_find
// (MCP-245) before any process is started. Stream filters, multiple targets
// and unsupported grep syntax are explicitly NOT passed off as equivalent.
func FileContentSearch(command string, args []string, workdir string) error {
	if filepath.Base(command) != "grep" {
		return nil
	}
	positions := []string{}
	recursive, fixed, insensitive, supported, options := false, false, false, true, true
	for _, arg := range args {
		if options && arg == "--" {
			options = false
			continue
		}
		if options && strings.HasPrefix(arg, "--") {
			switch arg {
			case "--recursive":
				recursive = true
			case "--fixed-strings":
				fixed = true
			case "--ignore-case":
				insensitive = true
			case "--line-number", "--with-filename", "--no-filename", "--extended-regexp":
			default:
				supported = false
			}
			continue
		}
		if options && len(arg) > 1 && strings.HasPrefix(arg, "-") {
			for _, flag := range arg[1:] {
				switch flag {
				case 'r', 'R':
					recursive = true
				case 'F':
					fixed = true
				case 'i':
					insensitive = true
				case 'n', 'H', 'h', 'l', 'E':
				default:
					supported = false
				}
			}
			continue
		}
		positions = append(positions, arg)
	}
	// "grep pattern" without -r/-R reads stdin, not a directory.
	if len(positions) == 0 || len(positions) > 2 ||
		(len(positions) == 1 && !recursive) || !supported {
		e := failure.NewHold(failure.PolicyDenied, "FILE_SEARCH_ROUTE_NEEDS_PLAN", "mcpserver.exec_start",
			"grep variant is not equivalent to native fs_find: use tool_resolve and a bounded fs_find/code_run workflow; do not silently reinterpret stdin, flags, or multiple paths", nil)
		e.NoEffect = true
		e.NextSafeAction = failure.Action{Tool: "tool_resolve", Arguments: map[string]any{"goal": "search file contents or filter bounded text with fs_find or code_run"}}
		return e
	}
	pattern := positions[0]
	if pattern == "" {
		return failure.NewHold(failure.PolicyDenied, "FILE_SEARCH_EMPTY_PATTERN", "mcpserver.exec_start",
			"empty grep pattern requires an explicit bounded search plan", nil)
	}
	path := workdir
	if len(positions) == 2 {
		if filepath.IsAbs(positions[1]) {
			path = positions[1]
		} else {
			path = filepath.Join(workdir, positions[1])
		}
	}
	regex := !fixed
	if insensitive {
		if fixed {
			pattern = regexp.QuoteMeta(pattern)
		}
		pattern = "(?i)" + pattern
		regex = true
	}
	e := failure.NewHold(failure.PolicyDenied, "FILE_SEARCH_ROUTE_FS_FIND", "mcpserver.exec_start",
		"filesystem content search uses fs_find (RE2 regex, bounded results and paging); use the returned typed plan instead of spawning grep", nil)
	e.NoEffect = true
	e.NextSafeAction = failure.Action{Tool: "fs_find", Arguments: map[string]any{"path": path, "pattern": pattern, "regex": regex, "max_results": 100}}
	return e
}

// GitAdd avoids a mutable shell staging step on the AI development
// path (MCP-227). project_commit stages exactly listed files atomically under
// an explicit lease; do not fabricate a commit message or a lease here.
func GitAdd(command string, args []string) error {
	if filepath.Base(command) != "git" {
		return nil
	}
	i := 0
	for i < len(args) {
		switch {
		case args[i] == "-C" || args[i] == "-c" || args[i] == "--git-dir" || args[i] == "--work-tree":
			i += 2
		case strings.HasPrefix(args[i], "-C") || strings.HasPrefix(args[i], "-c") ||
			strings.HasPrefix(args[i], "--git-dir=") || strings.HasPrefix(args[i], "--work-tree="):
			i++
		default:
			goto commandFound
		}
	}
commandFound:
	if i >= len(args) || args[i] != "add" {
		return nil
	}
	e := failure.NewHold(failure.PolicyDenied, "GIT_ADD_ROUTE_PROJECT_COMMIT", "mcpserver.exec_start",
		"for AI code changes git add is redundant: project_commit stages only named files under an active lease and commits atomically; for stage-only intent resolve a native bounded staging operation instead of exec_start", nil)
	e.NoEffect = true
	e.NextSafeAction = failure.Action{Tool: "tool_resolve", Arguments: map[string]any{"goal": "project_commit atomic stage and commit selected files under a project lease; or native stage-only git operation"}}
	return e
}

// JQJSON replaces the common jq '.path' file.json lookup with a native
// read-only code_run over bounded fs_read, not a shell or a jq interpreter.
// Unsupported jq expressions and stdin are reported as a separate planning
// case: jq's full language is NOT claimed to be implemented (MCP-244).
func JQJSON(command string, args []string, workdir string) error {
	if filepath.Base(command) != "jq" {
		return nil
	}
	pos := []string{}
	supported := true
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") && len(arg) > 1 {
			switch arg {
			case "-r", "-c", "-cr", "-rc", "--raw-output", "--compact-output":
			default:
				supported = false
			}
			continue
		}
		pos = append(pos, arg)
	}
	keys := []any{}
	if supported && len(pos) == 2 {
		selector := pos[0]
		if selector != "." {
			selector = strings.TrimPrefix(selector, ".[")
			if selector != pos[0] {
				selector = "[" + selector
			}
			tokens := regexp.MustCompile(`\.[a-zA-Z_][a-zA-Z0-9_]*|\[[0-9]+\]`).FindAllString(selector, -1)
			if len(tokens) == 0 || strings.Join(tokens, "") != selector {
				supported = false
			}
			for _, token := range tokens {
				if token[0] == '.' {
					keys = append(keys, token[1:])
				} else {
					index, err := strconv.Atoi(token[1 : len(token)-1])
					if err != nil || index > 1000000 {
						supported = false
						break
					}
					keys = append(keys, index)
				}
			}
		}
	}
	if !supported || len(pos) != 2 {
		e := failure.NewHold(failure.PolicyDenied, "JSON_QUERY_NEEDS_NATIVE_PLAN", "mcpserver.exec_start",
			"full jq syntax or stdin streams cannot be translated losslessly; use bounded fs_read + code_run JSON parsing with explicit semantics, not exec_start", nil)
		e.NoEffect = true
		e.NextSafeAction = failure.Action{Tool: "tool_resolve", Arguments: map[string]any{"goal": "code_run read-only JSON.parse of fs_read data with explicit filtering"}}
		return e
	}
	path := pos[1]
	if !filepath.IsAbs(path) {
		path = filepath.Join(workdir, path)
	}
	keyJSON, _ := json.Marshal(keys)
	// All variable inputs are JSON-encoded literals, not executable JS source.
	pathJSON, _ := json.Marshal(path)
	script := "const path=" + string(pathJSON) + ";const keys=" + string(keyJSON) +
		";let content='';let cursor=0;let complete=false;" +
		"for(let i=0;i<16;i++){const r=mcp.call('fs_read',{path,cursor,limit:65536});" +
		"if(r.encoding!=='utf-8'||typeof r.text!=='string')throw Error('JSON must be UTF-8');" +
		"content+=r.text;if(r.next_cursor>=r.total_bytes){complete=true;break;}" +
		"if(!Number.isFinite(r.next_cursor)||r.next_cursor<=cursor)throw Error('Invalid file cursor');cursor=r.next_cursor;}" +
		"if(!complete)throw Error('JSON exceeds bounded 1 MiB read');" +
		"let value=JSON.parse(content);for(const k of keys){if(value==null||typeof value!=='object'||" +
		"!Object.prototype.hasOwnProperty.call(value,k))return null;value=value[k];}" +
		"return value===undefined?null:value;"
	e := failure.NewHold(failure.PolicyDenied, "JSON_QUERY_ROUTE_CODE_RUN", "mcpserver.exec_start",
		"simple JSON field extraction uses read-only code_run/fs_read (max 1 MiB, not jq formatting); no jq subprocess started", nil)
	e.NoEffect = true
	e.NextSafeAction = failure.Action{Tool: "code_run", Arguments: map[string]any{"script": script, "max_calls": 16, "timeout_seconds": 20}}
	return e
}
