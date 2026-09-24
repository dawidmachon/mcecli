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
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/dawidmachon/mcecli/internal/output"
)

// queryFetchAll pages through the query collection and returns (items, apiCount).
// The server caps $pageSize at 25 (verified live 2026-09-16: $pageSize=400 →
// pageSize=25 in response), so iterate $page until collected >= API count or a
// page is empty. maxQueryPages guards against count drift mid-iteration.
// queryFetchAll pages the full query collection. countShift reports when
// the server's own count changed between pages of a single scan — results
// may then be inconsistent across pages.
func queryFetchAll(s *session, maxPages int) (items []any, count int, countShift string, err error) {
	const pageSize = 250 // server caps at 25; we only rely on len(items)
	var all []any
	firstCount := -1
	for page := 1; page <= maxPages; page++ {
		path := fmt.Sprintf("automation/v1/queries?$pageSize=%d&$page=%d", pageSize, page)
		_, resp, env2, _ := s.call(http.MethodGet, path, nil, nil)
		if env2 != nil {
			return nil, 0, "", fmt.Errorf("%s", string(resp))
		}
		m, ok := parseJSON(resp).(map[string]any)
		if !ok {
			return nil, 0, "", fmt.Errorf("unexpected response shape")
		}
		items, _ := m["items"].([]any)
		count := 0
		if f, ok := m["count"].(float64); ok {
			count = int(f)
		}
		if firstCount == -1 {
			firstCount = count
		} else if count != firstCount {
			countShift = fmt.Sprintf("server count changed mid-scan (%d → %d) — results may be inconsistent; re-run if unexpected", firstCount, count)
		}
		all = append(all, items...)
		if len(all) >= count || len(items) == 0 {
			return all, count, countShift, nil
		}
	}
	return all, firstCount, fmt.Sprintf("scan cap reached (%d pages)", maxPages), nil
}

// resolveQueryQID finds the queryDefinitionId for a query key by paging the
// full query collection and matching. The Query API requires GUIDs, not keys.
// (Was page-1-only — broke with >25 queries in context; caught live.)
func resolveQueryQID(s *session, key string) (string, error) {
	items, _, shift, err := queryFetchAll(s, 40)
	if err != nil {
		return "", err
	}
	_ = shift
	for _, it := range items {
		if im, ok := it.(map[string]any); ok {
			if k, _ := im["key"].(string); strings.EqualFold(k, key) {
				if qid, _ := im["queryDefinitionId"].(string); qid != "" {
					return qid, nil
				}
			}
		}
	}
	return "", fmt.Errorf("query with key '%s' not found in this context", key)
}

// queryPollInterval is how often we check a running query's status.
const queryPollInterval = 5 * time.Second

// queryMaxPoll is the maximum time to wait for a query to complete.
const queryMaxPoll = 5 * time.Minute

const usageQuery = `mcecli query — run and retrieve SQL-on-platform query results

  mcecli query list              — list saved queries (name, key, target DE, status)
  mcecli query run <key>        — run a saved query, poll until complete
               [--target DE]   override target DE key (query's default if omitted)
               [--poll SEC]    poll interval (default 5s, max 60s)
               [--timeout MIN] max wait time (default 5min)
               [--write --confirm] REQUIRED (running a query writes to target DE)
  mcecli query status <key>     — check if a query is running/finished
  mcecli query validate          — check SQL syntax WITHOUT creating/running
               --text "SQL"   (or @file.sql) — required
               --target KEY   target DE key (validated against it)
               [--category N] folder id (optional)
               --write REQUIRED (POST, though nothing is written)

Query API (automation/v1/queries): SFMC executes SQL server-side, writes
results to a target DE. Read the target DE afterwards with mcecli de rows.
This means millions of rows can be aggregated/filtered without client-side paging.
DANGER: --write --confirm required — query execution writes to the target DE.

Read-only commands (list, status) need no gate.
`

func cmdQuery(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageQuery)
		return exitUsage
	}
	switch args[0] {
	case "list":
		return queryList(args[1:], stdout, stderr)
	case "run":
		return queryRun(args[1:], stdout, stderr)
	case "status":
		return queryStatus(args[1:], stdout, stderr)
	case "validate":
		return queryValidate(args[1:], stdout, stderr)
	case "create":
		return queryCreate(args[1:], stdout, stderr)
	case "get":
		return queryGet(args[1:], stdout, stderr)
	case "update":
		return queryUpdate(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown 'query' subcommand %q\n\n%s", args[0], usageQuery)
		return exitUsage
	}
}

func queryList(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("query list", flag.ContinueOnError)
	var c common
	var search string
	var limit int
	var withText, full bool
	var pages int
	addCommon(fs, &c)
	fs.StringVar(&search, "search", "", "client-side filter: name/key/queryText contains")
	fs.IntVar(&limit, "limit", 0, "max rows after filtering (0 = all)")
	fs.BoolVar(&withText, "text", false, "include SQL text preview (first 80 chars)")
	fs.BoolVar(&full, "full", false, "include FULL queryText (implies --text)")
	fs.IntVar(&pages, "pages", 40, "max API pages to scan (25 rows/page)")
	if err := parseCmd(fs, args); err != nil {
		return exitUsage
	}

	s, env, code := newSession(&c)
	if env != nil {
		_ = output.Print(env, c.pretty, stdout)
		return code
	}

	items, apiCount, countShift, ferr := queryFetchAll(s, pages)
	if ferr != nil {
		e := output.Fail(0, ferr.Error(), "check query API access")
		_ = output.Print(e, c.pretty, stdout)
		return exitAPI
	}
	out := make([]map[string]string, 0, len(items))
	for _, item := range items {
		mi, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if search != "" {
			needle := strings.ToLower(search)
			hay := strings.ToLower(str(mi, "name") + " " + str(mi, "key") + " " + str(mi, "queryText"))
			if !strings.Contains(hay, needle) {
				continue
			}
		}
		if limit > 0 && len(out) >= limit {
			break
		}
		row := map[string]string{
			"name":   str(mi, "name"),
			"key":    str(mi, "key"),
			"status": str(mi, "status"),
			"target": str(mi, "targetKey"),
		}
		if ut := str(mi, "targetUpdateTypeName"); ut != "" {
			row["update"] = ut
		}
		if withText || full {
			qt := strings.ReplaceAll(str(mi, "queryText"), "\n", " ")
			if !full && len(qt) > 80 {
				qt = qt[:80] + "…"
			}
			row["text"] = qt
		}
		if row["target"] == "" {
			row["target"] = "(none)"
		}
		out = append(out, row)
	}

	e := output.OK(0, out)
	e.Count = apiCount
	switch {
	case countShift != "":
		e.Hint = countShift
	case len(items) >= pages*25:
		e.Hint = fmt.Sprintf("scan cap reached (%d pages ≈ %d rows) — raise --pages to scan further", pages, pages*25)
	case search != "" || limit > 0:
		e.Hint = fmt.Sprintf("filtered client-side: %d of %d match — --full shows complete SQL", len(out), apiCount)
	case apiCount > len(out):
		e.Hint = fmt.Sprintf("showing %d of %d — use --search to narrow", len(out), apiCount)
	}
	if len(items) == 0 {
		e.Hint = "no saved queries found in this context"
	}
	_ = output.Print(e, c.pretty, stdout)
	return exitOK
}

func queryStatus(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("query status", flag.ContinueOnError)
	var c common
	addCommon(fs, &c)
	if err := parseCmd(fs, args); err != nil || fs.NArg() != 1 {
		fmt.Fprint(stderr, "usage: mcecli query status <key>\n")
		return exitUsage
	}
	key := fs.Arg(0)

	s, env, code := newSession(&c)
	if env != nil {
		_ = output.Print(env, c.pretty, stdout)
		return code
	}

	qid, qerr := resolveQueryQID(s, key)
	if qerr != nil {
		e := output.Fail(0, qerr.Error(), "mcecli query list to see available queries")
		_ = output.Print(e, c.pretty, stdout)
		return exitAPI
	}
	_, resp, env2, _ := s.call(http.MethodGet, "automation/v1/queries/"+qid, nil, nil)
	if env2 != nil {
		_ = output.Print(env2, c.pretty, stdout)
		return exitAPI
	}

	var raw any
	if json.Unmarshal(resp, &raw) != nil {
		fmt.Fprintf(stderr, "query status: invalid JSON\n")
		return exitAPI
	}
	m := raw.(map[string]any)

	// The definition endpoint carries NO status field (verified live) —
	// running state comes from /actions/isrunning.
	_, resp2, env3, _ := s.call(http.MethodGet, "automation/v1/queries/"+qid+"/actions/isrunning", nil, nil)
	if env3 != nil {
		_ = output.Print(env3, c.pretty, stdout)
		return exitAPI
	}
	var run struct {
		IsRunning bool `json:"isRunning"`
	}
	_ = json.Unmarshal(resp2, &run)
	state := "not running (never started or finished)"
	if run.IsRunning {
		state = "running"
	}
	e := output.OK(0, map[string]any{
		"name":   str(m, "name"),
		"key":    str(m, "key"),
		"status": state,
		"target": str(m, "targetKey"),
	})
	if run.IsRunning {
		e.Hint = "query is still running — re-run mcecli query status " + key + " to check"
	} else {
		e.Hint = "not running — read results from target DE with mcecli de rows " + str(m, "targetKey") + " or start with: mcecli query run " + key + " --write --confirm"
	}
	_ = output.Print(e, c.pretty, stdout)
	return exitOK
}

func queryRun(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("query run", flag.ContinueOnError)
	var c common
	var targetKey, pollStr, timeoutStr string
	var write, confirm bool
	addCommon(fs, &c)
	fs.BoolVar(&write, "write", false, "confirm the write (explicit user approval required)")
	fs.BoolVar(&confirm, "confirm", false, "confirm DANGEROUS operation (query writes to target DE)")
	fs.StringVar(&targetKey, "target", "", "override target DE key")
	fs.StringVar(&pollStr, "poll", "", "poll interval in seconds (default 5)")
	fs.StringVar(&timeoutStr, "timeout", "", "max wait in minutes (default 5)")
	if err := parseCmd(fs, args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprint(stderr, usageQuery)
		return exitUsage
	}
	key := fs.Arg(0)
	if !write {
		e := output.Fail(0, "refusing query run without --write",
			"executing SQL writes to a target DE — get explicit user approval for THIS query, then retry with --write")
		_ = output.Print(e, c.pretty, stdout)
		return exitUsage
	}

	pollSec := 5
	if pollStr != "" {
		if n, err := strconv.Atoi(pollStr); err == nil && n > 0 && n <= 60 {
			pollSec = n
		}
	}
	timeoutMin := 5
	if timeoutStr != "" {
		if n, err := strconv.Atoi(timeoutStr); err == nil && n > 0 {
			timeoutMin = n
		}
	}
	maxWait := time.Duration(timeoutMin) * time.Minute

	s, env, code := newSession(&c)
	if env != nil {
		_ = output.Print(env, c.pretty, stdout)
		return code
	}

	qid, qerr := resolveQueryQID(s, key)
	if qerr != nil {
		e := output.Fail(0, qerr.Error(), "mcecli query list to see available queries")
		_ = output.Print(e, c.pretty, stdout)
		return exitAPI
	}

	// Fetch query definition to get target DE key.
	_, resp, env2, _ := s.call(http.MethodGet, "automation/v1/queries/"+qid, nil, nil)
	if env2 != nil {
		_ = output.Print(env2, c.pretty, stdout)
		return exitAPI
	}
	var qdef map[string]any
	if json.Unmarshal(resp, &qdef) != nil {
		fmt.Fprintf(stderr, "query run: invalid JSON reading query definition\n")
		return exitAPI
	}
	qName := str(qdef, "name")
	if targetKey != "" {
		// The start endpoint takes NO body — a target override would need a
		// PATCH of the saved definition. Refuse rather than silently run
		// against the original target (a lying flag is worse than no flag).
		e := output.Fail(0, fmt.Sprintf("--target is not supported by the start endpoint: %q would still run against its saved target %q", key, str(qdef, "targetKey")),
			"to change the target permanently: mcecli query update "+key+" --target "+str(qdef, "targetKey")+" --write --confirm")
		_ = output.Print(e, c.pretty, stdout)
		return exitUsage
	}
	qTarget := str(qdef, "targetKey")

	// Start the query with an EMPTY body (verified live 2026-09-16: JSON
	// body → silent failure; empty body → 200 "OK"). Target override happens
	// at query-definition level, not at start time.
	var startStatus int
	startStatus, resp, env2, code = s.call(http.MethodPost, "automation/v1/queries/"+qid+"/actions/start", nil, nil)
	if env2 != nil {
		_ = output.Print(env2, c.pretty, stdout)
		return code
	}
	if env2 == nil && (startStatus < 200 || startStatus > 299) {
		msg := strings.TrimSpace(string(resp))
		if m, ok := parseJSON(resp).(map[string]any); ok {
			msg = str(m, "message")
		}
		if msg == "" {
			msg = fmt.Sprintf("start returned HTTP %d", startStatus)
		}
		e := output.Fail(startStatus, "query start failed: "+msg, "check the query definition: mcecli query status "+key)
		_ = output.Print(e, c.pretty, stdout)
		return exitAPI
	}

	// Poll for completion via /actions/isrunning (the definition endpoint
	// carries NO status field — verified live; status lives only in the run
	// log, which can stay empty on success). First check fires immediately —
	// fast queries (a few seconds) shouldn't wait a full poll interval.
	poll := time.NewTicker(time.Duration(pollSec) * time.Second)
	defer poll.Stop()
	deadline := time.After(maxWait)
	ticks := make(chan struct{})
	go func() {
		ticks <- struct{}{} // immediate first check
		for range poll.C {
			ticks <- struct{}{}
		}
	}()

	var started bool
	sawRunning := false
	startedAt := time.Now()
	for {
		select {
		case <-ticks:
			if !started {
				started = true
				fmt.Fprintf(stderr, "Polling query %q (target: %s)…\n", qName, qTarget)
			}
			_, resp, env2, _ := s.call(http.MethodGet, "automation/v1/queries/"+qid+"/actions/isrunning", nil, nil)
			if env2 != nil {
				_ = output.Print(env2, c.pretty, stdout)
				return exitAPI
			}
			var st struct {
				IsRunning bool `json:"isRunning"`
			}
			if json.Unmarshal(resp, &st) != nil {
				continue
			}
			if st.IsRunning {
				sawRunning = true
				continue
			}
			// Race guard: right after start, SFMC may not have registered the
			// run yet (isrunning=false before it flips true). Only accept a
			// completion once we SAW it running, or after a 20s grace window.
			if !sawRunning && time.Since(startedAt) < 20*time.Second {
				continue
			}
			// Finished — check the run log for errors (empty on success).
			runErr := ""
			_, resp, env2, _ = s.call(http.MethodGet, "automation/v1/queries/"+qid+"/log", nil, nil)
			if env2 == nil {
				if m, ok := parseJSON(resp).(map[string]any); ok {
					if items, ok := m["items"].([]any); ok {
						for _, it := range items {
							if im, ok := it.(map[string]any); ok {
								if s2, _ := im["status"].(string); strings.EqualFold(s2, "Error") {
									runErr = str(im, "statusMessage")
									if runErr == "" {
										runErr = "query run errored (see run log)"
									}
								}
							}
						}
					}
				}
			}
			if runErr != "" {
				e := output.Fail(0, "query errored: "+runErr, "check the SQL: mcecli rest GET automation/v1/queries/"+qid+" --raw")
				_ = output.Print(e, c.pretty, stdout)
				return exitAPI
			}
			e := output.OK(0, map[string]any{
				"query":  qName,
				"key":    key,
				"status": "Complete",
				"target": qTarget,
			})
			e.Hint = "query complete — read results with: mcecli de rows " + qTarget + " (or your custom target)"
			_ = output.Print(e, c.pretty, stdout)
			return exitOK
		case <-deadline:
			e := output.Fail(0, fmt.Sprintf("query timed out after %d min — check status with: mcecli query status %s", timeoutMin, key), "timeout")
			_ = output.Print(e, c.pretty, stdout)
			return exitAPI
		}
	}
}

func str(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}

// queryValidate checks SQL syntax server-side WITHOUT creating or running
// anything. VERIFIED live 2026-09-16: POST /automation/v1/queries/actions/
// validate with {"Text": sql, "targetKey": de, "targetUpdateTypeId": 0} →
// {"queryValid":bool,"errors":[...],"warnings":[...]}. NOTE the field is
// "Text" here but "queryText" on create — SFMC inconsistency, tested both.
func queryValidate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("query validate", flag.ContinueOnError)
	var c common
	var text, target, category string
	addCommon(fs, &c)
	fs.StringVar(&text, "text", "", "SQL text to validate, or @file.sql")
	fs.StringVar(&target, "target", "", "target DE key (required)")
	fs.StringVar(&category, "category", "", "folder id (optional)")
	if err := parseCmd(fs, args); err != nil {
		return exitUsage
	}
	if text == "" || target == "" || fs.NArg() > 0 {
		fmt.Fprint(stderr, "usage: mcecli query validate --text \"SQL\" --target DE_KEY [--category N] --write\n")
		return exitUsage
	}

	bodyText := text
	if strings.HasPrefix(text, "@") {
		b, err := os.ReadFile(strings.TrimPrefix(text, "@"))
		if err != nil {
			e := output.Fail(0, err.Error(), "pass SQL inline or as @file.sql")
			_ = output.Print(e, c.pretty, stdout)
			return exitUsage
		}
		bodyText = string(b)
	}

	payload := map[string]any{
		"Text":               bodyText,
		"targetKey":          target,
		"targetUpdateTypeId": 0,
	}
	if category != "" {
		if n, err := strconv.Atoi(category); err == nil {
			payload["categoryId"] = n
		}
	}
	bodyBytes, _ := json.Marshal(payload)

	s, env, code := newSession(&c)
	if env != nil {
		_ = output.Print(env, c.pretty, stdout)
		return code
	}
	status, resp, env2, _ := s.call(http.MethodPost, "automation/v1/queries/actions/validate", bodyBytes, nil)
	if env2 != nil {
		_ = output.Print(env2, c.pretty, stdout)
		return exitAPI
	}
	if status < 200 || status > 299 {
		msg := strings.TrimSpace(string(resp))
		if m, ok := parseJSON(resp).(map[string]any); ok {
			msg = str(m, "message")
		}
		e := output.Fail(status, "validate failed: "+msg, "check --text and --target")
		_ = output.Print(e, c.pretty, stdout)
		return exitAPI
	}

	m, ok := parseJSON(resp).(map[string]any)
	if !ok {
		e := output.Fail(0, "unexpected validate response shape", string(resp))
		_ = output.Print(e, c.pretty, stdout)
		return exitAPI
	}
	valid, _ := m["queryValid"].(bool)
	errList, _ := m["errors"].([]any)
	warnList, _ := m["warnings"].([]any)
	e := output.OK(200, map[string]any{
		"queryValid": valid,
		"errors":     errList,
		"warnings":   warnList,
	})
	if !valid {
		e.Hint = "SQL is invalid — fix errors above; nothing was created or run"
	} else if len(warnList) > 0 {
		e.Hint = "SQL is valid (with warnings) — create with: mcecli rest POST automation/v1/queries --write --body @f.json (field name there is queryText, NOT Text)"
	} else {
		e.Hint = "SQL is valid — create with: mcecli rest POST automation/v1/queries --write --body @f.json (field name there is queryText, NOT Text)"
	}
	_ = output.Print(e, c.pretty, stdout)
	return exitOK
}

// queryCreate creates a saved query (gated write). Body shape verified live
// 2026-09-16: FLAT fields — name, key, description, queryText (NOT "Text"
// — that's the validate endpoint), targetKey, targetDescription,
// targetUpdateTypeId (0=Overwrite verified; 1=Update), categoryId REQUIRED.
func queryCreate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("query create", flag.ContinueOnError)
	var c common
	var text, target, name, desc, category, updateMode string
	var write, confirm bool
	addCommon(fs, &c)
	fs.StringVar(&text, "text", "", "SQL text, or @file.sql — required")
	fs.StringVar(&target, "target", "", "target DE key — required")
	fs.StringVar(&name, "name", "", "display name (defaults to key)")
	fs.StringVar(&desc, "description", "", "description")
	fs.StringVar(&category, "category", "", "folder id — required (find: mcecli folders --type queryactivity)")
	fs.StringVar(&updateMode, "update-mode", "overwrite", "overwrite | update (0=Overwrite verified live)")
	fs.BoolVar(&write, "write", false, "confirm the write")
	fs.BoolVar(&confirm, "confirm", false, "confirm DANGEROUS operation (saved query can write to its target)")
	if err := parseCmd(fs, args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprint(stderr, usageQuery)
		return exitUsage
	}
	key := fs.Arg(0)
	if text == "" || target == "" || category == "" {
		e := output.Fail(0, "--text, --target and --category are required",
			"mcecli folders --type queryactivity lists folder ids; validate SQL first with: mcecli query validate")
		_ = output.Print(e, c.pretty, stdout)
		return exitUsage
	}
	if !write || !confirm {
		e := output.Fail(0, "refusing query create without --write --confirm",
			"a saved query is a platform artifact that writes to its target DE when run — get explicit user approval, then retry")
		_ = output.Print(e, c.pretty, stdout)
		return exitUsage
	}

	bodyText := text
	if strings.HasPrefix(text, "@") {
		b, rerr := os.ReadFile(strings.TrimPrefix(text, "@"))
		if rerr != nil {
			e := output.Fail(0, rerr.Error(), "pass SQL inline or as @file.sql")
			_ = output.Print(e, c.pretty, stdout)
			return exitUsage
		}
		bodyText = string(b)
	}

	updateType := 0
	if strings.EqualFold(updateMode, "update") {
		updateType = 1
	} else if !strings.EqualFold(updateMode, "overwrite") {
		e := output.Fail(0, fmt.Sprintf("unknown --update-mode %q", updateMode), "overwrite | update")
		_ = output.Print(e, c.pretty, stdout)
		return exitUsage
	}

	displayName := name
	if displayName == "" {
		displayName = key
	}
	body := map[string]any{
		"name":               displayName,
		"key":                key,
		"description":        desc,
		"queryText":          bodyText,
		"targetKey":          target,
		"targetDescription":  "",
		"targetUpdateTypeId": updateType,
		"categoryId":         category,
	}
	bodyBytes, _ := json.Marshal(body)

	s, env, code := newSession(&c)
	if env != nil {
		_ = output.Print(env, c.pretty, stdout)
		return code
	}
	st, resp, env2, _ := s.call(http.MethodPost, "automation/v1/queries", bodyBytes, nil)
	if env2 != nil {
		_ = output.Print(env2, c.pretty, stdout)
		return exitAPI
	}
	if st < 200 || st > 299 {
		msg := strings.TrimSpace(string(resp))
		if m, ok := parseJSON(resp).(map[string]any); ok {
			if mstr := str(m, "message"); mstr != "" {
				msg = mstr
			}
		}
		e := output.Fail(st, "query create failed: "+msg,
			"categoryId required; field is queryText (validate uses Text); validate SQL first: mcecli query validate")
		_ = output.Print(e, c.pretty, stdout)
		return exitAPI
	}
	var created map[string]any
	_ = json.Unmarshal(resp, &created)
	if created == nil {
		created = map[string]any{}
	}
	e := output.OK(st, created)
	e.Hint = "run it: mcecli query run " + key + " --write --confirm — validate again anytime: mcecli query validate"
	_ = output.Print(e, c.pretty, stdout)
	return exitOK
}

// queryGet returns the full definition (queryText, categoryId, …) for a
// query key. GET /{id} 404s on the key — resolve key→queryDefinitionId first.
func queryGet(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("query get", flag.ContinueOnError)
	var c common
	addCommon(fs, &c)
	if err := parseCmd(fs, args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprint(stderr, "usage: mcecli query get <key>\n")
		return exitUsage
	}
	key := fs.Arg(0)

	s, env, code := newSession(&c)
	if env != nil {
		_ = output.Print(env, c.pretty, stdout)
		return code
	}
	qid, qerr := resolveQueryQID(s, key)
	if qerr != nil {
		e := output.Fail(0, qerr.Error(), "mcecli query list to see available queries")
		e.Hint = "mcecli query list to see available queries"
		_ = output.Print(e, c.pretty, stdout)
		return exitAPI
	}
	_, resp, env2, _ := s.call(http.MethodGet, "automation/v1/queries/"+qid, nil, nil)
	if env2 != nil {
		_ = output.Print(env2, c.pretty, stdout)
		return exitAPI
	}
	m, ok := parseJSON(resp).(map[string]any)
	if !ok {
		e := output.Fail(0, "unexpected response shape", "")
		_ = output.Print(e, c.pretty, stdout)
		return exitAPI
	}
	e := output.OK(200, m)
	e.Hint = "run: mcecli query run " + key + " --write --confirm"
	_ = output.Print(e, c.pretty, stdout)
	return exitOK
}

// queryUpdate patches a saved query definition (gated write).
// Only provided fields are sent. VERIFIED endpoint: PATCH
// /automation/v1/queries/{queryDefinitionId} (discovery; key must be
// resolved to qid first — GET /{key} 404s).
func queryUpdate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("query update", flag.ContinueOnError)
	var c common
	var text, target, name, desc, category, updateMode string
	var write, confirm bool
	addCommon(fs, &c)
	fs.StringVar(&text, "text", "", "new SQL text, or @file.sql")
	fs.StringVar(&target, "target", "", "new target DE key")
	fs.StringVar(&name, "name", "", "new display name")
	fs.StringVar(&desc, "description", "", "new description")
	fs.StringVar(&category, "category", "", "new folder id")
	fs.StringVar(&updateMode, "update-mode", "", "overwrite | update")
	fs.BoolVar(&write, "write", false, "confirm the write")
	fs.BoolVar(&confirm, "confirm", false, "confirm DANGEROUS operation (changes a saved query)")
	if err := parseCmd(fs, args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprint(stderr, "usage: mcecli query update <key> [--text …] [--target DE] [--category N] [--name N] --write --confirm\n")
		return exitUsage
	}
	key := fs.Arg(0)
	if !write || !confirm {
		e := output.Fail(0, "refusing query update without --write --confirm",
			"this permanently changes a saved query — retry with both flags")
		_ = output.Print(e, c.pretty, stdout)
		return exitUsage
	}

	body := map[string]any{}
	if text != "" {
		if strings.HasPrefix(text, "@") {
			b, rerr := os.ReadFile(strings.TrimPrefix(text, "@"))
			if rerr != nil {
				e := output.Fail(0, rerr.Error(), "pass SQL inline or as @file.sql")
				_ = output.Print(e, c.pretty, stdout)
				return exitUsage
			}
			text = string(b)
		}
		body["queryText"] = text // field name verified: create uses queryText, validate uses Text
	}
	if target != "" {
		body["targetKey"] = target
	}
	if name != "" {
		body["name"] = name
	}
	if desc != "" {
		body["description"] = desc
	}
	if category != "" {
		if n, err := strconv.Atoi(category); err == nil {
			body["categoryId"] = n
		}
	}
	if updateMode != "" {
		if strings.EqualFold(updateMode, "overwrite") {
			body["targetUpdateTypeId"] = 0
		} else if strings.EqualFold(updateMode, "update") {
			body["targetUpdateTypeId"] = 1
		}
	}
	if len(body) == 0 {
		e := output.Fail(0, "nothing to update", "use --text / --target / --category / --name / --description")
		_ = output.Print(e, c.pretty, stdout)
		return exitUsage
	}

	s, env, code := newSession(&c)
	if env != nil {
		_ = output.Print(env, c.pretty, stdout)
		return code
	}
	qid, qerr := resolveQueryQID(s, key)
	if qerr != nil {
		e := output.Fail(0, qerr.Error(), "mcecli query list to see available queries")
		_ = output.Print(e, c.pretty, stdout)
		return exitAPI
	}
	bodyBytes, _ := json.Marshal(body)
	st, resp, env2, _ := s.call(http.MethodPatch, "automation/v1/queries/"+qid, bodyBytes, nil)
	if env2 != nil {
		_ = output.Print(env2, c.pretty, stdout)
		return exitAPI
	}
	if st < 200 || st > 299 {
		msg := strings.TrimSpace(string(resp))
		if m, ok := parseJSON(resp).(map[string]any); ok {
			if mstr := str(m, "message"); mstr != "" {
				msg = mstr
			}
		}
		e := output.Fail(st, "query update failed: "+msg, "")
		_ = output.Print(e, c.pretty, stdout)
		return exitAPI
	}
	updated, _ := parseJSON(resp).(map[string]any)
	if updated == nil {
		updated = map[string]any{}
	}
	e := output.OK(st, updated)
	e.Hint = "verify: mcecli query get " + key + " — run: mcecli query run " + key + " --write --confirm"
	_ = output.Print(e, c.pretty, stdout)
	return exitOK
}
