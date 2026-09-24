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

// Subscriber operational lookups via SOAP (read-only). Useful when debugging
// delivery: "why is this person not getting mail?" → aggregate status,
// unsubscribed date, list memberships with per-list status.
//
// Wire shapes verified against sf-docs-scrap + live 2026-09-16:
// - Subscriber: Properties [SubscriberKey, EmailAddress, Status, ...],
//   filter SubscriberKey|EmailAddress equals
// - ListSubscriber: Properties [ListID, SubscriberKey, Status, CreatedDate],
//   filter SubscriberKey equals → one row per list membership

const usageSub = `mcecli sub — subscriber operational lookups (read-only, SOAP)

  mcecli sub <key|email>         — subscription state + list memberships
               [--no-lists]   — skip the ListSubscriber join call
               [--fields a,b] — project subscriber properties

Answers "why is this person not getting mail?":
  - Status: Active / Bounced / Unsubscribed / Held (Held = 3+ sequential
    bounces over 14+ days — triggered sends skip them)
  - lists: per-list Active/Unsubscribed status (ListSubscriber join)

For event history: mcecli dv sent|bounces|unsubs|clicks --subscriber-key K
`

// subscriberStatusNames maps the numeric SubscriberStatus enum to names.
// (API returns numbers; official enum: Active/Bounced/Held/Unsubscribed.)
var subscriberStatusNames = map[string]string{
	"0": "Unknown",
	"1": "Active",
	"2": "Bounced",
	"3": "Unsubscribed",
	"4": "Held",
}

func cmdSub(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("sub", flag.ContinueOnError)
	var c common
	var noLists, help bool
	var fields string
	addCommon(fs, &c)
	fs.BoolVar(&noLists, "no-lists", false, "skip the ListSubscriber join (subscriber row only)")
	fs.BoolVar(&help, "help", false, "print help")
	fs.StringVar(&fields, "fields", "", "comma-separated subscriber properties to return")
	if err := parseCmd(fs, args); err != nil {
		return exitUsage
	}
	if help || fs.NArg() != 1 {
		fmt.Fprint(stderr, usageSub)
		return exitUsage
	}
	ident := fs.Arg(0)

	subProps := []string{"SubscriberKey", "EmailAddress", "Status", "UnsubscribedDate", "CreatedDate", "EmailTypePreference"}
	if fields != "" {
		subProps = splitFields(fields)
	}

	// email vs key: contains @ → EmailAddress filter, else SubscriberKey
	filterProp := "SubscriberKey"
	if strings.Contains(ident, "@") {
		filterProp = "EmailAddress"
	}

	s, env, code := newSession(&c)
	if env != nil {
		_ = output.Print(env, c.pretty, stdout)
		return code
	}

	rows, status, err := soap.Retrieve(context.Background(), s.res.SoapURL(), s.tok.AccessToken,
		"Subscriber", subProps, soap.Opts{
			Filters: []soap.Filter{{Prop: filterProp, Op: "equals", Value: ident}},
			MaxRows: 2, // duplicate-key guard; we surface the ambiguity
		})
	if err != nil {
		e := output.Fail(0, err.Error(), "wire check: set MCECLI_SOAP_DEBUG=1 and retry")
		_ = output.Print(e, c.pretty, stdout)
		return exitAPI
	}
	if strings.HasPrefix(status, "Error") {
		e := output.Fail(0, "SOAP OverallStatus: "+status, "check the identifier (key vs email)")
		_ = output.Print(e, c.pretty, stdout)
		return exitAPI
	}
	if len(rows) == 0 {
		e := output.Fail(0, fmt.Sprintf("no subscriber found by %s %q", filterProp, ident),
			"find keys via events: mcecli dv sent --limit 5 — or search All Subscribers in the UI")
		e.Status = 404
		_ = output.Print(e, c.pretty, stdout)
		return exitAPI
	}
	// ambiguous email (multiple MID contexts may hold the same address):
	// surface ALL matches — never guess. Agent picks by SubscriberKey.
	if len(rows) > 1 {
		data := map[string]any{"ambiguous_matches": rows}
		e := output.OK(200, data)
		e.Count = len(rows)
		e.Hint = fmt.Sprintf("AMBIGUOUS: %d subscribers match %q (same address on multiple MID contexts) — re-run with the exact SubscriberKey to get lists", len(rows), ident)
		_ = output.Print(e, c.pretty, stdout)
		return exitOK
	}

	sub := rows[0]

	// resolve the canonical key (email lookups must join by the returned key)
	subKey := sub["SubscriberKey"]
	if subKey == "" {
		subKey = ident
	}

	data := map[string]any{"subscriber": sub}
	var lists []map[string]string
	listCount := 0
	if !noLists {
		lists, status, err = soap.Retrieve(context.Background(), s.res.SoapURL(), s.tok.AccessToken,
			"ListSubscriber", []string{"ListID", "SubscriberKey", "Status", "CreatedDate"},
			soap.Opts{
				Filters: []soap.Filter{{Prop: "SubscriberKey", Op: "equals", Value: subKey}},
				MaxRows: 500,
			})
		if err != nil {
			// subscriber lookup succeeded — don't fail the whole command
			data["lists_error"] = err.Error()
		} else if strings.HasPrefix(status, "Error") {
			data["lists_error"] = status
		} else {
			if lists == nil {
				lists = []map[string]string{}
			}
			listCount = len(lists)
			data["lists"] = lists
		}
	}

	e := output.OK(200, data)
	e.Count = listCount
	statusName := subscriberStatusNames[sub["Status"]]
	statusLine := sub["Status"]
	if statusName != "" {
		statusLine = fmt.Sprintf("%s (%s)", sub["Status"], statusName)
	}
	hint := "status=" + statusLine
	if !noLists {
		hint += fmt.Sprintf(" — %d list memberships; event history: mcecli dv bounces|unsubs|sent --subscriber-key %s", listCount, subKey)
	}
	e.Hint = hint
	_ = output.Print(e, c.pretty, stdout)
	return exitOK
}
