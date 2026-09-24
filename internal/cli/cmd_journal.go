// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

// journal: read access to the write audit trail.

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/dawidmachon/mcecli/internal/journal"
	"github.com/dawidmachon/mcecli/internal/output"
)

const usageJournal = `mcecli journal — read the write audit trail (~/.mcecli/journal.jsonl)

Every gated write (tier 1 + tier 2) is recorded: timestamp, profile, BU,
method, URL, response status, body hash/size, async request id, duration.
Request BODIES are intentionally not stored (privacy).

  mcecli journal [--last N] [--profile P] [--json]

Examples:
  mcecli journal --last 20
  mcecli journal --profile dev --json
`

func cmdJournal(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("journal", flag.ContinueOnError)
	var last int
	var prof string
	var asJSON bool
	fs.IntVar(&last, "last", 20, "show at most N most recent entries")
	fs.StringVar(&prof, "profile", "", "filter by profile (case-insensitive)")
	fs.BoolVar(&asJSON, "json", false, "print raw JSON lines instead of the envelope")
	help := addHelp(fs)
	if err := parseCmd(fs, args); err != nil {
		return exitUsage
	}
	if *help {
		fmt.Fprint(stdout, usageJournal)
		return exitOK
	}

	entries, err := journal.Last(last, prof)
	if err != nil {
		_ = output.Print(output.Fail(0, err.Error(), ""), asJSON, stdout)
		return exitAPI
	}

	if asJSON {
		for _, e := range entries {
			if b, err := json.Marshal(e); err == nil {
				fmt.Fprintln(stdout, string(b))
			}
		}
		return exitOK
	}

	data := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		data = append(data, map[string]any{
			"ts": e.TS, "profile": e.Profile, "bu": e.BU, "mid": e.MID,
			"method": e.Method, "url": e.URL, "status": e.Status,
			"request_id": e.RequestID, "duration_ms": e.DurationMS,
			"body_sha256": e.BodySHA256,
		})
	}
	e := output.OK(0, data)
	e.Count = len(data)
	if len(data) == 0 {
		e.Hint = "journal is empty — entries appear after gated writes (mcecli de add / mcecli rest --write)"
	} else {
		e.Hint = fmt.Sprintf("showing %d most recent; total entries: %d", len(data), journal.Count())
	}
	_ = output.Print(e, false, stdout)
	return exitOK
}
