// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/dawidmachon/mcecli/internal/output"
)

const usageRest = `mcecli rest — generic REST passthrough to the profile's REST host

Usage:
  mcecli rest <METHOD> <path> [--body <json|@file|->] [--header "K: V"]...
           [--raw] [--fields a,b] [--write] [--confirm] [--profile P --bu B]

<path> is relative to https://<subdomain>.rest.marketingcloudapis.com/
(absolute URLs are accepted only if they match the profile's REST host).

WRITE SAFETY (hard rule): any method other than GET requires --write;
DELETE additionally requires --confirm. Never pass them without the user's
explicit approval of that exact operation.

Examples:
  mcecli rest GET data/v1/customobjectdata/key/MyDE/rowset?page=1
  mcecli rest GET interaction/v1/journeys --fields name,status
  mcecli rest POST sms/v1/messageContact --body @payload.json --write
  mcecli rest POST hub/v1/dataevents/key:scratch_de/rowset --write --body '[{"keys":{"RowId":"row-1"},"values":{"Note":"x"}}]'
     (SYNC insert; 400 "Primary key ... does not exist" means the row ALREADY exists —
      use the async PUT above to update; note key:COLON form)

Verified recipes (bodies proven live on this platform):
  1) upsert one DE row (idempotent, async 202):
       mcecli de add scratch_de --data '{"RowId":"row-9","Note":"hi"}' --write
  2) raw upsert (flat body, GUID id — key: paths fail on fresh DEs):
       mcecli rest PUT data/v1/async/dataExtensions/{guid}/rows --write --body '{"items":[{"RowId":"v"}]}'
       (resolve guid: mcecli de list --search <name> --fields id)
  3) sync insert (errors if PK already exists):
       mcecli rest POST hub/v1/dataevents/key:{customerKey}/rowset --write --body '[{"keys":{"PK":"v"},"values":{"Field":"x"}}]'
  Body files: --body @file.json or --body @- (stdin) or inline JSON.
`

func cmdRest(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageRest)
		return exitUsage
	}
	fs := flag.NewFlagSet("rest", flag.ContinueOnError)
	var c common
	var bodyArg string
	var headers multiFlag
	var queryArg string
	var pageArg, sizeArg int
	var allPages bool
	addCommon(fs, &c)
	addProjection(fs, &c)
	fs.BoolVar(&c.write, "write", false, "allow non-GET methods (user approval required)")
	fs.BoolVar(&c.confirm, "confirm", false, "confirm DANGEROUS operations (sends, journey lifecycle, DELETE, cleardata, overwrites)")
	fs.StringVar(&bodyArg, "body", "", "JSON body: inline, @file, or - (stdin)")
	fs.Var(&headers, "header", `extra header "Key: value" (repeatable)`)
	fs.StringVar(&queryArg, "query", "", "query string appended to the path (shell-safe): '$pageSize=500&$page=2'")
	fs.IntVar(&pageArg, "page", 0, "set $page (GET)")
	fs.IntVar(&sizeArg, "size", 0, "set $pageSize (GET)")
	fs.BoolVar(&allPages, "all-pages", false, "GET: follow $page until count reached; emit items as NDJSON")
	help := addHelp(fs)
	if err := parseCmd(fs, args); err != nil {
		return exitUsage
	}
	if *help {
		fmt.Fprint(stdout, usageRest)
		return exitOK
	}
	rest := fs.Args()
	if len(rest) < 2 {
		fmt.Fprint(stderr, usageRest)
		return exitUsage
	}
	method := strings.ToUpper(rest[0])
	path := rest[1]

	if method != http.MethodGet && method != http.MethodHead {
		if !c.write {
			e := output.Fail(0, fmt.Sprintf("refusing %s without --write", method),
				"writes change data: get explicit user approval for THIS call, then retry with --write")
			_ = output.Print(e, c.pretty, stdout)
			return exitUsage
		}
		if danger, reason := classifyWrite(method, path); danger && !c.confirm {
			e := output.Fail(0, fmt.Sprintf("refusing DANGEROUS operation without --confirm: %s", reason),
				"this can send communications, alter runtime state, or destroy data — explicit user approval required for THIS exact call, then retry with --write --confirm")
			_ = output.Print(e, c.pretty, stdout)
			return exitUsage
		}
	}

	body, err := loadBody(bodyArg)
	if err != nil {
		e := output.Fail(0, err.Error(), "body must be valid JSON, @path/to/file.json, or - for stdin")
		_ = output.Print(e, c.pretty, stdout)
		return exitUsage
	}

	s, env, code := newSession(&c)
	if env != nil {
		_ = output.Print(env, c.pretty, stdout)
		return code
	}
	full, err := resolvePath(s.res, path)
	if err != nil {
		_ = output.Print(output.Fail(0, err.Error(), ""), c.pretty, stdout)
		return exitUsage
	}

	// shell-safe query-string handling (GET): --query/--page/--size assemble
	// the URL so agents never fight ?/&/$ shell quoting
	if method == http.MethodGet {
		u, perr := url.Parse(full)
		if perr != nil {
			e := output.Fail(0, perr.Error(), "check the path")
			_ = output.Print(e, c.pretty, stdout)
			return exitUsage
		}
		q := u.Query()
		if queryArg != "" {
			for _, kv := range strings.Split(queryArg, "&") {
				if kv == "" {
					continue
				}
				k, v, _ := strings.Cut(kv, "=")
				q.Set(strings.TrimPrefix(k, "$"), v)
			}
		}
		if pageArg > 0 {
			q.Set("$page", strconv.Itoa(pageArg))
		}
		if sizeArg > 0 {
			q.Set("$pageSize", strconv.Itoa(sizeArg))
		}
		u.RawQuery = q.Encode()
		full = u.String()
	}

	// DELETE auto-snapshot: capture current state so rollback stays possible
	undoDir := ""
	if method == http.MethodDelete {
		snapSession, senv, scode := newSession(&c)
		if senv == nil && scode == exitOK {
			undoDir = snapSession.undoSnapshot(method, full, path, &c)
		}
	}

	// --all-pages: follow $page until the envelope count is reached; emit
	// items as NDJSON (piping without jq). GET-only.
	if allPages {
		if method != http.MethodGet {
			e := output.Fail(0, "--all-pages is GET-only", "")
			_ = output.Print(e, c.pretty, stdout)
			return exitUsage
		}
		u, _ := url.Parse(full)
		q := u.Query()
		if !q.Has("$pageSize") {
			q.Set("$pageSize", "500")
		}
		total, seen := -1, 0
		for page := 1; page <= 400; page++ {
			q.Set("$page", strconv.Itoa(page))
			u.RawQuery = q.Encode()
			st, resp2, env3, _ := s.call(http.MethodGet, u.String(), nil, nil)
			if env3 != nil {
				_ = output.Print(env3, c.pretty, stdout)
				return exitAPI
			}
			if st >= 400 {
				e := errorEnvelope(st, resp2, http.MethodGet)
				e.Hint = "page scan stopped on error"
				_ = output.Print(e, c.pretty, stdout)
				return exitAPI
			}
			m, ok := parseJSON(resp2).(map[string]any)
			if !ok {
				e := output.Fail(0, "response is not a JSON object — --all-pages needs {items,count} envelopes", "")
				_ = output.Print(e, c.pretty, stdout)
				return exitAPI
			}
			if f, ok := m["count"].(float64); ok {
				total = int(f)
			}
			items, _ := m["items"].([]any)
			for _, it := range items {
				line, _ := json.Marshal(it)
				_, _ = stdout.Write(line)
				_, _ = stdout.Write([]byte("\n"))
				seen++
			}
			if len(items) == 0 || (total >= 0 && seen >= total) {
				break
			}
		}
		fmt.Fprintf(stderr, "NOTE: %d items across pages (server count: %d)\n", seen, total)
		return exitOK
	}

	start := time.Now()
	status, resp, env2, code := s.call(method, full, body, headers.headers())
	s.journalWriteSnap(&c, method, full, status, body, time.Since(start), "", "", undoDir)
	if undoDir != "" {
		fmt.Fprintf(stderr, "NOTE: before-image captured at %s (rollback: re-create with the saved JSON)\n", undoDir)
	}
	if env2 != nil {
		_ = output.Print(env2, c.pretty, stdout)
		return code
	}
	if status >= 400 {
		_ = output.Print(errorEnvelope(status, resp, method), c.pretty, stdout)
		return exitAPI
	}
	if c.raw {
		_, _ = stdout.Write(resp)
		fmt.Fprintln(stdout)
		return exitOK
	}
	e := envelopeFor(status, resp)
	e.Data = output.Project(e.Data, splitFields(c.fields))
	_ = output.Print(e, c.pretty, stdout)
	return exitOK
}
