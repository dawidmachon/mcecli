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
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dawidmachon/mcecli/internal/config"
	"github.com/dawidmachon/mcecli/internal/output"
)

const usageAPI = `mcecli api — explore the SFMC REST API surface via its discovery documents

  mcecli api sections              list known API sections
  mcecli api <section>             compact method index (name, method, path)
  mcecli api <section> <method>    details for one method + ready 'mcecli rest' line

Indexes are CACHED on disk (~/.mcecli/apidocs/) — SFMC APIs rarely change, so
default source is the cache (zero network). --refresh re-syncs; --raw shows
the full discovery JSON (live, uncached).

Examples:
  mcecli api data --filter customobject
  mcecli api messaging getMessageSendsCollection
  mcecli api hub --refresh
`

// knownSections are sections verified to answer <section>/v1/rest live.
const knownSections = "data asset automation interaction hub messaging push platform email sms"

// apiCache: discovery indexes barely ever change, so mcecli precaches them
// to disk and serves agent queries without touching the network.
type apiCache struct {
	FetchedAt int64            `json:"fetchedAt"`
	Methods   []apiMethodEntry `json:"methods"`
}

// apiMethodEntry is one flattened discovery entry (cache-serializable).
type apiMethodEntry struct {
	Name        string `json:"name"`
	Method      string `json:"method"`
	Path        string `json:"path"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
}

func apiCachePath(section string) string {
	return filepath.Join(config.Dir(), "apidocs", strings.ToLower(section)+".json")
}

// apiSearch greps every cached discovery doc for a keyword — answers
// "which endpoint deals with X" without knowing the section first.
func apiSearch(keyword string, refresh bool, stdout, stderr io.Writer) int {
	kw := strings.ToLower(keyword)
	type hit struct {
		section, method, path, name string
	}
	var hits []hit
	for _, section := range strings.Fields(knownSections) {
		ac := loadAPICache(section)
		if ac == nil {
			continue
		}
		for _, me := range ac.Methods {
			hay := strings.ToLower(me.Method + " " + me.Path + " " + me.Name + " " + me.Description)
			if strings.Contains(hay, kw) {
				hits = append(hits, hit{section, me.Method, me.Path, me.Name})
			}
		}
	}
	if hits == nil {
		hits = []hit{}
	}
	out := make([]map[string]string, 0, len(hits))
	for _, h := range hits {
		out = append(out, map[string]string{"section": h.section, "method": h.method, "path": h.path, "name": h.name})
	}
	e := output.OK(0, out)
	e.Count = len(out)
	e.Hint = "call with: mcecli rest <METHOD> <path> — or mcecli api <section> for details"
	_ = output.Print(e, false, stdout)
	return exitOK
}

func loadAPICache(section string) *apiCache {
	b, err := os.ReadFile(apiCachePath(section))
	if err != nil {
		return nil
	}
	var ac apiCache
	if json.Unmarshal(b, &ac) != nil || len(ac.Methods) == 0 {
		return nil
	}
	return &ac
}

func saveAPICache(section string, entries []apiMethodEntry) {
	p := apiCachePath(section)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return
	}
	if b, err := json.Marshal(apiCache{FetchedAt: time.Now().Unix(), Methods: entries}); err == nil {
		_ = os.WriteFile(p, b, 0o600)
	}
}

func cacheAgeLabel(fetchedAt int64) string {
	d := time.Since(time.Unix(fetchedAt, 0))
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%.1fh ago", d.Hours())
	default:
		return fmt.Sprintf("%.1fd ago", d.Hours()/24)
	}
}

func cmdAPI(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("api", flag.ContinueOnError)
	var c common
	var filter, search string
	addCommon(fs, &c)
	fs.StringVar(&filter, "filter", "", "substring filter on name/path/description (within the section)")
	fs.StringVar(&search, "search", "", "cross-section keyword search (no section needed)")
	fs.BoolVar(&c.refresh, "refresh", false, "re-fetch the discovery document, updating the cache")
	help := addHelp(fs)
	if err := parseCmd(fs, args); err != nil {
		return exitUsage
	}
	if *help {
		fmt.Fprint(stdout, usageAPI)
		return exitOK
	}
	rest := fs.Args()
	if search != "" {
		return apiSearch(search, c.refresh, stdout, stderr)
	}
	if len(rest) == 0 {
		fmt.Fprint(stderr, usageAPI)
		return exitUsage
	}
	if rest[0] == "sections" {
		_ = output.Print(output.OK(0, strings.Fields(knownSections)), c.pretty, stdout)
		return exitOK
	}
	section := rest[0]

	// cache-first: zero-network answers for agents
	if !c.refresh && !c.raw {
		if ac := loadAPICache(section); ac != nil {
			hint := fmt.Sprintf("served from disk cache (fetched %s) — --refresh to re-sync", cacheAgeLabel(ac.FetchedAt))
			return serveAPI(&c, stdout, stderr, section, rest, ac.Methods, hint, filter)
		}
	}

	s, env, code := newSession(&c)
	if env != nil {
		_ = output.Print(env, c.pretty, stdout)
		return code
	}
	status, resp, env2, code := s.call(http.MethodGet, section+"/v1/rest", nil, nil)
	if env2 != nil {
		_ = output.Print(env2, c.pretty, stdout)
		return code
	}
	if status >= 400 {
		e := errorEnvelope(status, resp, http.MethodGet)
		e.Hint = "no discovery document for section '" + section + "' — known good: " + knownSections
		_ = output.Print(e, c.pretty, stdout)
		return exitAPI
	}
	if c.raw {
		_, _ = stdout.Write(resp)
		fmt.Fprintln(stdout)
		return exitOK
	}

	entries := extractMethods(parseJSON(resp))
	if len(entries) == 0 {
		e := output.Fail(0, "no methods found in discovery document",
			"inspect raw with: mcecli api "+section+" --raw")
		_ = output.Print(e, c.pretty, stdout)
		return exitAPI
	}
	saveAPICache(section, entries)
	return serveAPI(&c, stdout, stderr, section, rest, entries,
		"fetched live and cached to disk — next calls are served from cache", filter)
}

// serveAPI renders index or detail from extracted entries.
func serveAPI(c *common, stdout, stderr io.Writer, section string, rest []string, entries []apiMethodEntry, hint, filter string) int {
	if len(rest) >= 2 {
		for _, m := range entries {
			if strings.EqualFold(m.Name, rest[1]) {
				_ = output.Print(methodDetailEnvelope(section, m), c.pretty, stdout)
				return exitOK
			}
		}
		e := output.Fail(404, "method '"+rest[1]+"' not found in section '"+section+"'",
			"mcecli api "+section+" --filter "+rest[1])
		_ = output.Print(e, c.pretty, stdout)
		return exitAPI
	}

	rows := make([]any, 0, len(entries))
	for _, m := range entries {
		rows = append(rows, map[string]any{"name": m.Name, "method": m.Method, "path": "/" + section + "/v1/" + m.Path})
	}
	e := output.OK(200, rows)
	e.Count = len(rows)
	e.Hint = hint + "; details: mcecli api " + section + " <method>"
	if filter != "" {
		rows = filterAPIRows(rows, filter)
		e.Data = rows
		e.Count = len(rows)
	}
	_ = output.Print(e, c.pretty, stdout)
	return exitOK
}

func filterAPIRows(rows []any, filter string) []any {
	f := strings.ToLower(filter)
	out := []any{}
	for _, r := range rows {
		m := r.(map[string]any)
		hay := strings.ToLower(m["name"].(string) + " " + m["path"].(string))
		if strings.Contains(hay, f) {
			out = append(out, r)
		}
	}
	return out
}

// extractMethods flattens root.methods (and nested resources, if a section
// uses them) into a sorted, cache-ready list. Shape tolerant by design:
// data/automation style (flat methods) and Google discovery style both work.
func extractMethods(doc any) []apiMethodEntry {
	root, ok := doc.(map[string]any)
	if !ok {
		return nil
	}
	var out []apiMethodEntry
	var walk func(m map[string]any)
	walk = func(m map[string]any) {
		for k, v := range m {
			sub, ok := v.(map[string]any)
			if !ok {
				continue
			}
			if _, isEntry := sub["path"]; isEntry {
				hm, isAPI := sub["httpMethod"].(string)
				if !isAPI {
					continue
				}
				if k == "discovery" {
					continue
				}
				d, _ := sub["description"].(string)
				out = append(out, apiMethodEntry{
					Name: k, Method: strings.ToUpper(hm),
					Path: sub["path"].(string), Description: d,
					Parameters: sub["parameters"],
				})
				continue
			}
			if k == "methods" || k == "resources" {
				walk(sub)
			}
		}
	}
	walk(root)
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// methodDetailEnvelope renders one method with its parameters and a
// ready-to-run mcecli rest example.
func methodDetailEnvelope(section string, m apiMethodEntry) *output.Envelope {
	fullPath := "/" + section + "/v1/" + m.Path
	data := map[string]any{
		"name":    m.Name,
		"method":  m.Method,
		"path":    fullPath,
		"example": "mcecli rest " + m.Method + " " + strings.TrimPrefix(fullPath, "/") + "   # replace {placeholders}; paths are case-sensitive",
	}
	if m.Description != "" {
		data["description"] = m.Description
	}
	if m.Parameters != nil {
		data["parameters"] = m.Parameters
	}
	if m.Method != "GET" {
		data["body_note"] = "discovery docs do NOT include body schemas — for DE rows prefer: mcecli de add; verified write recipes: mcecli help rest"
	}
	return output.OK(200, data)
}
