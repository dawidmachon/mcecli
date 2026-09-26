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

// Generic SOAP reads (v1.1 SOAP passthrough redesign).
//
// The first passthrough attempt (raw SOAP body passthrough) was removed
// before v1.0.0 — arbitrary XML was un-reviewable and un-gateable. This
// redesign keeps only the safe core:
//
//   - ONE verb: Retrieve. The request is built exclusively by the shared
//     internal/soap package from structured flags — there is no way to
//     express Create/Update/Delete/Perform/Execute/Schedule, with or
//     without --write. Reads need no gate (same tier as dv/describe).
//   - --props is REQUIRED: no accidental "retrieve everything" pulls, and
//     the platform's own teaching error guards typos.
//   - --filter is equals-only and repeatable (the package supports more
//     operators; curated commands own the complex cases).
//   - Wrong property names surface the platform's teaching error, pointed
//     at the embedded describe catalog.

const usageSoap = `mcecli soap — generic SOAP object reads (READ-ONLY Retrieve, structured input)

  mcecli soap retrieve <ObjectType>  — any SOAP object not covered by a
               curated command (mcecli describe lists verified objects)
               --props a,b,c   REQUIRED — properties to retrieve
               [--filter P=V]  server-side equals filter (repeatable, ANDed)
               [--limit N]     max rows (default 200; 0 = all pages; loud cap)
               [--fields a,b]  project the output rows

Examples:
  mcecli soap retrieve List --props ID,ListName,Type --filter ListName=Newsletter
  mcecli soap retrieve SendClassification --props ObjectID,Name,SendClassificationType

Safety: Retrieve is the only verb reachable — writes stay behind curated
commands and ` + "`mcecli rest`" + ` gates. equals-only filters; anything more complex
belongs in a curated command.
`

// repeatFlags collects repeated string flags (--filter P=V).
type repeatFlags []string

func (r *repeatFlags) String() string     { return strings.Join(*r, ",") }
func (r *repeatFlags) Set(v string) error { *r = append(*r, v); return nil }

func cmdSoap(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageSoap)
		return exitUsage
	}
	switch args[0] {
	case "retrieve":
		return soapRetrieve(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usageSoap)
		return exitOK
	default:
		fmt.Fprintf(stderr, "unknown 'soap' subcommand %q\n\n%s", args[0], usageSoap)
		return exitUsage
	}
}

func soapRetrieve(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("soap retrieve", flag.ContinueOnError)
	var c common
	var props string
	var filters repeatFlags
	var limit int
	addCommon(fs, &c)
	addProjection(fs, &c)
	fs.StringVar(&props, "props", "", "comma-separated properties to retrieve (REQUIRED — mcecli describe <object> lists verified ones)")
	fs.Var(&filters, "filter", "server-side equals filter Prop=Value (repeatable; ANDed)")
	fs.IntVar(&limit, "limit", 200, "max rows (0 = all pages)")
	help := addHelp(fs)
	if err := parseCmd(fs, args); err != nil {
		return exitUsage
	}
	if *help {
		fmt.Fprint(stdout, usageSoap)
		return exitOK
	}
	if fs.NArg() != 1 {
		fmt.Fprint(stderr, "usage: mcecli soap retrieve <ObjectType> --props a,b,c [--filter P=V] [--limit N]\n")
		return exitUsage
	}
	objectType := fs.Arg(0)
	if props == "" {
		e := output.Fail(0, "--props is required (explicit, no accidental full pulls)",
			"verified properties: mcecli describe " + objectType)
		_ = output.Print(e, c.pretty, stdout)
		return exitUsage
	}
	if limit < 0 {
		e := output.Fail(0, "--limit must be >= 0", "0 = retrieve all pages")
		_ = output.Print(e, c.pretty, stdout)
		return exitUsage
	}
	var soapFilters []soap.Filter
	for _, f := range filters {
		eq := strings.Index(f, "=")
		if eq <= 0 {
			e := output.Fail(0, fmt.Sprintf("malformed --filter %q", f),
				"form: Prop=Value (equals; repeatable)")
			_ = output.Print(e, c.pretty, stdout)
			return exitUsage
		}
		soapFilters = append(soapFilters, soap.Filter{Prop: f[:eq], Op: "equals", Value: f[eq+1:]})
	}

	s, env, code := newSession(&c)
	if env != nil {
		_ = output.Print(env, c.pretty, stdout)
		return code
	}
	rows, status, err := soap.Retrieve(context.Background(), s.res.SoapURL(), s.tok.AccessToken,
		objectType, splitFields(props), soap.Opts{Filters: soapFilters, MaxRows: limit})
	if err != nil {
		e := output.Fail(0, err.Error(), "wire check: set MCECLI_SOAP_DEBUG=1 and retry")
		_ = output.Print(e, c.pretty, stdout)
		return exitAPI
	}
	// teaching error for wrong property names (same shape as dv reads)
	if strings.Contains(status, "do not match with the fields") {
		e := output.Fail(0, "SFMC rejected a property name: "+status,
			"verified properties: mcecli describe "+objectType)
		e.Status = 400
		_ = output.Print(e, c.pretty, stdout)
		return exitAPI
	}
	if strings.HasPrefix(status, "Error") {
		e := output.Fail(0, "SOAP OverallStatus: "+status,
			"unknown ObjectType or bad filter — mcecli describe lists verified objects")
		_ = output.Print(e, c.pretty, stdout)
		return exitAPI
	}

	truncated := false
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
		truncated = true
	}
	// envelope contract: list data is ALWAYS an array, never null
	if rows == nil {
		rows = []map[string]string{}
	}
	e := output.OK(200, rows)
	e.Count = len(rows)
	hint := "SOAP retrieve (read-only) — equals-only filters; complex queries need curated commands or rest"
	if truncated {
		hint += fmt.Sprintf(" — TRUNCATED to --limit %d; narrow with --filter or raise --limit", limit)
	}
	e.Hint = hint
	// convert to generic maps so --fields projection applies to output rows
	out := make([]any, len(rows))
	for i, r := range rows {
		m := make(map[string]any, len(r))
		for k, v := range r {
			m[k] = v
		}
		out[i] = m
	}
	e.Data = output.Project(out, splitFields(c.fields))
	_ = output.Print(e, c.pretty, stdout)
	return exitOK
}
