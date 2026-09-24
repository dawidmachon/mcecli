// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

// md: metadata family — read-only, delivery-to-disk, mcdev-style retrieves
// with the mcecli envelope. No deploy/push (mcdev owns that).

import (
	"encoding/json"
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

	"github.com/dawidmachon/mcecli/internal/output"
)

type mdTypeDef struct {
	List           string // list endpoint (relative to REST host)
	SearchRequired bool   // endpoint requires $search (customObjects)
	SearchParam    string // "$search"
	PageSize       int
}

var mdTypes = map[string]mdTypeDef{
	"journeys":    {List: "interaction/v1/interactions", PageSize: 100},
	"automations": {List: "automation/v1/automations", PageSize: 100},
	"queries":     {List: "automation/v1/queries", PageSize: 100},
	"imports":     {List: "automation/v1/imports", PageSize: 100},
	"des":         {List: "data/v1/customObjects", SearchRequired: true, SearchParam: "$search", PageSize: 100},
	"assets":      {List: "asset/v1/content/assets", SearchRequired: true, SearchParam: "$filter", PageSize: 100},
}

const usageMD = `mcecli md — metadata family: read-only retrieves to local files (mcdev-style, envelope-free payloads)

  mcecli md types              list supported metadata types
  mcecli md pull <type>        retrieve ALL items of a type
      [--search X]          required for des ($search) and assets ($filter)
      [--size N]            page size (default 100)
      [--out DIR]           target directory
      [--refresh]           re-pull even if cached
      [--fields a,b]        keep only these fields in the stored items

Files: ~/.mcecli/work/<profile>/md/<type>/<id-or-key>.json (full item each)
       + index.json (id/key/name/file listing). Cache-first; --refresh re-pulls.
Deploy/push is deliberately OUT of scope — mcdev owns that.

Examples:
  mcecli md types
  mcecli md pull journeys --refresh
  mcecli md pull des --search preference
`

func cmdMD(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageMD)
		return exitUsage
	}
	// accept both 'mcecli md pull <type>' and 'mcecli md <type>'
	if args[0] == "pull" && len(args) >= 2 {
		args = args[1:]
	}
	if args[0] == "types" {
		names := make([]string, 0, len(mdTypes))
		for k := range mdTypes {
			names = append(names, k)
		}
		sort.Strings(names)
		_ = output.Print(output.OK(0, names), false, stdout)
		return exitOK
	}

	tdef, ok := mdTypes[strings.ToLower(args[0])]
	if !ok {
		e := output.Fail(0, "unknown metadata type '"+args[0]+"'",
			"mcecli md types — or probe any family with: mcecli rest GET <section>/v1/...")
		_ = output.Print(e, false, stdout)
		return exitUsage
	}
	return mdPull(strings.ToLower(args[0]), tdef, args[1:], stdout, stderr)
}

func mdPull(typeName string, tdef mdTypeDef, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("md pull "+typeName, flag.ContinueOnError)
	var c common
	var outDir, search, fields string
	addCommon(fs, &c)
	fs.IntVar(&c.size, "size", tdef.PageSize, "page size")
	fs.StringVar(&outDir, "out", "", "output directory (default ~/.mcecli/work/<profile>/md/<type>)")
	fs.StringVar(&outDir, "path", "", "alias for --out")
	fs.StringVar(&search, "search", "", "search term"+map[bool]string{true: " (REQUIRED for this type)", false: ""}[tdef.SearchRequired])
	fs.StringVar(&fields, "fields", "", "project stored items to these fields (comma-separated)")
	fs.BoolVar(&c.refresh, "refresh", false, "re-pull even if cached")
	help := addHelp(fs)
	if err := parseCmd(fs, args); err != nil {
		return exitUsage
	}
	if *help {
		fmt.Fprint(stdout, usageMD)
		return exitOK
	}

	profile := currentProfileName(&c)
	out := filepath.Join(workRoot(profile), "md", typeName)
	if outDir != "" {
		out = outDir
	}
	indexPath := filepath.Join(out, "index.json")
	metaPath := indexPath + ".meta.json"

	// cache-first
	if !c.refresh {
		if meta := readMeta(metaPath); meta != nil {
			return printDumpEnvelope(stdout, indexPath, meta,
				"served from disk cache (fetched "+cacheAgeLabel(int64(meta["fetchedAt"].(float64)))+") — --refresh re-pulls")
		}
	}

	s, env, code := newSession(&c)
	if env != nil {
		_ = output.Print(env, c.pretty, stdout)
		return code
	}

	// paginate: $page/$pageSize (SFMC standard); stop when a short page arrives
	items := []map[string]any{}
	const maxPages = 500
	for page := 1; page <= maxPages; page++ {
		q := url.Values{}
		q.Set("$pageSize", strconv.Itoa(c.size))
		q.Set("$page", strconv.Itoa(page))
		if tdef.SearchRequired {
			if search == "" {
				e := output.Fail(0, "this type requires a search term",
					"mcecli md pull "+typeName+" --search <term>")
				_ = output.Print(e, c.pretty, stdout)
				return exitUsage
			}
			q.Set(tdef.SearchParam, search)
		}
		path := tdef.List + "?" + q.Encode()
		status, resp, env2, code := s.call(http.MethodGet, path, nil, nil)
		if env2 != nil {
			_ = output.Print(env2, c.pretty, stdout)
			return code
		}
		if status >= 400 {
			_ = output.Print(errorEnvelope(status, resp, http.MethodGet), c.pretty, stdout)
			return exitAPI
		}
		pageItems, ok := parseJSON(resp).(map[string]any)["items"].([]any)
		if !ok {
			break
		}
		for _, it := range pageItems {
			if m, ok := it.(map[string]any); ok {
				items = append(items, m)
			}
		}
		if len(pageItems) < c.size {
			break
		}
	}

	if err := os.MkdirAll(out, 0o700); err != nil {
		_ = output.Print(output.Fail(0, err.Error(), ""), c.pretty, stdout)
		return exitCfg
	}
	proj := splitFields(fields)
	index := make([]map[string]any, 0, len(items))
	for _, item := range items {
		id, _ := item["id"].(string)
		key, _ := item["key"].(string)
		name, _ := item["name"].(string)
		if id == "" {
			if v, ok := item["queryDefinitionId"].(string); ok {
				id = v
			}
		}
		fname := slugify(key)
		if fname == "" {
			fname = slugify(name)
		}
		if fname == "" {
			fname = "item-" + strconv.Itoa(len(index)+1)
		}
		itemFile := fname + ".json"
		stored := any(item)
		if len(proj) > 0 {
			stored = output.Project(item, proj)
		}
		if b, err := json.MarshalIndent(stored, "", " "); err == nil {
			_ = os.WriteFile(filepath.Join(out, itemFile), b, 0o600)
		}
		index = append(index, map[string]any{
			"file": itemFile, "id": id, "key": key, "name": name,
		})
	}
	if ib, err := json.MarshalIndent(map[string]any{"type": typeName, "count": len(items), "items": index}, "", " "); err == nil {
		_ = os.WriteFile(indexPath, ib, 0o600)
	}
	meta := map[string]any{
		"fetchedAt": time.Now().Unix(), "rows": len(items),
		"type": typeName, "profile": profile,
	}
	writeMeta(metaPath, meta)

	e := output.OK(200, map[string]any{
		"type": typeName, "path": out, "count": len(items),
		"items": indexSample(index),
	})
	e.Hint = "grep/jq the files locally; index.json lists everything — --refresh re-pulls"
	_ = output.Print(e, c.pretty, stdout)
	return exitOK
}

func indexSample(index []map[string]any) []map[string]any {
	const max = 10
	if len(index) <= max {
		return index
	}
	return index[:max]
}
