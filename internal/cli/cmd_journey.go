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
	"sort"
	"strconv"
	"strings"

	"github.com/dawidmachon/mcecli/internal/output"
)

// Journey reads (REST interaction/v1). VERIFIED live (round 5 probe):
// - GET /interaction/v1/interactions — one item per key (newest version
//   only); server ignores $pageSize (pages at 50) and ignores $filter —
//   name filtering must be client-side.
// - GET /interaction/v1/interactions/status/key:{key}?AllVersions=true —
//   the FULL version history: [{id, status, versionNumber}, …]; the 400
//   on a bare call teaches it ("AllVersions=true or VersionNumber
//   required"); ?VersionNumber=N narrows to one version; an unknown
//   version number returns an empty array (must be hinted, not mistaken
//   for success).
// Version semantics: a new publish bumps `version`; older versions are
// immutable history with their own status (Stopped/…). There is no
// versions-list endpoint in discovery — the status endpoint IS the
// version index. Lifecycle writes (publish/stop/pause) deliberately stay
// behind `rest` gates: they touch live sendout.

const usageJourney = `mcecli journey — journey reads (REST interaction/v1, READ-ONLY)

  mcecli journey list              — journeys in the current BU context
               [--search STR]    — client-side filter on name+key ($filter
                                   is ignored by this endpoint)
               [--page N]        — server pages at 50 ($pageSize not honored)
               [--full | --fields a,b,c] — default output is a curated
                                   projection (key/name/version/status/channel/
                                   executionMode/lastPublishedDate)
  mcecli journey versions <key>    — FULL version history of one journey
               [--version N]     — only version N
               (one call; lifecycle writes stay behind: mcecli rest … --write)

Journeys are per-BU. Empty result? Check the context: mcecli status.
`

func cmdJourney(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageJourney)
		return exitUsage
	}
	switch args[0] {
	case "list":
		return journeyList(args[1:], stdout, stderr)
	case "versions":
		return journeyVersions(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usageJourney)
		return exitOK
	default:
		fmt.Fprintf(stderr, "unknown 'journey' subcommand %q\n\n%s", args[0], usageJourney)
		return exitUsage
	}
}

// journeyLeanCols — curated default projection for journey list; the raw
// item carries ~24 properties (goals/exits/defaults/stats) that audits
// almost never want by default.
var journeyLeanCols = []string{"key", "name", "version", "status", "channel", "executionMode", "lastPublishedDate"}

func journeyList(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("journey list", flag.ContinueOnError)
	var c common
	var search string
	var full bool
	addCommon(fs, &c)
	addPaging(fs, &c)
	addProjection(fs, &c)
	fs.StringVar(&search, "search", "", "client-side filter on name+key (server-side $filter is ignored by this endpoint)")
	fs.BoolVar(&full, "full", false, "complete raw objects (default is a curated projection; --fields overrides both)")
	help := addHelp(fs)
	if err := parseCmd(fs, args); err != nil {
		return exitUsage
	}
	if *help {
		fmt.Fprint(stdout, usageJourney)
		return exitOK
	}
	if full && c.fields != "" {
		e := output.Fail(0, "--full and --fields are mutually exclusive",
			"--full for complete objects, or --fields a,b,c to choose columns")
		_ = output.Print(e, c.pretty, stdout)
		return exitUsage
	}

	u := url.URL{Path: "interaction/v1/interactions"}
	q := url.Values{}
	if c.page > 0 {
		q.Set("$page", strconv.Itoa(c.page))
	}
	if c.size > 0 {
		q.Set("$pageSize", strconv.Itoa(c.size))
	}
	u.RawQuery = q.Encode()

	e, code := doGet(&c, u.String(), "journeys_read")
	if code != exitOK || e == nil {
		if e != nil {
			_ = output.Print(e, c.pretty, stdout)
		}
		return code
	}
	applyItemsEnvelope(e)
	items, _ := e.Data.([]any)
	serverRows := len(items)

	total := e.Count
	if search != "" {
		term := strings.ToLower(search)
		filtered := make([]any, 0, len(items))
		for _, it := range items {
			m, ok := it.(map[string]any)
			if !ok {
				continue
			}
			name, _ := m["name"].(string)
			key, _ := m["key"].(string)
			if strings.Contains(strings.ToLower(name), term) || strings.Contains(strings.ToLower(key), term) {
				filtered = append(filtered, it)
			}
		}
		items = filtered
		e.Data = items
		e.Count = len(items)
		e.Hint = fmt.Sprintf("%d of %d journey(s) in this BU context match %q (client-side filter — the endpoint ignores $filter)", len(items), total, search)
	}
	// transparency: the server ignores $pageSize (pages at 50) — compare
	// against the SERVER row count, not the post-search filtered view
	if c.size > 0 && serverRows > c.size {
		if e.Hint != "" {
			e.Hint += " — "
		}
		e.Hint += fmt.Sprintf("server returned %d rows despite --size %d ($pageSize not honored by this endpoint)", serverRows, c.size)
	}
	if e.Count == 0 && e.Hint == "" {
		e.Hint = "no journeys in this context — journeys are per-BU: mcecli status shows the current context"
	}
	// precedence: --fields wins over --full wins over the curated default
	switch {
	case c.fields != "":
		e.Data = output.Project(e.Data, splitFields(c.fields))
	case !full:
		if e.Hint == "" {
			e.Hint = "curated default projection — --full for complete objects, --fields a,b,c to choose columns"
		}
		e.Data = output.Project(e.Data, journeyLeanCols)
	}
	_ = output.Print(e, c.pretty, stdout)
	return exitOK
}

// journeyVersions — the FULL version history of one journey key via the
// status endpoint (AllVersions=true). One call; --version N narrows to a
// single version (VersionNumber=N). An EMPTY result is real data (unknown
// version / unknown key) and is hinted loudly instead of passed as bare [].
func journeyVersions(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("journey versions", flag.ContinueOnError)
	var c common
	var version int
	addCommon(fs, &c)
	fs.IntVar(&version, "version", 0, "only version N (default: all versions)")
	help := addHelp(fs)
	if err := parseCmd(fs, args); err != nil {
		return exitUsage
	}
	if *help {
		fmt.Fprint(stdout, usageJourney)
		return exitOK
	}
	if fs.NArg() != 1 {
		fmt.Fprint(stderr, "usage: mcecli journey versions <key>\n")
		return exitUsage
	}
	if version < 0 {
		e := output.Fail(0, "--version must be >= 0", "0 = all versions (the default)")
		_ = output.Print(e, c.pretty, stdout)
		return exitUsage
	}
	key := fs.Arg(0)

	u := url.URL{Path: "interaction/v1/interactions/status/key:" + url.PathEscape(key)}
	q := url.Values{}
	if version > 0 {
		q.Set("VersionNumber", strconv.Itoa(version))
	} else {
		q.Set("AllVersions", "true") // bare call 400s: "AllVersions=true or VersionNumber required"
	}
	u.RawQuery = q.Encode()

	s, env, code := newSession(&c)
	if env != nil {
		_ = output.Print(env, c.pretty, stdout)
		return code
	}
	st, resp, env2, _ := s.call(http.MethodGet, u.String(), nil, nil)
	if env2 != nil {
		_ = output.Print(env2, c.pretty, stdout)
		return exitAPI
	}
	if st >= 400 {
		e := errorEnvelope(st, resp, http.MethodGet)
		_ = output.Print(e, c.pretty, stdout)
		return exitAPI
	}
	raw, ok := parseJSON(resp).([]any)
	if !ok {
		e := output.Fail(st, "unexpected response shape",
			"raw path: mcecli rest GET interaction/v1/interactions/status/key:"+key+"?AllVersions=true")
		_ = output.Print(e, c.pretty, stdout)
		return exitAPI
	}

	rows := make([]map[string]any, 0, len(raw))
	for _, it := range raw {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		rows = append(rows, map[string]any{
			"version":      m["versionNumber"],
			"status":       m["status"],
			"definitionId": m["id"],
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		a, _ := rows[i]["version"].(float64)
		b, _ := rows[j]["version"].(float64)
		return a < b
	})

	e := output.OK(st, rows)
	e.Count = len(rows)
	switch {
	case len(rows) == 0:
		if version > 0 {
			e.Hint = fmt.Sprintf("no version %d for key %s — AllVersions shows what exists", version, key)
		} else {
			e.Hint = "no versions for this key — typo, or the journey lives in another BU (mcecli status)"
		}
	case len(rows) > 1:
		latest := rows[len(rows)-1]
		e.Hint = fmt.Sprintf("%d versions; latest v%v is %s — older versions are immutable history; per-version detail: mcecli rest GET interaction/v1/interactions/%v?extras=all",
			len(rows), latest["version"], latest["status"], latest["definitionId"])
	default:
		e.Hint = "single version; per-version detail: mcecli rest GET interaction/v1/interactions/" + keyRowID(rows[0]) + "?extras=all"
	}
	_ = output.Print(e, c.pretty, stdout)
	return exitOK
}

func keyRowID(row map[string]any) string {
	if id, ok := row["definitionId"].(string); ok {
		return id
	}
	b, _ := json.Marshal(row["definitionId"])
	return string(b)
}
