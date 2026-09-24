// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/dawidmachon/mcecli/internal/output"
	"github.com/dawidmachon/mcecli/internal/soap"
)

// TriggeredSendDefinition operational reads (SOAP). VERIFIED live
// 2026-09-17: CustomerKey/Name/TriggeredSendStatus/CategoryID/CreatedDate
// retrievable; IsPaused NOT (docs overstate). Statuses seen: Canceled.

const usageTS = `mcecli ts — triggered send definitions (read-only, SOAP)

  mcecli ts list                 — triggered send definitions
               [--search STR] — client-side filter on name+key
               [--status S]   — client-side filter (Active/Canceled/…)
               [--limit N]    — max rows (default 200)
  mcecli ts get <customerKey>    — one definition (server-side key filter)

Debugging: paused/canceled TS silently swallow sends. Pair with:
mcecli dv sent --subscriber-key K (did events happen at all?)
`

func cmdTS(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageTS)
		return exitUsage
	}
	switch args[0] {
	case "list":
		return tsList(args[1:], stdout, stderr)
	case "get":
		return tsGet(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usageTS)
		return exitOK
	default:
		fmt.Fprintf(stderr, "unknown 'ts' subcommand %q\n\n%s", args[0], usageTS)
		return exitUsage
	}
}

var tsProps = []string{"CustomerKey", "Name", "TriggeredSendStatus", "CategoryID", "CreatedDate"}

func tsList(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ts list", flag.ContinueOnError)
	var c common
	var search, statusFilter string
	var limit int
	addCommon(fs, &c)
	fs.StringVar(&search, "search", "", "client-side filter on name+key")
	fs.StringVar(&statusFilter, "status", "", "client-side filter on TriggeredSendStatus")
	fs.IntVar(&limit, "limit", 200, "max rows after filtering")
	if err := parseCmd(fs, args); err != nil {
		return exitUsage
	}

	s, env, code := newSession(&c)
	if env != nil {
		_ = output.Print(env, c.pretty, stdout)
		return code
	}
	rows, status, err := soap.Retrieve(context.Background(), s.res.SoapURL(), s.tok.AccessToken,
		"TriggeredSendDefinition", tsProps, soap.Opts{MaxRows: 2500})
	if err != nil {
		e := output.Fail(0, err.Error(), "wire check: set MCECLI_SOAP_DEBUG=1 and retry")
		_ = output.Print(e, c.pretty, stdout)
		return exitAPI
	}
	if strings.HasPrefix(status, "Error") {
		e := output.Fail(0, "SOAP OverallStatus: "+status, "")
		_ = output.Print(e, c.pretty, stdout)
		return exitAPI
	}

	out := make([]map[string]string, 0, len(rows))
	for _, r := range rows {
		if search != "" {
			name := strings.ToLower(r["Name"])
			key := strings.ToLower(r["CustomerKey"])
			if !strings.Contains(name, strings.ToLower(search)) && !strings.Contains(key, strings.ToLower(search)) {
				continue
			}
		}
		if statusFilter != "" && !strings.EqualFold(r["TriggeredSendStatus"], statusFilter) {
			continue
		}
		out = append(out, r)
	}
	truncated := false
	if limit > 0 && len(out) > limit {
		out = out[:limit]
		truncated = true
	}
	if out == nil {
		out = []map[string]string{}
	}
	e := output.OK(200, out)
	e.Count = len(out)
	hint := fmt.Sprintf("%d definitions retrieved", len(rows))
	if search != "" || statusFilter != "" {
		hint += " — filtered client-side"
	}
	if truncated {
		hint += " — TRUNCATED to --limit"
	}
	e.Hint = hint
	_ = output.Print(e, c.pretty, stdout)
	return exitOK
}

func tsGet(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ts get", flag.ContinueOnError)
	var c common
	addCommon(fs, &c)
	if err := parseCmd(fs, args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprint(stderr, "usage: mcecli ts get <customerKey>\n")
		return exitUsage
	}
	key := fs.Arg(0)

	s, env, code := newSession(&c)
	if env != nil {
		_ = output.Print(env, c.pretty, stdout)
		return code
	}
	// NOTE: server-side CustomerKey filter on TSD returns 0 rows on this
	// tenant even for existing keys (same unreliable-filter family as DE
	// rows / List.ListID) — scan and match client-side instead.
	rows, status, err := soap.Retrieve(context.Background(), s.res.SoapURL(), s.tok.AccessToken,
		"TriggeredSendDefinition", tsProps, soap.Opts{MaxRows: 2500})
	if err != nil {
		e := output.Fail(0, err.Error(), "")
		_ = output.Print(e, c.pretty, stdout)
		return exitAPI
	}
	if strings.HasPrefix(status, "Error") {
		e := output.Fail(0, "SOAP OverallStatus: "+status, "")
		_ = output.Print(e, c.pretty, stdout)
		return exitAPI
	}
	var match map[string]string
	for _, r := range rows {
		if strings.EqualFold(r["CustomerKey"], key) {
			match = r
			break
		}
	}
	if match == nil {
		e := output.Fail(0, fmt.Sprintf("no triggered send definition with CustomerKey %q (scanned %d)", key, len(rows)), "mcecli ts list --search "+key)
		e.Status = 404
		_ = output.Print(e, c.pretty, stdout)
		return exitAPI
	}
	e := output.OK(200, match)
	e.Hint = "TriggeredSendStatus Canceled/Inactive = sends are NOT going out; events: mcecli dv sent --subscriber-key K"
	_ = output.Print(e, c.pretty, stdout)
	return exitOK
}
