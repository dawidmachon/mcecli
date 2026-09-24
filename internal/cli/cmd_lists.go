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

// List-centric operational reads (SOAP List / ListSubscriber).
// VERIFIED live 2026-09-17: List [ID, ListName, Type]; ListSubscriber
// via SubscriberKey filter (ListSubscriber by ListID NREs on this tenant —
// wire-style-sensitive, not a scope issue).

const usageLists = `mcecli lists — subscriber list reads (read-only, SOAP)

  mcecli lists                   — lists in this context (All Subscribers, …)
               [--search STR] — filter on name
  mcecli lists members <key>     — which lists a subscriber is on (ListSubscriber
               --limit N]    — max rows (default 500; subscribers can be on many lists)

Per-subscriber detail: mcecli sub <key|email> (gets all per-list status)
`

func cmdLists(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return listsAll(nil, stdout, stderr)
	}
	switch args[0] {
	case "members":
		return listsMembers(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usageLists)
		return exitOK
	default:
		return listsAll(args, stdout, stderr)
	}
}

func listsAll(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("lists", flag.ContinueOnError)
	var c common
	var search string
	addCommon(fs, &c)
	fs.StringVar(&search, "search", "", "client-side filter on list name")
	if err := parseCmd(fs, args); err != nil {
		return exitUsage
	}

	s, env, code := newSession(&c)
	if env != nil {
		_ = output.Print(env, c.pretty, stdout)
		return code
	}
	rows, status, err := soap.Retrieve(context.Background(), s.res.SoapURL(), s.tok.AccessToken,
		"List", []string{"ID", "ListName", "Type", "Description"}, soap.Opts{MaxRows: 500})
	if err != nil {
		// retry without Description (not all contexts expose it)
		rows, status, err = soap.Retrieve(context.Background(), s.res.SoapURL(), s.tok.AccessToken,
			"List", []string{"ID", "ListName", "Type"}, soap.Opts{MaxRows: 500})
	}
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
	out := make([]map[string]string, 0, len(rows))
	for _, r := range rows {
		if search != "" && !strings.Contains(strings.ToLower(r["ListName"]), strings.ToLower(search)) {
			continue
		}
		out = append(out, r)
	}
	e := output.OK(200, out)
	e.Count = len(out)
	e.Hint = "which lists a subscriber is on: mcecli lists members <subscriberKey> — state detail: mcecli sub <key|email>"
	_ = output.Print(e, c.pretty, stdout)
	return exitOK
}

func listsMembers(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("lists members", flag.ContinueOnError)
	var c common
	var limit int
	addCommon(fs, &c)
	fs.IntVar(&limit, "limit", 500, "max rows (subscribers can be on many lists)")
	if err := parseCmd(fs, args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprint(stderr, "usage: mcecli lists members <subscriberKey> [--limit N]\n")
		return exitUsage
	}
	key := fs.Arg(0)

	s, env, code := newSession(&c)
	if env != nil {
		_ = output.Print(env, c.pretty, stdout)
		return code
	}
	rows, status, err := soap.Retrieve(context.Background(), s.res.SoapURL(), s.tok.AccessToken,
		"ListSubscriber", []string{"ListID", "SubscriberKey", "Status", "CreatedDate"},
		soap.Opts{
			Filters: []soap.Filter{{Prop: "SubscriberKey", Op: "equals", Value: key}},
			MaxRows: limit,
		})
	if err != nil {
		e := output.Fail(0, err.Error(), "wire check: set MCECLI_SOAP_DEBUG=1 and retry")
		_ = output.Print(e, c.pretty, stdout)
		return exitAPI
	}
	if strings.HasPrefix(status, "Error") {
		e := output.Fail(0, "SOAP OverallStatus: "+status, "check the subscriberKey")
		_ = output.Print(e, c.pretty, stdout)
		return exitAPI
	}
	truncated := false
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
		truncated = true
	}
	if rows == nil {
		rows = []map[string]string{}
	}
	e := output.OK(200, rows)
	e.Count = len(rows)
	hint := "Status per list: Active / Unsubscribed"
	if truncated {
		hint += " — TRUNCATED to --limit"
	}
	e.Hint = hint
	_ = output.Print(e, c.pretty, stdout)
	return exitOK
}
