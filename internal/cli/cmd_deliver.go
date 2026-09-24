// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/dawidmachon/mcecli/internal/config"
	"github.com/dawidmachon/mcecli/internal/output"
)

// Delivery commands: pull data to local files so agents can grep/scan them
// with cheap local tools; the envelope carries only the summary + path.
// All dumps are cache-first: an existing file is served until --refresh.

const usageAsset = `mcecli asset — search and download assets to local files

  mcecli asset search --name X [--pull]    find assets by name, optionally download bodies
  mcecli asset pull <id> [--out DIR]      download one asset: meta.json + content

Assets cached under ~/.mcecli/work/<profile>/asset/ — --refresh re-pulls.
`

var slugRe = regexp.MustCompile(`[^a-z0-9_-]+`)

func slugify(s string) string {
	return strings.Trim(slugRe.ReplaceAllString(strings.ToLower(s), "_"), "_")
}

func workRoot(profile string) string {
	return filepath.Join(config.Dir(), "work", profile)
}

// currentProfileName resolves the profile name without touching the network
// (needed to place cache files before any session exists).
func currentProfileName(c *common) string {
	cfg, err := config.Load()
	if err != nil {
		return "default"
	}
	res, err := config.Resolve(cfg, config.LoadState(), c.profile, "")
	if err != nil {
		return "default"
	}
	return res.Name
}

func assetExt(assetTypeName string) string {
	switch strings.ToLower(assetTypeName) {
	case "htmlblock", "webpage", "templatebasedemail", "email", "textonlyemail":
		return "html"
	case "textblock", "freeformblock":
		return "html"
	case "jsonblock", "jsonmessage":
		return "json"
	case "codesnippetblock", "codesnippet":
		return "txt"
	default:
		return "txt"
	}
}

// ---------- mcecli de dump ----------

func deDump(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("de dump", flag.ContinueOnError)
	var c common
	var outDir, maxAge string
	addCommon(fs, &c)
	fs.IntVar(&c.size, "size", 500, "rows per page (default 500)")
	fs.StringVar(&outDir, "out", "", "output directory (default ~/.mcecli/work/<profile>/de)")
	fs.StringVar(&outDir, "path", "", "alias for --out")
	fs.BoolVar(&c.refresh, "refresh", false, "re-pull even if a local dump exists")
	fs.StringVar(&maxAge, "max-age", "", "serve cache only if newer than this (e.g. 30m, 2h); otherwise re-pull")
	if err := parseCmd(fs, args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprint(stderr, "usage: mcecli de dump <key|name> [--out DIR] [--refresh]\n")
		return exitUsage
	}
	name := fs.Arg(0)
	profile := currentProfileName(&c)

	out := filepath.Join(workRoot(profile), "de")
	if outDir != "" {
		out = outDir
	}
	target := filepath.Join(out, slugify(name)+".ndjson")
	metaPath := target + ".meta.json"

	// cache-first (with optional freshness bound)
	if !c.refresh {
		if meta := readMeta(metaPath); meta != nil && cacheFreshEnough(meta, maxAge) {
			return printDumpEnvelope(stdout, target, meta, "served from disk cache (fetched "+cacheAgeLabel(int64(meta["fetchedAt"].(float64)))+") — --max-age/--refresh re-pulls")
		}
	}

	s, env, code := newSession(&c)
	if env != nil {
		_ = output.Print(env, c.pretty, stdout)
		return code
	}

	// resolve ONCE, strictly (ambiguous names are refused, never guessed)
	m, denv := resolveDE(s, name)
	if denv != nil {
		_ = output.Print(denv, c.pretty, stdout)
		return exitAPI
	}
	base := rowsetPathForKey(m.Key, 0, c.size)
	status, resp, env2, code := s.call(http.MethodGet, base, nil, nil)
	if env2 != nil {
		_ = output.Print(env2, c.pretty, stdout)
		return code
	}
	if status >= 400 {
		_ = output.Print(errorEnvelope(status, resp, http.MethodGet), c.pretty, stdout)
		return exitAPI
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		_ = output.Print(output.Fail(0, err.Error(), ""), c.pretty, stdout)
		return exitCfg
	}
	f, err := os.Create(target)
	if err != nil {
		_ = output.Print(output.Fail(0, err.Error(), ""), c.pretty, stdout)
		return exitCfg
	}

	rows := 0
	var total int
	next := ""
	pageResp := resp
	const maxPages = 5000 // hard stop against server-side continuation loops
	for page := 0; page < maxPages; page++ {
		raw := parseJSON(pageResp)
		m, _ := raw.(map[string]any)
		if n, ok := m["count"].(float64); ok {
			total = int(n)
		}
		if mm, ok := m["items"].([]any); ok {
			for _, row := range mergeRowItems(mm) {
				line, _ := json.Marshal(row)
				f.Write(line)
				f.Write([]byte("\n"))
				rows++
			}
		}
		if links, ok := m["links"].(map[string]any); ok {
			if nl, ok := links["next"].(string); ok {
				next = resolveNext(nl)
			} else {
				next = ""
			}
		} else {
			next = ""
		}
		if next == "" {
			break
		}
		if total > 0 && rows >= total {
			break
		}
		status, pageResp, env2, code = s.call(http.MethodGet, next, nil, nil)
		if env2 != nil || status >= 400 {
			f.Close()
			e := output.Fail(status, "dump interrupted after "+strconv.Itoa(rows)+" rows", "partial file kept: "+target)
			_ = output.Print(e, c.pretty, stdout)
			return exitAPI
		}
	}
	f.Close()

	meta := map[string]any{
		"fetchedAt": time.Now().Unix(), "rows": rows, "total": total,
		"key": name, "customerKey": m.Key, "profile": profile,
	}
	writeMeta(metaPath, meta)
	return printDumpEnvelope(stdout, target, meta, "pulled live; re-asks served from disk until --refresh")
}

func printDumpEnvelope(stdout io.Writer, path string, meta map[string]any, hint string) int {
	fi, _ := os.Stat(path)
	data := map[string]any{
		"path": path, "rows": metaInt(meta, "rows"), "fetchedAt": meta["fetchedAt"],
	}
	if fi != nil {
		data["bytes"] = fi.Size()
	}
	e := output.OK(0, data)
	e.Hint = hint + " — grep/jq the file locally instead of re-querying"
	_ = output.Print(e, false, stdout)
	return exitOK
}

// cacheFreshEnough reports whether the sidecar satisfies --max-age.
// Empty maxAge means "any age is fine".
func cacheFreshEnough(meta map[string]any, maxAge string) bool {
	if strings.TrimSpace(maxAge) == "" {
		return true
	}
	d, err := time.ParseDuration(maxAge)
	if err != nil {
		return true // unparseable bound: don't block the cached copy
	}
	fa := metaInt(meta, "fetchedAt")
	return time.Since(time.Unix(int64(fa), 0)) <= d
}

// metaInt reads a sidecar value as int regardless of int/float64 storage.
func metaInt(meta map[string]any, k string) int {
	switch v := meta[k].(type) {
	case int:
		return v
	case float64:
		return int(v)
	}
	return 0
}

// readMeta loads a dump sidecar; returns nil if missing/broken.
func readMeta(path string) map[string]any {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	return m
}

func writeMeta(path string, meta map[string]any) {
	if b, err := json.Marshal(meta); err == nil {
		_ = os.WriteFile(path, b, 0o600)
	}
}

// ---------- mcecli asset ----------

func cmdAsset(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, "usage: mcecli asset search --name X [--pull] | mcecli asset pull <id>\n")
		return exitUsage
	}
	switch args[0] {
	case "search":
		return assetSearch(args[1:], stdout, stderr)
	case "pull":
		return assetPull(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown 'asset' subcommand %q\n", args[0])
		return exitUsage
	}
}

func assetSearch(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("asset search", flag.ContinueOnError)
	var c common
	var name, outDir string
	var pull bool
	addCommon(fs, &c)
	fs.StringVar(&name, "name", "", "name contains (REQUIRED)")
	fs.IntVar(&c.size, "size", 25, "max results")
	fs.BoolVar(&pull, "pull", false, "also download each asset body locally")
	fs.StringVar(&outDir, "out", "", "output directory")
	fs.StringVar(&outDir, "path", "", "alias for --out")
	fs.BoolVar(&c.refresh, "refresh", false, "re-pull even if cached")
	help := addHelp(fs)
	if err := parseCmd(fs, args); err != nil {
		return exitUsage
	}
	if *help {
		fmt.Fprint(stdout, usageAsset)
		return exitOK
	}
	if name == "" {
		_ = output.Print(output.Fail(0, "--name is required", `mcecli asset search --name header`), c.pretty, stdout)
		return exitUsage
	}

	profile := currentProfileName(&c)
	out := filepath.Join(workRoot(profile), "asset")
	if outDir != "" {
		out = outDir
	}
	slug := "search-" + slugify(name)
	indexPath := filepath.Join(out, slug+".json")
	metaPath := indexPath + ".meta.json"

	if !pull && !c.refresh {
		if meta := readMeta(metaPath); meta != nil {
			return printDumpEnvelope(stdout, indexPath, meta, "served from disk cache (fetched "+cacheAgeLabel(int64(meta["fetchedAt"].(float64)))+") — --refresh re-pulls, --pull downloads bodies")
		}
	}

	s, env, code := newSession(&c)
	if env != nil {
		_ = output.Print(env, c.pretty, stdout)
		return code
	}
	adv := scopeAdvisory(s.tok, "documents_and_images_read", "saved_content_read")
	q := "$filter=name%20like%20'" + url.QueryEscape(name) + "'&$pageSize=" + strconv.Itoa(c.size) +
		"&$fields=id,name,customerKey,assetType,fileProperties,createdDate,modifiedDate,category"
	status, resp, env2, code := s.call(http.MethodGet, "asset/v1/content/assets?"+q, nil, nil)
	if env2 != nil {
		_ = output.Print(env2, c.pretty, stdout)
		return code
	}
	if status >= 400 {
		_ = output.Print(errorEnvelope(status, resp, http.MethodGet), c.pretty, stdout)
		return exitAPI
	}

	if err := os.MkdirAll(out, 0o700); err != nil {
		_ = output.Print(output.Fail(0, err.Error(), ""), c.pretty, stdout)
		return exitCfg
	}
	_ = os.WriteFile(indexPath, resp, 0o600)

	var downloaded int
	if pull {
		// comma-ok chain: a non-object API response must not panic
		var items []any
		if mo, ok := parseJSON(resp).(map[string]any); ok {
			items, _ = mo["items"].([]any)
		}
		for _, it := range items {
			m, ok := it.(map[string]any)
			if !ok {
				continue
			}
			id := assetIDString(m["id"])
			if id == "" {
				continue
			}
			_ = assetPullInternal(s, out, id, &downloaded)
		}
	}

	meta := map[string]any{"fetchedAt": time.Now().Unix(), "rows": countItemsIn(resp), "query": name, "profile": profile}
	writeMeta(metaPath, meta)
	e := output.OK(200, map[string]any{"path": indexPath, "count": countItemsIn(resp), "downloaded": downloaded})
	e.Hint = "grep the index locally; --pull downloads bodies; --refresh re-pulls"
	if adv != "" {
		e.Hint = adv + " — " + e.Hint
	}
	_ = output.Print(e, c.pretty, stdout)
	return exitOK
}

// assetIDString normalizes asset ids (SFMC returns them as JSON numbers).
func assetIDString(v any) string {
	switch id := v.(type) {
	case string:
		return id
	case float64:
		return strconv.Itoa(int(id))
	}
	return ""
}

func assetPull(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("asset pull", flag.ContinueOnError)
	var c common
	var outDir string
	addCommon(fs, &c)
	fs.StringVar(&outDir, "out", "", "output directory")
	fs.BoolVar(&c.refresh, "refresh", false, "re-pull even if cached")
	if err := parseCmd(fs, args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprint(stderr, "usage: mcecli asset pull <id> [--out DIR]\n")
		return exitUsage
	}
	out := filepath.Join(workRoot(currentProfileName(&c)), "asset")
	if outDir != "" {
		out = outDir
	}
	s, env, code := newSession(&c)
	if env != nil {
		_ = output.Print(env, c.pretty, stdout)
		return code
	}
	n := 0
	if err := assetPullInternal(s, out, fs.Arg(0), &n); err != nil {
		_ = output.Print(output.Fail(0, err.Error(), "mcecli asset pull <numeric id>"), c.pretty, stdout)
		return exitAPI
	}
	e := output.OK(200, map[string]any{"path": out, "files": n})
	e.Hint = "review files locally; meta.json holds the full asset"
	_ = output.Print(e, c.pretty, stdout)
	return exitOK
}

// assetPullInternal downloads one asset: meta.json + body (content or file).
func assetPullInternal(s *session, outDir, id string, counter *int) error {
	dir := filepath.Join(outDir, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	status, resp, env2, _ := s.call(http.MethodGet, "asset/v1/content/assets/"+url.PathEscape(id), nil, nil)
	if env2 != nil {
		return fmt.Errorf("%s", string(resp))
	}
	if status >= 400 {
		return fmt.Errorf("HTTP %d fetching asset %s", status, id)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), resp, 0o600); err != nil {
		return err
	}

	m, _ := parseJSON(resp).(map[string]any)
	ext := "txt"
	if at, ok := m["assetType"].(map[string]any); ok {
		if n, ok := at["name"].(string); ok {
			ext = assetExt(n)
		}
	}
	// Priority 1: publishedURL (full CDN URL — mcdev pattern). Write it to url.txt
	// and try to fetch it as binary directly. Agents can use it without any
	// SFMC API call. Falls back silently to url.txt if fetch fails.
	if pu, ok := m["publishedURL"].(string); ok && pu != "" {
		_ = os.WriteFile(filepath.Join(dir, "url.txt"), []byte(pu), 0o600)
		if purl, perr := url.Parse(pu); perr == nil && purl.Scheme != "" {
			if fresp, gerr := http.DefaultClient.Get(pu); gerr == nil && fresp.StatusCode < 400 {
				defer fresp.Body.Close()
				if body, rerr := io.ReadAll(fresp.Body); rerr == nil && len(body) > 0 {
					fe := ext
					if fp, ok := m["fileProperties"].(map[string]any); ok {
						if x, ok := fp["extension"].(string); ok && x != "" {
							fe = strings.TrimPrefix(strings.ToLower(x), ".")
						}
					}
					if werr := os.WriteFile(filepath.Join(dir, "file."+fe), body, 0o600); werr == nil {
						*counter++
						return nil
					}
				}
			}
		}
		// Even if the CDN fetch failed, the URL is written — agent can use it directly
		*counter++
		return nil
	}
	// Priority 2: inline content (htmlblock, textblock, webpage bodies)
	if content, ok := m["content"].(string); ok && content != "" {
		if err := os.WriteFile(filepath.Join(dir, "body."+ext), []byte(content), 0o600); err != nil {
			return err
		}
		*counter++
		return nil
	}
	// Priority 3: binary file endpoint (images, pdfs, etc.) — /file returns
	// base64 wrapped in JSON quotes on some tenants; decode when detected.
	fstatus, fb, env2, _ := s.call(http.MethodGet, "asset/v1/content/assets/"+url.PathEscape(id)+"/file", nil, nil)
	if env2 == nil && fstatus == http.StatusOK && len(fb) > 0 {
		fe := "bin"
		if fp, ok := m["fileProperties"].(map[string]any); ok {
			if x, ok := fp["extension"].(string); ok && x != "" {
				fe = strings.TrimPrefix(strings.ToLower(x), ".")
			}
		}
		data := fb
		if strings.HasPrefix(strings.TrimSpace(string(fb)), `"`) {
			var b64 string
			if json.Unmarshal(fb, &b64) == nil {
				if dec, derr := base64.StdEncoding.DecodeString(b64); derr == nil {
					data = dec
				}
			}
		}
		if err := os.WriteFile(filepath.Join(dir, "file."+fe), data, 0o600); err != nil {
			return err
		}
		*counter++
	}
	return nil
}
