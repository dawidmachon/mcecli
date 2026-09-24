// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

// explain: offline knowledge base mapping SFMC error patterns to causes/actions.

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/dawidmachon/mcecli/internal/output"
)

type errPattern struct {
	Pattern  string
	Cause    string
	Action   string
	Severity string // "info", "recoverable", "fatal"
}

var errorKB = []errPattern{
	{"Token Expired", "OAuth token lifetime (20 min) exceeded", "mcecli auto-refreshes; if persistent, check system clock skew", "recoverable"},
	{"invalid_client", "client_id or client_secret is wrong, or the package was deleted/deactivated", "mcecli profile add <name> --subdomain ... --client-id ... --client-secret ...", "fatal"},
	{"Client authentication failed", "Same as invalid_client — credentials don't match an active installed package", "Verify in SFMC Setup > Installed Packages", "fatal"},
	{"not accessible for this account", "The credential doesn't have access to the requested BU (account_id)", "Check package access in SFMC Setup, or use a credential scoped to that BU", "recoverable"},
	{"Filter is invalid", "The API returned a filter error — usually a scope or permission issue on the package", "Check installed package scopes in SFMC Setup", "recoverable"},
	{"UsernameToken is expected", "SOAP auth rejected the token format — mcecli uses bare fueloauth, this should not happen", "Report as a bug if seen", "fatal"},
	{"security header is not present", "SOAP auth header missing or malformed", "mcecli handles this; report as a bug if seen", "fatal"},
	{"Primary key", "A row with this PK already exists (for POST/insert) or the PK field is wrong", "Use PUT for upsert, or check field names with mcecli de get", "recoverable"},
	{"cannot be retrieved for key", "DE not found by customerKey in the current account context", "DEs are per-BU: mcecli bu discover shows accessible BUs; mcecli de find <name> searches all", "recoverable"},
	{"404 page not found", "The endpoint path doesn't exist on this host", "Check mcecli api <section> for the correct path", "recoverable"},
	{"Identifying Guid is invalid", "The GUID passed in the URL path is not a valid MC object ID", "mcecli de list --search <name> --fields id to get the correct GUID", "recoverable"},
	{"JSON Deserialization Exception", "The request body doesn't match the expected schema", "For DE rows: mcecli de add handles the body format automatically", "recoverable"},
	{"is required", "A required parameter is missing", "Read the error message for the field name; check mcecli help <command>", "recoverable"},
	{"Login failed", "WSSE auth failed — this should not happen with mcecli (it uses fueloauth)", "Report as a bug if seen", "fatal"},
	{"Rate limit exceeded", "Too many API requests in a short window", "mcecli auto-backs off on 429; wait and reduce request frequency", "recoverable"},
	{"Request throttled", "SFMC is intentionally slowing down this client", "Wait 30-60 seconds before retrying", "recoverable"},
	{"Query took too long", "The query exceeded the execution timeout", "Optimize the SQL (add WHERE clause, reduce target fields)", "recoverable"},
	{"Target data extension is locked", "Another process is writing to the target DE", "Wait for the other process to complete", "recoverable"},
}

const usageExplain = `mcecli explain — look up SFMC error messages in the offline knowledge base

  mcecli explain <error text>    match against known error patterns

Example:
  mcecli explain "Token Expired"
  mcecli explain "Primary key 'RowId' does not exist"
`

func cmdExplain(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("explain", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	query := strings.Join(fs.Args(), " ")
	if query == "" {
		fmt.Fprint(stderr, usageExplain)
		return exitUsage
	}

	lq := strings.ToLower(query)
	matched := false
	for _, ep := range errorKB {
		if strings.Contains(lq, strings.ToLower(ep.Pattern)) {
			e := output.OK(200, map[string]any{
				"pattern":  ep.Pattern,
				"cause":    ep.Cause,
				"action":   ep.Action,
				"severity": ep.Severity,
			})
			_ = output.Print(e, false, stdout)
			matched = true
		}
	}
	if !matched {
		e := output.Fail(404, "no knowledge base entry matches '"+query+"'",
			"check docs/dev/endpoint-notes.md for the full catalog; new findings should be added there")
		_ = output.Print(e, false, stdout)
		return exitAPI
	}
	return exitOK
}
