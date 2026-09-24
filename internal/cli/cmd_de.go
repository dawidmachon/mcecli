// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dawidmachon/mcecli/internal/auth"
	"github.com/dawidmachon/mcecli/internal/config"
	"github.com/dawidmachon/mcecli/internal/output"
	"github.com/dawidmachon/mcecli/internal/soap"
)

const usageDE = `mcecli de — data extension commands (list/get/rows/dump are reads; add is a gated write)

  mcecli de list  --search NAME [--category ID] [--page N --size N] [--fields f1,f2]
               --search is REQUIRED (no plain listing exists in the API).
  mcecli de get   <key>            — definition + field schema (resolves key via search)
  mcecli de rows  <key|name> [--page N --size N] [--fields f1,f2] [--next PATH]
               paging is token-based; the envelope's "next" carries the
               continuation path — pass it back with --next.
               Accepts customerKey OR name (auto-resolves).
  mcecli de dump  <key|name> [--out DIR] [--refresh] — all rows -> local NDJSON
  mcecli de add   <key|name> --data '{...}' --write   — upsert ONE row (async)
  mcecli de add   <key|name> --data @rows.ndjson --write [--batch-size 200]
               file rows: NDJSON lines, JSON array, or {"items":[...]} —
               each row is a FLAT object; chunked async upserts
  mcecli de find  <key|name>     — search ALL configured BUs + account level
               (DEs are per-BU; this locates which context has the DE)

Endpoint evidence: docs/dev/endpoint-notes.md
`

func cmdDE(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageDE)
		return exitUsage
	}
	switch args[0] {
	case "list":
		return deList(args[1:], stdout, stderr)
	case "get":
		return deGet(args[1:], stdout, stderr)
	case "rows":
		return deRows(args[1:], stdout, stderr)
	case "dump":
		return deDump(args[1:], stdout, stderr)
	case "add":
		return deAdd(args[1:], stdout, stderr)
	case "create":
		return cmdDECreate(args[1:], stdout, stderr)
	case "field":
		return cmdDEField(args[1:], stdout, stderr)
	case "diff":
		return deDiff(args[1:], stdout, stderr)
	case "find":
		return deFind(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usageDE)
		return exitOK
	default:
		fmt.Fprintf(stderr, "unknown 'de' subcommand %q\n\n%s", args[0], usageDE)
		return exitUsage
	}
}

func deList(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("de list", flag.ContinueOnError)
	var c common
	var search, category string
	addCommon(fs, &c)
	addPaging(fs, &c)
	addProjection(fs, &c)
	fs.StringVar(&search, "search", "", "search term — REQUIRED ($search: matches name/key/description)")
	fs.StringVar(&search, "contains", "", "alias for --search")
	fs.StringVar(&category, "category", "", "category (folder) id instead of --search")
	help := addHelp(fs)
	if err := parseCmd(fs, args); err != nil {
		return exitUsage
	}
	if *help {
		fmt.Fprint(stdout, usageDE)
		return exitOK
	}
	if search == "" && category == "" {
		e := output.Fail(0, "customObjects requires $search or categoryId",
			"mcecli de list --search <term>  (or --category <id>)")
		_ = output.Print(e, c.pretty, stdout)
		return exitUsage
	}

	u := url.URL{Path: "data/v1/customObjects"}
	q := url.Values{}
	if search != "" {
		q.Set("$search", search)
	}
	if category != "" {
		q.Set("categoryId", category)
	}
	if c.page > 0 {
		q.Set("$page", strconv.Itoa(c.page))
	}
	if c.size > 0 {
		q.Set("$pageSize", strconv.Itoa(c.size))
	}
	u.RawQuery = q.Encode()

	e, code := doGet(&c, u.String(), "data_extensions_read")
	if code != exitOK || e == nil {
		if e != nil {
			_ = output.Print(e, c.pretty, stdout)
		}
		return code
	}
	applyItemsEnvelope(e)
	// transparency: this endpoint may ignore $pageSize — surface it
	if arr, ok := e.Data.([]any); ok && c.size > 0 && len(arr) > c.size {
		e.Hint = fmt.Sprintf("server returned %d rows despite --size %d ($pageSize not honored by this endpoint)", len(arr), c.size)
	}
	if e.Count == 0 {
		e.Hint = "no matches in this context — DEs are per-BU: mcecli bu discover shows reachable BUs, mcecli de find <name> searches them all"
	}
	e.Data = output.Project(e.Data, splitFields(c.fields))
	_ = output.Print(e, c.pretty, stdout)
	return exitOK
}

func deGet(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("de get", flag.ContinueOnError)
	var c common
	help := addHelp(fs)
	addCommon(fs, &c)
	addProjection(fs, &c)
	if err := parseCmd(fs, args); err != nil {
		return exitUsage
	}
	if *help {
		fmt.Fprint(stdout, usageDE)
		return exitOK
	}
	if fs.NArg() != 1 {
		fmt.Fprint(stderr, usageDE)
		return exitUsage
	}

	s, env, code := newSession(&c)
	if env != nil {
		_ = output.Print(env, c.pretty, stdout)
		return code
	}
	m, denv := resolveDE(s, fs.Arg(0))
	if denv != nil {
		_ = output.Print(denv, c.pretty, stdout)
		return exitAPI
	}

	_, defResp, env2, code := s.call(http.MethodGet, "data/v1/customObjects/"+url.PathEscape(m.ID), nil, nil)
	if env2 != nil {
		_ = output.Print(env2, c.pretty, stdout)
		return code
	}
	_, fieldsResp, env2, code := s.call(http.MethodGet, "data/v1/customObjects/"+url.PathEscape(m.ID)+"/fields", nil, nil)
	if env2 != nil {
		_ = output.Print(env2, c.pretty, stdout)
		return code
	}

	defRaw, fRaw := parseJSON(defResp), parseJSON(fieldsResp)
	data := map[string]any{"key": m.Key, "name": m.Name, "id": m.ID}
	if dm, ok := defRaw.(map[string]any); ok {
		data["definition"] = dm
	} else {
		data["definition"] = defRaw
	}
	if fm, ok := fRaw.(map[string]any); ok {
		if fl, ok := fm["fields"].([]any); ok {
			data["fields"] = fl
			e := output.OK(200, data)
			e.Count = len(fl)
			// --fields projects the FIELD schema, not the wrapper
			if pf := splitFields(c.fields); len(pf) > 0 {
				data["fields"] = output.Project(fl, pf)
				delete(data, "definition")
			}
			_ = output.Print(e, c.pretty, stdout)
			return exitOK
		}
	}
	data["fields"] = fRaw
	e := output.OK(200, data)
	_ = output.Print(e, c.pretty, stdout)
	return exitOK
}

func deRows(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("de rows", flag.ContinueOnError)
	var c common
	var next string
	addCommon(fs, &c)
	addPaging(fs, &c)
	addProjection(fs, &c)
	fs.StringVar(&next, "next", "", "continuation path from envelope's \"next\" (token-based paging)")
	whereP := addWhere(fs)
	help := addHelp(fs)
	if err := parseCmd(fs, args); err != nil {
		return exitUsage
	}
	if *help {
		fmt.Fprint(stdout, usageDE)
		return exitOK
	}
	if next == "" && fs.NArg() != 1 {
		fmt.Fprint(stderr, usageDE)
		return exitUsage
	}

	var path string
	if next != "" {
		path = strings.TrimPrefix(next, "/")
		if !strings.HasPrefix(path, "data/v1/") && !strings.Contains(path, "/customobjectdata/") {
			path = "data/" + strings.TrimPrefix(path, "/")
		}
	} else {
		path = rowsetPathForKey(fs.Arg(0), c.page, c.size)
	}

	// --where switches to SOAP filtered retrieve for huge DEs
	if *whereP != "" && next == "" {
		key := fs.Arg(0)
		s, senv, scode := newSession(&c)
		if senv != nil {
			_ = output.Print(senv, c.pretty, stdout)
			return scode
		}
		filters := parseWhere(*whereP)
		if filters == nil {
			_ = output.Print(output.Fail(0, "cannot parse --where",
				`format: --where "field=value[,field2=value2]"`), c.pretty, stdout)
			return exitUsage
		}
		var cols []string
		if pf := splitFields(c.fields); len(pf) > 0 {
			cols = pf
		}
		resultRows, err := soap.RetrieveFilteredRows(context.Background(),
			s.res.SoapURL(), s.tok.AccessToken, key, cols, filters)
		if err != nil {
			_ = output.Print(output.Fail(0, err.Error(),
				"SOAP filtered retrieve requires the DE to have filterable columns; try de rows without --where"), c.pretty, stdout)
			return exitAPI
		}
		data := make([]any, len(resultRows))
		for i, r := range resultRows {
			m := map[string]any{}
			for k, v := range r {
				m[k] = v
			}
			data[i] = m
		}
		e := output.OK(200, data)
		e.Count = len(data)
		e.Hint = fmt.Sprintf("SOAP filtered retrieve: %d rows (server-side filtering)", len(data))
		_ = output.Print(e, c.pretty, stdout)
		return exitOK
	}

	e, code := doGet(&c, path, "data_extensions_read")
	if code == exitAPI && e != nil && e.Error != nil && strings.Contains(e.Error.Message, "cannot be retrieved for key") {
		// customerKey mismatch (name given, or fresh DE): resolve via $search -> GUID
		if s, env, ccode := newSession(&c); env == nil && ccode == exitOK {
			_, sresp, senv2, _ := s.call(http.MethodGet,
				"data/v1/customObjects?$search="+url.QueryEscape(fs.Arg(0)), nil, nil)
			matches := []deMatch(nil)
			if senv2 == nil {
				matches = findDEMatches(sresp, fs.Arg(0))
			}
			switch {
			case len(matches) == 1 && matches[0].Key != fs.Arg(0):
				// exactly one DE named like the arg (and arg wasn't its key): use its key
				e, code = doGet(&c, rowsetPathForKey(matches[0].Key, c.page, c.size))
			case len(matches) > 1:
				e = ambiguousDEErr(fs.Arg(0), matches)
			default:
				e.Hint = "not found with the current token account — check mcecli status"
			}
		}
	}
	if code != exitOK || e == nil {
		if e != nil {
			_ = output.Print(e, c.pretty, stdout)
		}
		return code
	}
	normalizeRowset(e)
	e.Data = output.Project(e.Data, splitFields(c.fields))
	_ = output.Print(e, c.pretty, stdout)
	return exitOK
}

// addWhere registers the --where flag for SOAP filtered retrieves.
func addWhere(fs *flag.FlagSet) *string {
	s := fs.String("where", "", "SOAP filtered retrieve: \"field=value[,field2=value2]\" (for huge DEs)")
	return s
}

// parseWhere parses "field=value,field2=value2" into a map.
func parseWhere(s string) map[string]string {
	out := map[string]string{}
	for _, pair := range strings.Split(s, ",") {
		if i := strings.Index(pair, "="); i > 0 {
			out[strings.TrimSpace(pair[:i])] = strings.TrimSpace(pair[i+1:])
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// rowsetPathForKey builds the rowset path for a customerKey with paging.
func rowsetPathForKey(key string, page, size int) string {
	return "data/v1/customobjectdata/key/" + url.PathEscape(key) + "/rowset" + rowsetQuery(page, size)
}

func rowsetQuery(page, size int) string {
	q := url.Values{}
	if page > 0 {
		q.Set("$page", strconv.Itoa(page))
	}
	if size > 0 {
		q.Set("$pageSize", strconv.Itoa(size))
	}
	if len(q) == 0 {
		return ""
	}
	return "?" + q.Encode()
}

// deAdd upserts one or more rows (async, chunked). Accepts key OR name;
// bodies are FLAT JSON rows — the API matches PK fields by name. Gated: --write.
func deAdd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("de add", flag.ContinueOnError)
	var c common
	var dataArg string
	var batchSize int
	addCommon(fs, &c)
	fs.BoolVar(&c.write, "write", false, "confirm the write (explicit user approval required)")
	fs.StringVar(&dataArg, "data", "", `row(s): '{"PK":"v"}' | array | @file.ndjson | @file.json | @- (stdin)`)
	fs.IntVar(&batchSize, "batch-size", 200, "rows per async request (chunked)")
	fs.BoolVar(&c.noSnapshot, "no-snapshot", false, "skip before-image reads (faster on huge DEs; journal records the skip)")
	help := addHelp(fs)
	if err := parseCmd(fs, args); err != nil {
		return exitUsage
	}
	if *help {
		fmt.Fprint(stdout, usageDE)
		return exitOK
	}
	// stdin/stdout body forms may land after positionals: "mcecli de add K --data @-"
	pos := fs.Args()
	if len(pos) >= 2 && (pos[1] == "-" || pos[1] == "@-") && dataArg == "" {
		dataArg = pos[1]
		pos = pos[:1]
	}
	if fs.NArg() != 1 || dataArg == "" {
		fmt.Fprintln(stderr, `usage: mcecli de add <key|name> --data '{"PK":"v",...}' --write`)
		return exitUsage
	}
	if !c.write {
		e := output.Fail(0, "refusing row write without --write",
			"get explicit user approval for THIS write, then retry with --write")
		_ = output.Print(e, c.pretty, stdout)
		return exitUsage
	}
	rowsIn, rerr := parseRowsArg(dataArg)
	if rerr != nil || len(rowsIn) == 0 {
		e := output.Fail(0, "--data must contain at least one flat row",
			`one: '{"RowId":"row-9","Note":"hi"}' · many: @rows.ndjson (one flat JSON object per line) or a JSON array — PK field required in every row`)
		_ = output.Print(e, c.pretty, stdout)
		return exitUsage
	}
	key := fs.Arg(0)

	s, env, code := newSession(&c)
	if env != nil {
		_ = output.Print(env, c.pretty, stdout)
		return code
	}
	m, denv := resolveDE(s, key)
	if denv != nil {
		_ = output.Print(denv, c.pretty, stdout)
		return exitAPI
	}
	custKey := m.Key

	// PK fields of this DE (needed for keyed before-image reads)
	pkFields := []string{}
	if fldResp, ok := fetchFields(s, m.ID); ok {
		pkFields = pkNamesFromFields(fldResp)
	}

	if batchSize <= 0 {
		batchSize = 200
	}
	total, chunks := 0, 0
	snapshotState := "n/a"
	lastReqID := ""
	for start := 0; start < len(rowsIn); start += batchSize {
		stop := start + batchSize
		if stop > len(rowsIn) {
			stop = len(rowsIn)
		}
		// before-images: per row, keyed SOAP read (cheap server-side filter)
		// unless skipped. Stored in the journal-referenced undo dir.
		var undoDir string
		if !c.noSnapshot && len(pkFields) > 0 {
			undoDir = newUndoDir(s.res.Name, "DEUPSERT-"+sanitizeBUName(custKey))
		}
		var beforeRows []map[string]any
		if undoDir != "" {
			for _, rowAny := range rowsIn[start:stop] {
				row, ok := rowAny.(map[string]any)
				if !ok {
					continue
				}
				pk := map[string]string{}
				for _, f := range pkFields {
					if v, ok := row[f]; ok {
						pk[f] = fmt.Sprint(v)
					}
				}
				if len(pk) == 0 {
					continue
				}
				if before, err := soap.RetrieveDERows(context.Background(),
					s.res.SoapURL(), s.tok.AccessToken, custKey, pk); err == nil && len(before) > 0 {
					beforeRows = append(beforeRows, mergePKValues(pk, before[0]))
				}
			}
			if len(beforeRows) > 0 {
				writeBeforeRows(undoDir, beforeRows)
			}
		}
		switch {
		case c.noSnapshot:
			snapshotState = "skipped(--no-snapshot)"
		case undoDir != "":
			snapshotState = fmt.Sprintf("captured(%d before-rows)", len(beforeRows))
		case len(pkFields) == 0:
			snapshotState = "n/a(no PK fields)"
		default:
			snapshotState = "skipped(rows had no PK values)"
		}

		body, _ := json.Marshal(map[string]any{"items": rowsIn[start:stop]})
		chunkPath := "data/v1/async/dataextensions/key:" + url.PathEscape(custKey) + "/rows"
		chunkURL := s.res.RestURL() + "/" + chunkPath
		start0 := time.Now()
		status, resp, env2, code := s.call(http.MethodPut, chunkPath, body, nil)
		chunkReqID := ""
		if m2, ok := parseJSON(resp).(map[string]any); ok {
			if r, ok := m2["requestId"].(string); ok {
				chunkReqID = r
			}
		}
		s.journalWriteSnap(&c, http.MethodPut, chunkURL, status, body, time.Since(start0), chunkReqID, undoDir, snapshotState)
		if env2 != nil {
			_ = output.Print(env2, c.pretty, stdout)
			return code
		}
		if status >= 400 {
			e := errorEnvelope(status, resp, http.MethodPut)
			e.Hint = fmt.Sprintf("chunk %d (rows %d-%d) failed — check field names: mcecli de get %s --fields name",
				chunks+1, start+1, stop, key)
			_ = output.Print(e, c.pretty, stdout)
			return exitAPI
		}
		if chunkReqID != "" {
			lastReqID = chunkReqID
		}
		total += stop - start
		chunks++
	}
	e := output.OK(202, map[string]any{
		"accepted": true, "mode": "async-upsert", "de": key,
		"rows": total, "chunks": chunks, "request_id": lastReqID,
		"status_path": "data/v1/async/" + lastReqID + "/status",
		"snapshot":    snapshotState,
	})
	if adv := scopeAdvisory(s.tok, "data_extensions_write"); adv != "" {
		e.Hint = adv
	}
	_ = output.Print(e, c.pretty, stdout)
	return exitOK
}

// parseRowsArg resolves --data: flat row, JSON array, {"items":[]},
// @file.ndjson (one flat object per line), @file.json, or @- (stdin).
func parseRowsArg(arg string) ([]any, error) {
	var raw []byte
	switch {
	case arg == "":
		return nil, errors.New("empty --data")
	case arg == "-" || arg == "@-":
		b, err := io.ReadAll(stdin())
		if err != nil {
			return nil, err
		}
		raw = b
	case strings.HasPrefix(arg, "@"):
		b, err := osReadFile(strings.TrimPrefix(arg, "@"))
		if err != nil {
			return nil, err
		}
		raw = b
	default:
		raw = []byte(arg)
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil, errors.New("empty --data")
	}
	var parsed any
	if err := json.Unmarshal([]byte(trimmed), &parsed); err == nil {
		switch v := parsed.(type) {
		case []any:
			return v, nil
		case map[string]any:
			if items, ok := v["items"].([]any); ok {
				return items, nil
			}
			return []any{v}, nil
		}
	}
	// NDJSON fallback: one flat object per line
	var out []any
	for _, line := range strings.Split(trimmed, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	if len(out) == 0 {
		return nil, errors.New("no rows parsed")
	}
	return out, nil
}

// deFind locates a DE across every context the profile can reach:
// account-level plus each configured BU. DEs are per-BU, so the same name
// can exist in several contexts (or none). Read-only; tokens are cached.
func deFind(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("de find", flag.ContinueOnError)
	var c common
	addCommon(fs, &c)
	if err := parseCmd(fs, args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: mcecli de find <key|name> [--profile P]")
		return exitUsage
	}
	arg := fs.Arg(0)

	s, env, code := newSession(&c)
	if env != nil {
		_ = output.Print(env, c.pretty, stdout)
		return code
	}

	type target struct {
		label, mid string
	}
	targets := []target{{"(account)", ""}}
	for name, e := range s.res.Profile.BUs {
		targets = append(targets, target{name, e.MID})
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].label < targets[j].label })

	matches := []any{}
	var unreachable, containsOnly []string
	for _, tg := range targets {
		tr := *s.res
		tr.MID = tg.mid
		tr.BUName = tg.label
		tok, terr := auth.Get(&tr, false)
		if terr != nil {
			unreachable = append(unreachable, tg.label)
			continue
		}
		sub := session{res: &tr, tok: tok}
		st, resp, env2, _ := sub.call(http.MethodGet,
			"data/v1/customObjects?$search="+url.QueryEscape(arg)+"&$pageSize=25&$fields=id,name,key,rowCount", nil, nil)
		if env2 != nil || st >= 400 {
			unreachable = append(unreachable, tg.label)
			continue
		}
		if items, ok := parseJSON(resp).(map[string]any)["items"].([]any); ok {
			for _, it := range items {
				m, ok := it.(map[string]any)
				if !ok {
					continue
				}
				k, _ := m["key"].(string)
				nm, _ := m["name"].(string)
				row := map[string]any{"bu": tg.label, "mid": tg.mid, "key": k, "name": nm}
				if strings.EqualFold(k, arg) || strings.EqualFold(nm, arg) {
					row["match"] = "exact"
					matches = append(matches, row)
				} else {
					containsOnly = append(containsOnly, tg.label)
				}
			}
		}
	}

	e := output.OK(200, matches)
	e.Count = len(matches)
	switch {
	case len(matches) > 0:
		e.Hint = "scope calls to a matching BU with --bu <bu> (tokens per BU are cached)"
	default:
		e.Hint = "no exact matches in any reachable context"
		if len(containsOnly) > 0 {
			e.Hint = fmt.Sprintf("no exact match, but similar DEs exist in BUs: %s", strings.Join(containsOnly, ", "))
		}
	}
	if len(unreachable) > 0 {
		e.Hint += fmt.Sprintf("; unreachable contexts: %s", strings.Join(unreachable, ", "))
	}
	_ = output.Print(e, c.pretty, stdout)
	return exitOK
}

// normalizeRowset flattens the VERIFIED rowset shape:
// {links:{next}, requestToken, count, page, pageSize, items:[{keys,values}]}
// into envelope {count, page, next, data:[merged rows]}.
func normalizeRowset(e *output.Envelope) {
	m, ok := e.Data.(map[string]any)
	if !ok {
		return // passthrough whatever came back
	}
	if n, ok := m["count"].(float64); ok {
		e.Count = int(n)
	}
	if links, ok := m["links"].(map[string]any); ok {
		if nl, ok := links["next"].(string); ok {
			e.Next = resolveNext(nl)
		}
	}
	e.Data = replaceWithRows(m)
}

// resolveNext converts a section-relative links.next ("/v1/...") into a

// replaceWithRows flattens items [{keys,values}] into single row objects.
func replaceWithRows(m map[string]any) any {
	items, _ := m["items"].([]any)
	rows := mergeRowItems(items)
	out := make([]any, len(rows))
	for i, r := range rows {
		out[i] = r
	}
	return out
}

// applyItemsEnvelope handles {items, count, links.next} collection shapes.
func applyItemsEnvelope(e *output.Envelope) {
	m, ok := e.Data.(map[string]any)
	if !ok {
		return
	}
	if n, ok := m["count"].(float64); ok {
		e.Count = int(n)
	}
	if items, ok := m["items"].([]any); ok {
		e.Data = items
		if e.Count == 0 {
			e.Count = len(items)
		}
	}
	if links, ok := m["links"].(map[string]any); ok {
		if nl, ok := links["next"].(string); ok {
			e.Next = resolveNext(nl)
		}
	}
}

// deMatch is one exact-match DE resolution result.
type deMatch struct {
	ID, Key, Name string
}

// findDEMatches returns DEs whose key OR name exactly matches arg
// (case-insensitive). len>1 means the argument is ambiguous.
func findDEMatches(body []byte, arg string) []deMatch {
	var out []deMatch
	m, ok := parseJSON(body).(map[string]any)
	if !ok {
		return nil
	}
	items, ok := m["items"].([]any)
	if !ok {
		return nil
	}
	seen := map[string]bool{}
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		key, _ := m["key"].(string)
		name, _ := m["name"].(string)
		id, _ := m["id"].(string)
		if id == "" {
			continue
		}
		if strings.EqualFold(key, arg) || strings.EqualFold(name, arg) {
			if !seen[id] {
				seen[id] = true
				out = append(out, deMatch{ID: id, Key: key, Name: name})
			}
		}
	}
	return out
}

// ambiguousDEErr builds a refusal when arg matches multiple DEs — we never
// guess which one the caller meant.
func ambiguousDEErr(arg string, matches []deMatch) *output.Envelope {
	keys := make([]string, 0, len(matches))
	for _, m := range matches {
		keys = append(keys, m.Key+" ("+m.Name+")")
	}
	return output.Fail(400, fmt.Sprintf("'%s' is ambiguous — matches %d data extensions:", arg, len(matches)),
		"pass the exact customerKey instead: "+strings.Join(keys, " | "))
}

// resolveDE is THE single way curated commands resolve a DE argument
// (customerKey or name) to one concrete DE. Not-found and ambiguous cases
// return a ready error envelope — callers must not guess.
func resolveDE(s *session, arg string) (*deMatch, *output.Envelope) {
	_, body, env2, _ := s.call(http.MethodGet,
		"data/v1/customObjects?$search="+url.QueryEscape(arg), nil, nil)
	if env2 != nil {
		return nil, env2
	}
	matches := findDEMatches(body, arg)
	switch {
	case len(matches) == 0:
		return nil, output.Fail(404, "DE '"+arg+"' not found in this scope",
			"not visible with the current token account — mcecli bu discover lists accessible BUs; check mcecli status")
	case len(matches) > 1:
		return nil, ambiguousDEErr(arg, matches)
	}
	return &matches[0], nil
}

// doGet runs a GET via a fresh session and returns the envelope (not printed).
// Optional anyOf scopes produce an advisory hint (never a block).
func doGet(c *common, path string, anyOfScopes ...string) (*output.Envelope, int) {
	s, env, code := newSession(c)
	if env != nil {
		return env, code
	}
	status, resp, env2, code := s.call(http.MethodGet, path, nil, nil)
	if env2 != nil {
		return env2, code
	}
	if status >= 400 {
		return errorEnvelope(status, resp, http.MethodGet), exitAPI
	}
	e := envelopeFor(status, resp)
	if adv := scopeAdvisory(s.tok, anyOfScopes...); adv != "" {
		e.Hint = adv
	}
	return e, exitOK
}

// fetchFields retrieves the field schema of a DE by id (best-effort).
func fetchFields(s *session, id string) ([]byte, bool) {
	status, resp, env2, _ := s.call(http.MethodGet,
		"data/v1/customObjects/"+url.PathEscape(id)+"/fields", nil, nil)
	if env2 != nil || status >= 400 {
		return nil, false
	}
	return resp, true
}

// pkNamesFromFields extracts isPrimaryKey field names from a fields response.
func pkNamesFromFields(body []byte) []string {
	m, ok := parseJSON(body).(map[string]any)
	if !ok {
		return nil
	}
	fl, ok := m["fields"].([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, f := range fl {
		if fm, ok := f.(map[string]any); ok {
			if pk, _ := fm["isPrimaryKey"].(bool); pk {
				if n, ok := fm["name"].(string); ok {
					out = append(out, n)
				}
			}
		}
	}
	return out
}

// mergePKValues merges the PK values into the before-image row (they are not
// repeated inside the SOAP row properties).
func mergePKValues(pk map[string]string, before map[string]string) map[string]any {
	row := map[string]any{}
	for k, v := range before {
		row[k] = v
	}
	for k, v := range pk {
		row["(key) "+k] = v
	}
	return row
}

// newUndoDir creates a fresh undo directory for one write operation.
func newUndoDir(profile, label string) string {
	stamp := time.Now().UTC().Format("20060102-150405.000000000")
	dir := filepath.Join(config.Dir(), "work", profile, "undo", stamp+"-"+label)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}
	return dir
}

// writeBeforeRows stores before-images as NDJSON for rollback.
func writeBeforeRows(undoDir string, rows []map[string]any) {
	if len(rows) == 0 {
		return
	}
	var b strings.Builder
	for _, r := range rows {
		if line, err := json.Marshal(r); err == nil {
			b.Write(line)
			b.WriteString("\n")
		}
	}
	_ = os.WriteFile(filepath.Join(undoDir, "before-rows.ndjson"), []byte(b.String()), 0o600)
	_ = os.WriteFile(filepath.Join(undoDir, "README.txt"), []byte(
		"Before-image rows captured before an async upsert.\nRollback: re-add these rows via 'mcecli de add' (values may need upsert semantics).\n"), 0o600)
}
