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

// GUC + ESD reads (SOAP).
// VERIFIED live 2026-09-17: GUC 9 rows (ID/Name/CategoryType/CreatedDate);
// ESD 5 defs (CustomerKey/Name/CreatedDate — SendDefinitionStatus NOT
// retrievable on the reference org).

const usageGUC = `mcecli guc — global unsubscribe categories (read-only, SOAP)

  mcecli guc list                — enterprise unsubscribe categories

GlobalUnsubscribeCategory objects control what happens when a subscriber
clicks a global unsubscribe link. GUC referenced by:
  mcecli sub <key|email>  (GlobalUnsubscribeCategory field on subscriber row)
`

func cmdGUC(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "list" || args[0] == "help" || args[0] == "-h" {
		if len(args) > 0 && args[0] == "help" {
			fmt.Fprint(stdout, usageGUC)
			return exitOK
		}
		return gucList(stdout, stderr)
	}
	fmt.Fprintf(stderr, "unknown 'guc' subcommand %q\n\n%s", args[0], usageGUC)
	return exitUsage
}

func gucList(stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("guc list", flag.ContinueOnError)
	var c common
	addCommon(fs, &c)
	if err := parseCmd(fs, []string{}); err != nil {
		return exitUsage
	}

	s, env, code := newSession(&c)
	if env != nil {
		_ = output.Print(env, c.pretty, stdout)
		return code
	}
	rows, status, err := soap.Retrieve(context.Background(), s.res.SoapURL(), s.tok.AccessToken,
		"GlobalUnsubscribeCategory",
		[]string{"ID", "Name", "CategoryType", "CreatedDate"},
		soap.Opts{MaxRows: 100})
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
	if rows == nil {
		rows = []map[string]string{}
	}
	e := output.OK(200, rows)
	e.Count = len(rows)
	e.Hint = "categories referenced by subscribers' GlobalUnsubscribeCategory property — see: mcecli sub <key|email>"
	_ = output.Print(e, c.pretty, stdout)
	return exitOK
}

const usageESD = `mcecli esd — email send definitions (read-only, SOAP)

  mcecli esd list                — send definitions (CustomerKey/Name/CreatedDate)
               [--search STR] — client-side filter on name+key
               [--limit N]    — max rows (default 200)
  mcecli esd get <customerKey>  — one definition

SendDefinitionStatus is NOT retrievable on the reference org. Recent send events:
  mcecli dv sent (event dates, subscriber keys, send IDs)
  mcecli dv send <sendID> (EmailName/Subject metadata)
`

func cmdESD(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" {
		fmt.Fprint(stdout, usageESD)
		return exitOK
	}
	if args[0] == "list" {
		return esdList(args[1:], stdout, stderr)
	}
	if args[0] == "get" {
		return esdGet(args[1:], stdout, stderr)
	}
	fmt.Fprintf(stderr, "unknown 'esd' subcommand %q\n\n%s", args[0], usageESD)
	return exitUsage
}

func esdList(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("esd list", flag.ContinueOnError)
	var c common
	var search string
	var limit int
	addCommon(fs, &c)
	fs.StringVar(&search, "search", "", "client-side filter on name+key")
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
		"EmailSendDefinition",
		[]string{"CustomerKey", "Name", "CreatedDate"},
		soap.Opts{MaxRows: 2500})
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
	hint := "SendDefinitionStatus not retrievable on the reference org — recent send events: mcecli dv sent"
	if truncated {
		hint += " — TRUNCATED to --limit"
	}
	e.Hint = hint
	_ = output.Print(e, c.pretty, stdout)
	return exitOK
}

func esdGet(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("esd get", flag.ContinueOnError)
	var c common
	addCommon(fs, &c)
	if err := parseCmd(fs, args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprint(stderr, "usage: mcecli esd get <customerKey>\n")
		return exitUsage
	}
	key := fs.Arg(0)

	s, env, code := newSession(&c)
	if env != nil {
		_ = output.Print(env, c.pretty, stdout)
		return code
	}
	// Server-side key filter unreliable on ESD in this tenant context.
	// Retrieve all + client-side match (5 defs = trivial cost).
	rows, status, err := soap.Retrieve(context.Background(), s.res.SoapURL(), s.tok.AccessToken,
		"EmailSendDefinition",
		[]string{"CustomerKey", "Name", "CreatedDate"},
		soap.Opts{MaxRows: 2500})
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
	for _, r := range rows {
		if strings.EqualFold(r["CustomerKey"], key) {
			e := output.OK(200, r)
			e.Hint = "SendDefinitionStatus not retrievable — recent events: mcecli dv sent --send-id <ID>"
			_ = output.Print(e, c.pretty, stdout)
			return exitOK
		}
	}
	e := output.Fail(404, fmt.Sprintf("no email send definition with CustomerKey %q (scanned %d)", key, len(rows)), "mcecli esd list")
	_ = output.Print(e, c.pretty, stdout)
	return exitAPI
}
