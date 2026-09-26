// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"flag"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/dawidmachon/mcecli/internal/config"
	"github.com/dawidmachon/mcecli/internal/output"
)

// Work-cache hygiene (roadmap idea, v1.2). ~/.mcecli/work/<profile>/ holds
// asset dumps, de dump output and undo snapshots — all disposable EXCEPT
// undo images (rollback ability). Prune is REPORT-ONLY by default; --do
// deletes. The journal (~/.mcecli/journal.jsonl) lives outside work/ and is
// never touched — it is the audit trail.

const usageWork = `mcecli work — local work-cache hygiene (~/.mcecli/work/)

  mcecli work prune               — REPORT entries older than the cutoff
               [--older-than 30d] — age cutoff (30m/24h/7d/90d; default 30d)
               [--do]             — actually delete (report is the default)
               [--profile NAME]   — limit to one profile's cache

Undo images are disposable too — deleting one removes the rollback path
for that DELETE (mcecli undo list shows what remains). The write journal
(~/.mcecli/journal.jsonl) is NEVER pruned.
`

func cmdWork(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageWork)
		return exitUsage
	}
	switch args[0] {
	case "prune":
		return workPrune(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usageWork)
		return exitOK
	default:
		fmt.Fprintf(stderr, "unknown 'work' subcommand %q\n\n%s", args[0], usageWork)
		return exitUsage
	}
}

func parseAge(v string) (time.Duration, error) {
	m := regexp.MustCompile(`^(\d+)(m|h|d)$`).FindStringSubmatch(strings.ToLower(v))
	if m == nil {
		return 0, fmt.Errorf("bad --older-than %q", v)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("bad --older-than %q", v)
	}
	switch m[2] {
	case "m":
		return time.Duration(n) * time.Minute, nil
	case "h":
		return time.Duration(n) * time.Hour, nil
	default: // d
		return time.Duration(n) * 24 * time.Hour, nil
	}
}

// dirSize sums a file or (recursively) a directory tree.
func dirSize(path string) int64 {
	var total int64
	_ = filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, ierr := d.Info(); ierr == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

type pruneItem struct {
	path     string
	profile  string
	category string
	size     int64
	age      time.Duration
}

func workPrune(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("work prune", flag.ContinueOnError)
	var c common
	var olderThan string
	var do bool
	addCommon(fs, &c)
	fs.StringVar(&olderThan, "older-than", "30d", "age cutoff (30m/24h/7d/90d)")
	fs.BoolVar(&do, "do", false, "actually delete (default is a report only)")
	help := addHelp(fs)
	if err := parseCmd(fs, args); err != nil {
		return exitUsage
	}
	if *help {
		fmt.Fprint(stdout, usageWork)
		return exitOK
	}
	age, aerr := parseAge(olderThan)
	if aerr != nil {
		e := output.Fail(0, aerr.Error(), "form: <n>m | <n>h | <n>d — e.g. 30d")
		_ = output.Print(e, c.pretty, stdout)
		return exitUsage
	}
	cutoff := time.Now().Add(-age)

	workDir := filepath.Join(config.Dir(), "work")
	profiles, _ := os.ReadDir(workDir)

	candidates := []pruneItem{}
	for _, pd := range profiles {
		if !pd.IsDir() {
			continue
		}
		if c.profile != "" && pd.Name() != c.profile {
			continue
		}
		profDir := filepath.Join(workDir, pd.Name())
		cats, _ := os.ReadDir(profDir)
		for _, cd := range cats {
			if !cd.IsDir() {
				continue
			}
			catDir := filepath.Join(profDir, cd.Name())
			leaves, _ := os.ReadDir(catDir)
			for _, leaf := range leaves {
				p := filepath.Join(catDir, leaf.Name())
				info, ierr := leaf.Info()
				if ierr != nil || !info.ModTime().Before(cutoff) {
					continue
				}
				candidates = append(candidates, pruneItem{
					path: p, profile: pd.Name(), category: cd.Name(),
					size: dirSize(p), age: time.Since(info.ModTime()),
				})
			}
		}
	}

	if !do {
		rows := make([]any, 0, len(candidates))
		var total int64
		for _, it := range candidates {
			rows = append(rows, map[string]any{
				"profile":  it.profile,
				"category": it.category,
				"path":     it.path,
				"bytes":    it.size,
				"ageDays":  math.Round(it.age.Hours()/24*10) / 10,
			})
			total += it.size
		}
		e := output.OK(200, rows)
		e.Count = len(rows)
		if len(rows) == 0 {
			e.Hint = fmt.Sprintf("nothing older than %s in the scanned work caches", olderThan)
		} else {
			e.Hint = fmt.Sprintf("REPORT ONLY — %d entries, %d bytes reclaimable — re-run with --do to delete; undo images included (deleting removes that rollback path)", len(rows), total)
		}
		_ = output.Print(e, c.pretty, stdout)
		return exitOK
	}

	removed, freed := 0, int64(0)
	for _, it := range candidates {
		if os.RemoveAll(it.path) == nil {
			removed++
			freed += it.size
		}
	}
	e := output.OK(200, map[string]any{
		"removed":    removed,
		"candidates": len(candidates),
		"freedBytes": freed,
	})
	e.Count = removed
	e.Hint = fmt.Sprintf("pruned %d of %d entries (%d bytes) — journal untouched; mcecli undo list shows surviving rollback images", removed, len(candidates), freed)
	_ = output.Print(e, c.pretty, stdout)
	return exitOK
}
