// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/dawidmachon/mcecli/internal/output"
)

// Automations operational reads (REST /automation/v1/automations).
// VERIFIED live 2026-09-17: list (count = total on parent BU, count=total,
// $page/$pageSize honored), /{id} detail (+steps with activities),
// /healthreport (CSV: 30DaySuccessRate, 30DayErrorCount, per-automation).

const usageAuto = `mcecli auto — automation operational reads (read-only)

  mcecli auto list               — automations (name/key/status/lastRunTime)
               [--search STR] — client-side filter on name+key
               [--status S]   — client-side filter on status (e.g. Error)
               [--limit N]    — max rows after filtering (default 50)
               [--all]        — scan all pages (may be thousands of rows)
  mcecli auto <id-or-key>        — one automation: detail + step activities
  mcecli auto health             — platform health report (30-day success/
                                error/skip counts per automation) as JSON

Ops debugging: health → pick failing automation → detail → steps.
`

func cmdAuto(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageAuto)
		return exitUsage
	}
	switch args[0] {
	case "list":
		return autoList(args[1:], stdout, stderr)
	case "health":
		return autoHealth(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usageAuto)
		return exitOK
	default:
		return autoDetail(args, stdout, stderr)
	}
}

func autoList(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("auto list", flag.ContinueOnError)
	var c common
	var search, statusFilter string
	var limit int
	var all bool
	addCommon(fs, &c)
	fs.StringVar(&search, "search", "", "client-side filter on name+key")
	fs.StringVar(&statusFilter, "status", "", "client-side filter on status")
	fs.IntVar(&limit, "limit", 50, "max rows after filtering")
	fs.BoolVar(&all, "all", false, "scan all pages (many API calls)")
	if err := parseCmd(fs, args); err != nil {
		return exitUsage
	}

	s, env, code := newSession(&c)
	if env != nil {
		_ = output.Print(env, c.pretty, stdout)
		return code
	}

	var out []map[string]any
	total := 0
	scanned := 0
	maxPage := 1
	if all {
		maxPage = 60 // ~3000 rows ceiling
	}
	for page := 1; page <= maxPage; page++ {
		path := fmt.Sprintf("automation/v1/automations?$pageSize=50&$page=%d", page)
		st, resp, env2, _ := s.call(http.MethodGet, path, nil, nil)
		if env2 != nil {
			_ = output.Print(env2, c.pretty, stdout)
			return exitAPI
		}
		if st < 200 || st > 299 {
			e := output.Fail(st, string(resp), "check automation API access")
			_ = output.Print(e, c.pretty, stdout)
			return exitAPI
		}
		m, ok := parseJSON(resp).(map[string]any)
		if !ok {
			e := output.Fail(0, "unexpected response shape", "")
			_ = output.Print(e, c.pretty, stdout)
			return exitAPI
		}
		if total == 0 {
			if f, ok := m["count"].(float64); ok {
				total = int(f)
			}
		}
		items, _ := m["items"].([]any)
		scanned += len(items)
		for _, it := range items {
			im, ok := it.(map[string]any)
			if !ok {
				continue
			}
			if search != "" {
				name, _ := im["name"].(string)
				key, _ := im["key"].(string)
				if !strings.Contains(strings.ToLower(name), strings.ToLower(search)) &&
					!strings.Contains(strings.ToLower(key), strings.ToLower(search)) {
					continue
				}
			}
			if statusFilter != "" {
				stt, _ := im["status"].(string)
				if !strings.EqualFold(stt, statusFilter) {
					continue
				}
			}
			if limit > 0 && len(out) >= limit {
				break
			}
			out = append(out, map[string]any{
				"id":          str(im, "id"),
				"key":         str(im, "key"),
				"name":        str(im, "name"),
				"status":      str(im, "status"),
				"lastRunTime": str(im, "lastRunTime"),
				"type":        str(im, "type"),
			})
		}
		if len(out) >= limit && limit > 0 {
			break
		}
		if scanned >= total || len(items) == 0 {
			break
		}
	}
	if out == nil {
		out = []map[string]any{}
	}
	e := output.OK(200, out)
	e.Count = len(out)
	hint := fmt.Sprintf("%d automations in context, %d scanned", total, scanned)
	if search != "" || statusFilter != "" {
		hint += " — filtered client-side"
	}
	if total > scanned {
		hint += fmt.Sprintf(" — use --all to scan the remaining %d", total-scanned)
	}
	e.Hint = hint
	_ = output.Print(e, c.pretty, stdout)
	return exitOK
}

func autoDetail(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("auto detail", flag.ContinueOnError)
	var c common
	var expandQueries, deps bool
	addCommon(fs, &c)
	fs.BoolVar(&expandQueries, "expand-queries", false, "resolve query activities → SQL text + target DE")
	fs.BoolVar(&deps, "deps", false, "list input/output DEs of the automation's queries (best-effort)")
	if err := parseCmd(fs, args); err != nil {
		return exitUsage
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprint(stderr, usageAuto)
		return exitUsage
	}
	ref := rest[0]

	s, env, code := newSession(&c)
	if env != nil {
		_ = output.Print(env, c.pretty, stdout)
		return code
	}

	// id is a GUID; key is not — resolve keys via a filtered list scan
	path := "automation/v1/automations/" + ref
	if !looksLikeGUID(ref) {
		found := ""
		for page := 1; page <= 30; page++ {
			p := fmt.Sprintf("automation/v1/automations?$pageSize=50&$page=%d", page)
			st, resp, env2, _ := s.call(http.MethodGet, p, nil, nil)
			if env2 != nil || (st < 200 || st > 299) {
				break
			}
			m, ok := parseJSON(resp).(map[string]any)
			if !ok {
				break
			}
			items, _ := m["items"].([]any)
			for _, it := range items {
				if im, ok := it.(map[string]any); ok {
					if strings.EqualFold(str(im, "key"), ref) || strings.EqualFold(str(im, "name"), ref) {
						found = str(im, "id")
						break
					}
				}
			}
			if found != "" {
				break
			}
			if len(items) == 0 {
				break // empty page — no point asking for more
			}
			if cnt, ok := m["count"].(float64); ok && scannedCount(items)*page >= int(cnt) {
				break
			}
		}
		if found == "" {
			e := output.Fail(0, fmt.Sprintf("no automation with key/name %q", ref), "mcecli auto list --search "+ref)
			_ = output.Print(e, c.pretty, stdout)
			return exitAPI
		}
		path = "automation/v1/automations/" + found
	}

	st, resp, env2, _ := s.call(http.MethodGet, path, nil, nil)
	if env2 != nil {
		_ = output.Print(env2, c.pretty, stdout)
		return exitAPI
	}
	if st < 200 || st > 299 {
		msg := strings.TrimSpace(string(resp))
		if m, ok := parseJSON(resp).(map[string]any); ok {
			msg = str(m, "message")
		}
		e := output.Fail(st, msg, "mcecli auto list --search to find the right automation")
		_ = output.Print(e, c.pretty, stdout)
		return exitAPI
	}
	m, ok := parseJSON(resp).(map[string]any)
	if !ok {
		e := output.Fail(0, "unexpected response shape", "")
		_ = output.Print(e, c.pretty, stdout)
		return exitAPI
	}
	steps := summarizeSteps(m)
	data := map[string]any{
		"id":          str(m, "id"),
		"key":         str(m, "key"),
		"name":        str(m, "name"),
		"status":      str(m, "status"),
		"lastRunTime": str(m, "lastRunTime"),
		"description": str(m, "description"),
		"steps":       steps,
	}

	// --expand-queries: resolve query activities (objectTypeId 300) against
	// the queries collection — SQL text, target DE and update mode per query.
	if expandQueries {
		type qInfo struct {
			key, name, target, update, text string
		}
		byName := map[string]qInfo{}
		items, _, _, ferr := queryFetchAll(s, 120) // up to ~3000 query defs
		if ferr != nil {
			data["expand_error"] = ferr.Error()
		} else {
			for _, it := range items {
				if im, ok := it.(map[string]any); ok {
					k, _ := im["key"].(string)
					n, _ := im["name"].(string)
					qt, _ := im["queryText"].(string)
					tk, _ := im["targetKey"].(string)
					ut, _ := im["targetUpdateTypeName"].(string)
					if len(qt) > 400 {
						qt = qt[:400] + "…"
					}
					byName[strings.ToLower(k)] = qInfo{k, n, tk, ut, strings.ReplaceAll(qt, "\n", " ")}
					byName[strings.ToLower(n)] = qInfo{k, n, tk, ut, strings.ReplaceAll(qt, "\n", " ")}
				}
			}
			expanded := make([]map[string]any, 0)
			for _, stp := range steps {
				for i, an := range stp["activities"].([]string) {
					if stp["types"].([]string)[i] != "query" {
						continue
					}
					if qi, ok := byName[strings.ToLower(an)]; ok {
						expanded = append(expanded, map[string]any{
							"activity":  an,
							"key":       qi.key,
							"target":    qi.target,
							"update":    qi.update,
							"queryText": qi.text,
						})
					} else {
						expanded = append(expanded, map[string]any{"activity": an, "resolved": false})
					}
				}
			}
			data["queries"] = expanded
		}
	}

	// --deps: input/output DEs of the automation's queries (best-effort:
	// sources parsed FROM the SQL text)
	if deps {
		depSet := map[string]bool{}
		var sources []string
		for _, it := range data["queries"].([]map[string]any) {
			if t, ok := it["target"].(string); ok && t != "" {
				depSet[t] = true
			}
			if qt, ok := it["queryText"].(string); ok {
				for _, m := range depSourceRe.FindAllStringSubmatch(strings.ToUpper(qt), -1) {
					src := strings.Trim(m[1], "[]")
					if !depSet[src] {
						depSet[src] = true
						sources = append(sources, src)
					}
				}
			}
		}
		targets := []string{}
		for t := range depSet {
			targets = append(targets, t)
		}
		data["deps"] = map[string]any{
			"outputs": targets,
			"inputs":  sources,
			"note":    "best-effort: sources parsed from SQL FROM/JOIN clauses",
		}
	}

	e := output.OK(st, data)
	e.Hint = "health context: mcecli auto health"
	_ = output.Print(e, c.pretty, stdout)
	return exitOK
}

// depSourceRe matches FROM/JOIN table references in SFMC SQL.
var depSourceRe = regexp.MustCompile(`(?:FROM|JOIN)\s+([_A-Za-z0-9\[\]]+)`)

// autoHealth fetches /automations/healthreport (CSV) and parses it to rows.
func autoHealth(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("auto health", flag.ContinueOnError)
	var c common
	var limit int
	addCommon(fs, &c)
	fs.IntVar(&limit, "limit", 100, "max rows")
	if err := parseCmd(fs, args); err != nil {
		return exitUsage
	}

	s, env, code := newSession(&c)
	if env != nil {
		_ = output.Print(env, c.pretty, stdout)
		return code
	}
	st, resp, env2, _ := s.call(http.MethodGet, "automation/v1/automations/healthreport", nil, nil)
	if env2 != nil {
		_ = output.Print(env2, c.pretty, stdout)
		return exitAPI
	}
	if st < 200 || st > 299 {
		e := output.Fail(st, strings.TrimSpace(string(resp)), "healthreport needs automation read scope")
		_ = output.Print(e, c.pretty, stdout)
		return exitAPI
	}
	rows, headers := parseCSV(strings.TrimPrefix(string(resp), "\xef\xbb\xbf"))
	if rows == nil {
		rows = [][]string{}
	}
	out := make([]map[string]string, 0, len(rows))
	for _, r := range rows {
		if len(out) >= limit && limit > 0 {
			break
		}
		m := map[string]string{}
		for i, h := range headers {
			if i < len(r) {
				m[h] = r[i]
			}
		}
		out = append(out, m)
	}
	e := output.OK(st, out)
	e.Count = len(out)
	e.Hint = "sorted by platform; watch 30DaySuccessRate < 100 and 30DayErrorCount > 0"
	_ = output.Print(e, c.pretty, stdout)
	return exitOK
}

// parseCSV parses simple RFC4180-ish CSV (quoted fields with commas).
func parseCSV(s string) ([][]string, []string) {
	var rows [][]string
	var cur []string
	var field, header []string
	inQ, headerDone := false, false
	var b strings.Builder
	flush := func() {
		field = append(field, b.String())
		b.Reset()
	}
	endField := func() {
		flush()
		cur = append(cur, field[len(field)-1])
		field = field[:len(field)-1]
	}
	endRow := func() {
		endField()
		if !headerDone {
			header = cur
			headerDone = true
		} else {
			rows = append(rows, cur)
		}
		cur = nil
	}
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case inQ:
			if ch == '"' {
				if i+1 < len(s) && s[i+1] == '"' {
					b.WriteByte('"')
					i++
				} else {
					inQ = false
				}
			} else {
				b.WriteByte(ch)
			}
		case ch == '"':
			inQ = true
		case ch == ',':
			endField()
		case ch == '\r':
			// skip
		case ch == '\n':
			if len(cur) > 0 || b.Len() > 0 {
				endRow()
			}
		default:
			b.WriteByte(ch)
		}
	}
	if len(cur) > 0 || b.Len() > 0 {
		endRow()
	}
	return rows, header
}

func summarizeSteps(m map[string]any) []map[string]any {
	stepsAny, _ := m["steps"].([]any)
	out := make([]map[string]any, 0, len(stepsAny))
	for i, stp := range stepsAny {
		sm, ok := stp.(map[string]any)
		if !ok {
			continue
		}
		acts, _ := sm["activities"].([]any)
		var actNames []string
		var actTypes []float64
		for _, a := range acts {
			am, ok := a.(map[string]any)
			if !ok {
				continue
			}
			actNames = append(actNames, str(am, "name"))
			if t, ok := am["objectTypeId"].(float64); ok {
				actTypes = append(actTypes, t)
			}
		}
		labels := make([]string, 0, len(actTypes))
		for _, t := range actTypes {
			labels = append(labels, activityTypeLabel(int64(t)))
		}
		out = append(out, map[string]any{
			"step":          i + 1,
			"name":          str(sm, "name"),
			"activities":    actNames,
			"objectTypeIds": actTypes,
			"types":         labels,
		})
	}
	return out
}

func looksLikeGUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, ch := range s {
		switch ch {
		case '-':
			if i != 8 && i != 13 && i != 18 && i != 23 {
				return false
			}
		default:
			if !strings.ContainsRune("0123456789abcdefABCDEF", ch) {
				return false
			}
		}
	}
	return true
}

func scannedCount(items []any) int { return len(items) }

// automationActivityTypes maps Automation Studio objectTypeId values to
// labels (community mapping; verified on this tenant: 300=query activities,
// 43=import activities by name convention).
var automationActivityTypes = map[int64]string{
	300: "query",
	43:  "import",
	42:  "script",
	467: "wait",
	73:  "report",
	550: "data extract",
	728: "email send",
}

func activityTypeLabel(id int64) string {
	if l, ok := automationActivityTypes[id]; ok {
		return l
	}
	return fmt.Sprintf("type %d", id)
}
