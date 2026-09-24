// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/dawidmachon/mcecli/internal/output"
)

// ENS (event notification) operational reads — VERIFIED live:
// reads callbacks registered and their event subscriptions.

const usageEns = `mcecli ens — event notification (webhook) reads (read-only, REST)

  mcecli ens callbacks           — registered callbacks + status
  mcecli ens subs <callbackId>   — event subscriptions for one callback

Tells you which platform events are being pushed where (monitoring
transparency). Writes (create/update/delete/verify) stay gated via mcecli rest.
`

func cmdENS(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageEns)
		return exitUsage
	}
	switch args[0] {
	case "callbacks":
		return ensCallbacks(args[1:], stdout, stderr)
	case "subs":
		return ensSubs(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usageEns)
		return exitOK
	default:
		fmt.Fprintf(stderr, "unknown 'ens' subcommand %q\n\n%s", args[0], usageEns)
		return exitUsage
	}
}

func ensCallbacks(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ens callbacks", flag.ContinueOnError)
	var c common
	addCommon(fs, &c)
	if err := parseCmd(fs, args); err != nil {
		return exitUsage
	}
	s, env, code := newSession(&c)
	if env != nil {
		_ = output.Print(env, c.pretty, stdout)
		return code
	}
	st, resp, env2, _ := s.call(http.MethodGet, "platform/v1/ens-callbacks", nil, nil)
	if env2 != nil {
		_ = output.Print(env2, c.pretty, stdout)
		return exitAPI
	}
	if st < 200 || st > 299 {
		e := output.Fail(st, strings.TrimSpace(string(resp)), "ENS reads need the event-notification scope")
		_ = output.Print(e, c.pretty, stdout)
		return exitAPI
	}
	var rows []map[string]any
	json.Unmarshal(resp, &rows)
	if rows == nil {
		rows = []map[string]any{}
	}
	e := output.OK(st, rows)
	e.Count = len(rows)
	e.Hint = "subscriptions per callback: mcecli ens subs <callbackId>"
	_ = output.Print(e, c.pretty, stdout)
	return exitOK
}

func ensSubs(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ens subs", flag.ContinueOnError)
	var c common
	addCommon(fs, &c)
	if err := parseCmd(fs, args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprint(stderr, "usage: mcecli ens subs <callbackId>\n")
		return exitUsage
	}
	cb := fs.Arg(0)
	// path-segment hygiene: callback ids are GUIDs; refuse anything that
	// could alter the request path
	if !isSafePathSegment(cb) {
		e := output.Fail(0, fmt.Sprintf("invalid callbackId %q", cb), "ids look like GUIDs — list them with: mcecli ens callbacks")
		_ = output.Print(e, c.pretty, stdout)
		return exitUsage
	}
	s, env, code := newSession(&c)
	if env != nil {
		_ = output.Print(env, c.pretty, stdout)
		return code
	}
	st, resp, env2, _ := s.call(http.MethodGet, "platform/v1/ens-subscriptions-by-cb/"+cb, nil, nil)
	if env2 != nil {
		_ = output.Print(env2, c.pretty, stdout)
		return exitAPI
	}
	if st < 200 || st > 299 {
		e := output.Fail(st, strings.TrimSpace(string(resp)), "find callback ids with: mcecli ens callbacks")
		_ = output.Print(e, c.pretty, stdout)
		return exitAPI
	}
	var rows []map[string]any
	json.Unmarshal(resp, &rows)
	if rows == nil {
		rows = []map[string]any{}
	}
	e := output.OK(st, rows)
	e.Count = len(rows)
	e.Hint = "eventCategoryTypes show which platform events stream to this callback"
	_ = output.Print(e, c.pretty, stdout)
	return exitOK
}

// isSafePathSegment allows only GUID/identifier-safe characters in
// user-supplied path segments (alnum, dash, underscore, dot).
func isSafePathSegment(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}
