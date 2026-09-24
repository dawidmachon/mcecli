// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

// diff: compare a local NDJSON dump against live rows — drift detection.

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/dawidmachon/mcecli/internal/output"
	"github.com/dawidmachon/mcecli/internal/soap"
)

const usageDeDiff = `mcecli de diff — compare a local NDJSON dump against live rows

  mcecli de diff <key|name> --against dump.ndjson [--pk RowId] [--size N]

Reads the local NDJSON file and compares it against the live DE rows.
Reports: only_in_dump (deleted from SFMC), only_in_live (added after dump),
changed (same PK, different values). Matches rows by PK field values.
Requires the DE to have PK fields; --pk overrides the PK field name(s).

Examples:
  mcecli de diff scratch_de --against ~/.mcecli/work/<profile>/de/mcx_scratch.ndjson
  mcecli de diff MyDE --against old_dump.ndjson --pk Email
`

func deDiff(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("de diff", flag.ContinueOnError)
	var c common
	var against, pkOverride string
	var size int
	addCommon(fs, &c)
	fs.StringVar(&against, "against", "", "local NDJSON file to compare against (REQUIRED)")
	fs.StringVar(&pkOverride, "pk", "", "PK field name(s), comma-separated (auto-detected from DE schema if omitted)")
	fs.IntVar(&size, "size", 500, "page size for live retrieval")
	help := addHelp(fs)
	if err := parseCmd(fs, args); err != nil {
		return exitUsage
	}
	if *help {
		fmt.Fprint(stdout, usageDeDiff)
		return exitOK
	}
	if fs.NArg() != 1 || against == "" {
		fmt.Fprint(stderr, usageDeDiff)
		return exitUsage
	}
	if _, err := os.Stat(against); err != nil {
		_ = output.Print(output.Fail(0, "--against file not found: "+against, ""), c.pretty, stdout)
		return exitUsage
	}

	key := fs.Arg(0)

	// read local dump
	localRows, err := readNDJSON(against)
	if err != nil {
		_ = output.Print(output.Fail(0, "read dump: "+err.Error(), ""), c.pretty, stdout)
		return exitUsage
	}

	// fetch live rows
	s, env, code := newSession(&c)
	if env != nil {
		_ = output.Print(env, c.pretty, stdout)
		return code
	}
	var liveRows []map[string]string
	if len(pkOverrideFields(pkOverride)) > 0 {
		// SOAP filtered retrieve per PK set — for each unique PK in the dump
		pkNames := pkOverrideFields(pkOverride)
		for _, lr := range localRows {
			pk := map[string]string{}
			for _, f := range pkNames {
				if v, ok := lr["(key) "+f]; ok {
					pk[f] = v
				} else if v, ok := lr[f]; ok {
					pk[f] = v
				}
			}
			if len(pk) == 0 {
				continue
			}
			rows, err := soapRetrieveFiltered(s, key, pk)
			if err != nil {
				continue
			}
			for _, r := range rows {
				liveRows = append(liveRows, r)
			}
		}
	} else {
		// fetch all live rows via REST token pagination
		liveRows, err = fetchAllLiveRows(s, key, size)
		if err != nil {
			_ = output.Print(output.Fail(0, err.Error(), "check mcecli de rows "+key), c.pretty, stdout)
			return exitAPI
		}
	}

	// build PK-based maps
	pkKey := func(m map[string]string) string {
		var parts []string
		for k, v := range m {
			if strings.HasPrefix(k, "(key) ") {
				parts = append(parts, strings.TrimPrefix(k, "(key) ")+"="+v)
			}
		}
		if len(parts) == 0 {
			return ""
		}
		sort.Strings(parts)
		return strings.Join(parts, ",")
	}

	localMap := map[string]map[string]string{}
	noPKLocal := 0
	for _, r := range localRows {
		k := pkKey(r)
		if k == "" {
			noPKLocal++
			continue
		}
		localMap[k] = r
	}
	liveMap := map[string]map[string]string{}
	noPKLive := 0
	for _, r := range liveRows {
		k := pkKey(r)
		if k == "" {
			noPKLive++
			continue
		}
		liveMap[k] = r
	}

	var onlyDump, onlyLive, changed []string
	for k := range localMap {
		if _, ok := liveMap[k]; !ok {
			onlyDump = append(onlyDump, k)
		}
	}
	for k, lv := range liveMap {
		lv2, ok := localMap[k]
		if !ok {
			onlyLive = append(onlyLive, k)
		} else if !rowsEqual(lv2, lv) {
			changed = append(changed, k)
		}
	}

	sort.Strings(onlyDump)
	sort.Strings(onlyLive)
	sort.Strings(changed)

	inSync := len(onlyDump) == 0 && len(onlyLive) == 0 && len(changed) == 0
	data := map[string]any{
		"in_sync":      inSync,
		"local_rows":   len(localRows),
		"live_rows":    len(liveRows),
		"only_in_dump": onlyDump,
		"only_in_live": onlyLive,
		"changed":      changed,
	}
	if noPKLocal+noPKLive > 0 {
		data["rows_without_pk"] = noPKLocal + noPKLive
	}
	e := output.OK(200, data)
	e.Hint = "in_sync=true means the dump matches live exactly"
	if !inSync {
		e.Hint = fmt.Sprintf("drift detected: %d only in dump, %d only in live, %d changed", len(onlyDump), len(onlyLive), len(changed))
	}
	if noPKLocal+noPKLive > 0 {
		e.Hint += fmt.Sprintf("; %d rows had no PK values (comparison skipped)", noPKLocal+noPKLive)
	}
	_ = output.Print(e, c.pretty, stdout)
	return exitOK
}

// readNDJSON reads a newline-delimited JSON file into a slice of maps.
func readNDJSON(path string) ([]map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []map[string]string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) != nil {
			continue
		}
		row := map[string]string{}
		for k, v := range m {
			row[k] = fmt.Sprint(v)
		}
		out = append(out, row)
	}
	return out, sc.Err()
}

// fetchAllLiveRows pages through all rows of a DE via the token continuation.
func fetchAllLiveRows(s *session, key string, size int) ([]map[string]string, error) {
	var all []map[string]string
	path := rowsetPathForKey(key, 1, size)
	for page := 0; page < 500; page++ {
		status, resp, env2, _ := s.call(http.MethodGet, path, nil, nil)
		if env2 != nil {
			return all, fmt.Errorf("transport error")
		}
		if status >= 400 {
			return all, fmt.Errorf("HTTP %d", status)
		}
		m, ok := parseJSON(resp).(map[string]any)
		if !ok {
			break
		}
		if items, ok := m["items"].([]any); ok {
			for _, it := range items {
				if im, ok := it.(map[string]any); ok {
					row := map[string]string{}
					if keys, ok := im["keys"].(map[string]any); ok {
						for k, v := range keys {
							row["(key) "+k] = fmt.Sprint(v)
						}
					}
					if vals, ok := im["values"].(map[string]any); ok {
						for k, v := range vals {
							row[k] = fmt.Sprint(v)
						}
					}
					all = append(all, row)
				}
			}
		}
		if links, ok := m["links"].(map[string]any); ok {
			if nl, ok := links["next"].(string); ok {
				path = resolveNext(nl)
			} else {
				break
			}
		} else {
			break
		}
	}
	return all, nil
}

func rowsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func pkOverrideFields(pk string) []string {
	if strings.TrimSpace(pk) == "" {
		return nil
	}
	var out []string
	for _, f := range strings.Split(pk, ",") {
		if t := strings.TrimSpace(f); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func soapRetrieveFiltered(s *session, key string, pk map[string]string) ([]map[string]string, error) {
	return soap.RetrieveDERows(context.Background(), s.res.SoapURL(), s.tok.AccessToken, key, pk)
}
