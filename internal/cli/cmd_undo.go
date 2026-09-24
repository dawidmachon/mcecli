// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

// undo: access to before-image snapshots captured before DELETE operations.
// Rollback is intentionally manual — the agent (or user) re-applies the
// saved state with the existing gated commands (rest POST/PUT, de add).
// mcecli never auto-executes a rollback.

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/dawidmachon/mcecli/internal/config"
	"github.com/dawidmachon/mcecli/internal/output"
)

const usageUndo = `mcecli undo — list/show before-image snapshots captured before DELETEs

  mcecli undo list [--profile P]      snapshots for a profile (newest first)
  mcecli undo show <stamp-dir>        print the saved request + response

Rollback is MANUAL by design: re-create the resource with the saved JSON
using the gated commands (mcecli rest POST ... --write, mcecli de add, ...).
Every DELETE automatically captures a snapshot; the journal links to it.

Examples:
  mcecli undo list --profile <name>
  mcecli undo show 20260913-190101.000000000-DELETE
`

func cmdUndo(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("undo", flag.ContinueOnError)
	var prof string
	fs.StringVar(&prof, "profile", "", "filter by profile")
	help := addHelp(fs)
	if err := parseCmd(fs, args); err != nil {
		return exitUsage
	}
	if *help {
		fmt.Fprint(stdout, usageUndo)
		return exitOK
	}

	if len(args) == 0 || args[0] == "list" {
		return undoList(prof, stdout)
	}
	if args[0] == "show" {
		if len(args) < 2 {
			fmt.Fprint(stderr, "usage: mcecli undo show <stamp-dir>\n")
			return exitUsage
		}
		return undoShow(args[1], stdout)
	}
	fmt.Fprintf(stderr, "unknown 'undo' subcommand %q\n", args[0])
	return exitUsage
}

// undoRoot returns the undo dirs for one profile, newest first.
func undoRoot(profile string) (string, []os.DirEntry) {
	root := filepath.Join(config.Dir(), "work")
	if profile != "" {
		cfg, err := config.Load()
		if err == nil {
			if p, canonical := config.FindProfile(cfg, profile); p != nil {
				root = filepath.Join(root, canonical)
			}
		}
	}
	undoDir := filepath.Join(root, "undo")
	entries, err := os.ReadDir(undoDir)
	if err != nil {
		return undoDir, nil
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() > entries[j].Name() })
	return undoDir, entries
}

func undoList(profile string, stdout io.Writer) int {
	root, entries := undoRoot(profile)
	data := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		req, _ := os.ReadFile(filepath.Join(root, e.Name(), "request.json"))
		data = append(data, map[string]any{
			"dir":   e.Name(),
			"saved": string(req),
		})
	}
	e := output.OK(0, data)
	e.Count = len(data)
	if len(data) == 0 {
		e.Hint = "no snapshots yet — DELETE operations capture before-images automatically"
	} else {
		e.Hint = "rollback is manual: re-create with the saved JSON via gated commands (mcecli rest POST ... --write)"
	}
	_ = output.Print(e, false, stdout)
	return exitOK
}

func undoShow(stamp string, stdout io.Writer) int {
	// search all profiles for the stamp
	workRoot := filepath.Join(config.Dir(), "work")
	profiles, _ := os.ReadDir(workRoot)
	for _, p := range profiles {
		if !p.IsDir() {
			continue
		}
		dir := filepath.Join(workRoot, p.Name(), "undo", stamp)
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			req, _ := os.ReadFile(filepath.Join(dir, "request.json"))
			resp, _ := os.ReadFile(filepath.Join(dir, "response.json"))
			e := output.OK(200, map[string]any{
				"profile": p.Name(), "dir": dir,
				"request":  string(req),
				"response": string(resp),
				"hint":     "rollback manually: re-create with mcecli rest POST ... --write (gated)",
			})
			_ = output.Print(e, false, stdout)
			return exitOK
		}
	}
	e := output.Fail(404, "snapshot '"+stamp+"' not found", "mcecli undo list")
	_ = output.Print(e, false, stdout)
	return exitAPI
}
