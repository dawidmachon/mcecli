// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/dawidmachon/mcecli/internal/output"
	"github.com/dawidmachon/mcecli/internal/soap"
)

// dvObjects maps CLI aliases to SOAP ObjectTypes + verified properties.
// Property sets verified LIVE on this tenant 2026-09-16 (probe + rejection
// messages) — official docs overstate: BounceReason, UnsubscribeType,
// OptOut, ListID-on-UnsubEvent are NOT retrievable here even though the
// object docs list them. Wrong names return "Error: The Request Property(s)
// X do not match with the fields of <Obj> retrieve".
var dvObjects = map[string]struct {
	objectType string
	props      []string
	dateProp   string // filter target for --since
	aliasOf    string // object type this data view corresponds to
}{
	"sent":    {"SentEvent", []string{"SubscriberKey", "EventDate", "SendID", "BatchID", "ListID", "TriggeredSendDefinitionObjectID", "EventType"}, "EventDate", "_Sent"},
	"clicks":  {"ClickEvent", []string{"SubscriberKey", "EventDate", "SendID", "BatchID", "URL", "EventType"}, "EventDate", "_Clicks"},
	"opens":   {"OpenEvent", []string{"SubscriberKey", "EventDate", "SendID", "BatchID", "EventType"}, "EventDate", "_Opens"},
	"bounces": {"BounceEvent", []string{"SubscriberKey", "EventDate", "SendID", "BatchID", "BounceType", "BounceCategory", "EventType"}, "EventDate", "_Bounce"},
	"unsubs":  {"UnsubEvent", []string{"SubscriberKey", "EventDate", "SendID", "BatchID", "IsMasterUnsubscribed", "EventType"}, "EventDate", "_Unsubscribes"},
	"notsent": {"NotSentEvent", []string{"SubscriberKey", "EventDate", "SendID", "BatchID", "EventType"}, "EventDate", "_NotSent"},
	"send":    {"Send", []string{"ID", "EmailName", "Subject", "FromName", "SentDate"}, "SentDate", "_Job metadata"},
}

const usageDV = `mcecli dv — data-view reads via SOAP event objects (READ-ONLY, cheapest path)

  mcecli dv list                     — data-view objects + verified properties
  mcecli dv <object> [--since W]     — recent tracking rows (1 SOAP call)
               [--send-id N]      — one send's events (joins to dv send <N>)
               [--subscriber-key K]
               [--fields a,b,c]   — project columns (default: verified set)
               [--limit N]        — max rows (default 200; 0 = all pages)
  mcecli dv send <id>                — one send's metadata (EmailName/Subject/
                                    FromName) — the _Job join: dv sent → pick
                                    SendID → dv send <SendID>

Objects:
  sent=SentEvent(_Sent)  clicks=ClickEvent(_Clicks)  opens=OpenEvent(_Opens)
  bounces=BounceEvent(_Bounce)  unsubs=UnsubEvent(_Unsubscribes)
  notsent=NotSentEvent(_NotSent)  send=Send(_Job metadata)

--since accepts 90m, 24h, 7d (default), or 2006-01-02 / RFC3339.

For agents: this is the BEST option for record-level tracking reads.
Use 'mcecli query run' only for SQL-only needs (aggregates, joins beyond
sent+send, _Subscribers, bulk). REST rowset NEVER reaches data views.
Filter server-side (--since/--send-id) — data views are huge.
`

func cmdDV(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageDV)
		return exitUsage
	}
	switch args[0] {
	case "list":
		return dvList(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usageDV)
		return exitOK
	case "send":
		return dvGet(args, stdout, stderr)
	default:
		return dvGet(args, stdout, stderr)
	}
}

func dvList(_ []string, stdout, stderr io.Writer) int {
	order := []string{"sent", "clicks", "opens", "bounces", "unsubs", "notsent", "send"}
	rows := make([]map[string]any, 0, len(order))
	for _, alias := range order {
		dv := dvObjects[alias]
		rows = append(rows, map[string]any{
			"alias":       alias,
			"soap_object": dv.objectType,
			"data_view":   dv.aliasOf,
			"properties":  dv.props,
		})
	}
	e := output.OK(0, rows)
	e.Count = len(rows)
	e.Hint = "read rows with: mcecli dv <alias> --since 7d"
	_ = output.Print(e, false, stdout)
	return exitOK
}

// parseSince turns --since values (90m, 24h, 7d, 2006-01-02, RFC3339) into a
// UTC timestamp for the SOAP greaterThan filter.
func parseSince(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Now().UTC().Add(-7 * 24 * time.Hour).Format("2006-01-02T15:04:05"), nil
	}
	// relative: <n><m|h|d>
	if m := regexp.MustCompile(`^(\d+)(min|m|h|d)$`).FindStringSubmatch(strings.ToLower(v)); m != nil {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			return "", fmt.Errorf("bad --since %q", v)
		}
		var d time.Duration
		switch m[2] {
		case "min", "m":
			d = time.Duration(n) * time.Minute
		case "h":
			d = time.Duration(n) * time.Hour
		case "d":
			d = time.Duration(n) * 24 * time.Hour
		}
		return time.Now().UTC().Add(-d).Format("2006-01-02T15:04:05"), nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02T15:04:05Z", "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t.UTC().Format("2006-01-02T15:04:05"), nil
		}
	}
	return "", fmt.Errorf("bad --since %q — use 90m, 24h, 7d, or 2006-01-02", v)
}

func dvGet(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("dv", flag.ContinueOnError)
	var c common
	var since, sendID, subKey, fields string
	var limit int
	addCommon(fs, &c)
	fs.StringVar(&since, "since", "", "time window: 90m, 24h, 7d (default), 2006-01-02, RFC3339")
	fs.StringVar(&sendID, "send-id", "", "filter to one SendID (equals)")
	fs.StringVar(&subKey, "subscriber-key", "", "filter to one SubscriberKey (equals)")
	fs.StringVar(&fields, "fields", "", "comma-separated properties to return")
	fs.IntVar(&limit, "limit", 200, "max rows to return (0 = all pages)")
	if err := parseCmd(fs, args); err != nil {
		return exitUsage
	}
	rest := fs.Args()
	if len(rest) < 1 {
		fmt.Fprint(stderr, usageDV)
		return exitUsage
	}
	alias := strings.ToLower(rest[0])
	dv, ok := dvObjects[alias]
	if !ok {
		// accept full SOAP object names too (SentEvent, ClickEvent, ...)
		for a, d := range dvObjects {
			if strings.EqualFold(d.objectType, rest[0]) {
				alias, dv, ok = a, d, true
				break
			}
		}
	}
	if !ok {
		e := output.Fail(0, fmt.Sprintf("unknown data-view object %q", rest[0]),
			"mcecli dv list — sent|clicks|opens|bounces|unsubs|notsent|send")
		_ = output.Print(e, c.pretty, stdout)
		return exitUsage
	}
	// positional id: mcecli dv send <SendID>
	if alias == "send" && sendID == "" && len(rest) > 1 {
		sendID = rest[1]
	}
	// no silently-ignored input: extra positionals are a mistake
	maxPos := 1
	if alias == "send" {
		maxPos = 2
	}
	if len(rest) > maxPos {
		e := output.Fail(0, fmt.Sprintf("unexpected extra arguments after %q: %v", rest[0], rest[maxPos:]),
			"one object per call; filter with --send-id / --subscriber-key")
		_ = output.Print(e, c.pretty, stdout)
		return exitUsage
	}

	var filters []soap.Filter
	var sinceVal string
	// dv send <id>: ID-equals ONLY. The Send object answers silent 0 rows for
	// ComplexFilterPart (SentDate AND ID) on this tenant, while bare ID-equals
	// works (verified 2026-09-16) — and an exact-ID lookup needs no date bound.
	exactIDLookup := alias == "send" && sendID != ""
	if dv.dateProp != "" && !exactIDLookup {
		sv, serr := parseSince(since)
		if serr != nil {
			e := output.Fail(0, serr.Error(), "--since 90m | 24h | 7d | 2006-01-02")
			_ = output.Print(e, c.pretty, stdout)
			return exitUsage
		}
		sinceVal = sv
		filters = append(filters, soap.Filter{Prop: dv.dateProp, Op: "greaterThan", Value: sinceVal})
	}
	if sendID != "" {
		prop := "SendID"
		if alias == "send" {
			prop = "ID"
		}
		if _, err := strconv.Atoi(sendID); err != nil {
			e := output.Fail(0, "--send-id must be a numeric SendID", "find IDs with: mcecli dv "+alias+" --fields SendID")
			_ = output.Print(e, c.pretty, stdout)
			return exitUsage
		}
		filters = append(filters, soap.Filter{Prop: prop, Op: "equals", Value: sendID})
	}
	if subKey != "" && alias == "send" {
		e := output.Fail(0, "--subscriber-key does not apply to send (Send has no SubscriberKey)",
			"filter events by subscriber first: mcecli dv sent --subscriber-key K — then join with mcecli dv send <SendID>")
		_ = output.Print(e, c.pretty, stdout)
		return exitUsage
	}
	if subKey != "" {
		filters = append(filters, soap.Filter{Prop: "SubscriberKey", Op: "equals", Value: subKey})
	}

	selected := dv.props
	if fields != "" {
		selected = splitFields(fields)
	}

	s, env, code := newSession(&c)
	if env != nil {
		_ = output.Print(env, c.pretty, stdout)
		return code
	}

	maxRows := limit
	if maxRows < 0 {
		maxRows = 0
	}
	rows, status, err := soap.Retrieve(context.Background(), s.res.SoapURL(), s.tok.AccessToken,
		dv.objectType, selected, soap.Opts{Filters: filters, MaxRows: maxRows})
	if err != nil {
		e := output.Fail(0, err.Error(), "wire check: set MCECLI_SOAP_DEBUG=1 and retry")
		_ = output.Print(e, c.pretty, stdout)
		return exitAPI
	}
	// Teaching error for wrong property names (verified live message shape).
	if strings.Contains(status, "do not match with the fields") {
		e := output.Fail(0, "SFMC rejected a property name: "+status,
			"verified properties for "+alias+": "+strings.Join(dv.props, ",")+
				" — override with --fields (mcecli dv list)")
		e.Status = 400
		_ = output.Print(e, c.pretty, stdout)
		return exitAPI
	}
	if strings.HasPrefix(status, "Error") {
		e := output.Fail(0, "SOAP OverallStatus: "+status, "check filters (bad dates/values fail here)")
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
	hint := fmt.Sprintf("soap_object=%s", dv.objectType)
	if sinceVal != "" {
		hint += fmt.Sprintf(" since=%s", sinceVal)
	}
	hint += " — join to send metadata: mcecli dv send <SendID>"
	if alias == "send" {
		hint = fmt.Sprintf("soap_object=Send id=%s — the _Job metadata for the events above", sendID)
	}
	if truncated {
		hint += " — TRUNCATED to --limit; narrow with --since/--send-id or raise --limit"
	}
	e.Hint = hint
	_ = output.Print(e, c.pretty, stdout)
	return exitOK
}
