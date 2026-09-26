// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/dawidmachon/mcecli/internal/output"
	"github.com/dawidmachon/mcecli/internal/soap"
)

// dvObjects maps CLI aliases to SOAP ObjectTypes + verified properties.
// Property sets verified LIVE on the reference org 2026-09-16 (probe + rejection
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
  mcecli dv recipients <jobId>       — per-recipient send status for ONE job
                                    (REST job stats; jobId = SendID from dv
                                    sent; one row per send transaction)
               [--fields a,b,c]   — subscriberId,transactionId,transactionTime,domain
               [--limit N]        — max rows (default 200; 0 = no row cap; loud)
               [--pages N]        — max item pages to scan (25 recipients/page)

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
	case "recipients":
		return dvRecipients(args[1:], stdout, stderr)
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
	// ComplexFilterPart (SentDate AND ID) on the reference org, while bare ID-equals
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

// dvRecipients — per-recipient send status for one email send job, via
// GET /messaging/v1/jobs/{id}/stats/sends (roadmap v1.1 "per-recipient send
// status"). VERIFIED live: items are {subscriberId, stats:[{id,
// transactionTime, domain}]}; a recipient's stats array carries one entry per
// send transaction (re-sends repeat). The jobId namespace == _Sent.SendID —
// the same id answers both dv sent --send-id and this endpoint, so the join
// is: mcecli dv sent → pick SendID → mcecli dv recipients <SendID>.
// NOT available: GET /messaging/v1/emailSends/{jobId} (404 live; absent from
// discovery) — recorded in docs/dev/endpoint-notes.md.
// Paging: the server pages ITEMS at 25 and does not honor $pageSize (echoes
// the default) — the command walks $page until the server count is consumed
// or the page runs dry. Rows are FLATTENED (one per transaction); --limit
// caps rows loudly, --pages bounds the scan loudly (never silent truncation).
func dvRecipients(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("dv recipients", flag.ContinueOnError)
	var c common
	var fields string
	var limit, pages int
	addCommon(fs, &c)
	fs.StringVar(&fields, "fields", "", "comma-separated projection: subscriberId,transactionId,transactionTime,domain")
	fs.IntVar(&limit, "limit", 200, "max transaction rows (0 = no row cap)")
	fs.IntVar(&pages, "pages", 40, "max item pages to scan (server pages recipients at 25)")
	help := addHelp(fs)
	if err := parseCmd(fs, args); err != nil {
		return exitUsage
	}
	if *help {
		fmt.Fprint(stdout, usageDV)
		return exitOK
	}
	if fs.NArg() != 1 {
		fmt.Fprint(stderr, "usage: mcecli dv recipients <jobId>   (jobId = SendID from mcecli dv sent)\n")
		return exitUsage
	}
	jobID := fs.Arg(0)
	if _, err := strconv.Atoi(jobID); err != nil {
		e := output.Fail(0, "jobId must be numeric",
			"find job ids: mcecli dv sent --fields SendID — then mcecli dv recipients <SendID>")
		_ = output.Print(e, c.pretty, stdout)
		return exitUsage
	}
	if pages < 1 {
		e := output.Fail(0, "--pages must be >= 1", "server pages recipients at 25 items per call")
		_ = output.Print(e, c.pretty, stdout)
		return exitUsage
	}
	if limit < 0 {
		e := output.Fail(0, "--limit must be >= 0", "0 = no row cap (--pages still bounds the scan)")
		_ = output.Print(e, c.pretty, stdout)
		return exitUsage
	}

	s, env, code := newSession(&c)
	if env != nil {
		_ = output.Print(env, c.pretty, stdout)
		return code
	}

	rows := []any{}
	recipientsSeen := 0
	serverCount := -1
	rowCapHit := false
	for page := 1; page <= pages; page++ {
		u := url.URL{Path: "messaging/v1/jobs/" + jobID + "/stats/sends"}
		q := url.Values{}
		q.Set("$page", strconv.Itoa(page))
		u.RawQuery = q.Encode()
		st, resp, env2, _ := s.call(http.MethodGet, u.String(), nil, nil)
		if env2 != nil {
			_ = output.Print(env2, c.pretty, stdout)
			return exitAPI
		}
		if st >= 400 {
			e := errorEnvelope(st, resp, http.MethodGet)
			if st == 404 {
				e.Hint = "no send stats for this job (id typo, job never sent, or stats expired) — verify the job: mcecli dv send " + jobID
			}
			_ = output.Print(e, c.pretty, stdout)
			return exitAPI
		}
		m, ok := parseJSON(resp).(map[string]any)
		if !ok {
			e := output.Fail(st, "unexpected response shape",
				"raw path: mcecli rest GET messaging/v1/jobs/"+jobID+"/stats/sends")
			_ = output.Print(e, c.pretty, stdout)
			return exitAPI
		}
		if n, ok := m["count"].(float64); ok {
			serverCount = int(n)
		}
		items, _ := m["items"].([]any)
		if len(items) == 0 {
			break
		}
		for _, it := range items {
			recipientsSeen++
			im, ok := it.(map[string]any)
			if !ok {
				continue
			}
			subID := im["subscriberId"]
			stats, _ := im["stats"].([]any)
			for _, stat := range stats {
				sm, ok := stat.(map[string]any)
				if !ok {
					continue
				}
				rows = append(rows, map[string]any{
					"subscriberId":    subID,
					"transactionId":   sm["id"],
					"transactionTime": sm["transactionTime"],
					"domain":          sm["domain"],
				})
			}
			if limit > 0 && len(rows) >= limit {
				rowCapHit = true
				break
			}
		}
		if rowCapHit {
			break
		}
		if serverCount >= 0 && recipientsSeen >= serverCount {
			break
		}
	}

	e := output.OK(200, rows)
	e.Count = len(rows)
	hint := "REST job send stats — one row per send transaction (re-sends repeat); subscriberId only, no address (join: mcecli dv sent --send-id " + jobID + ")"
	if serverCount >= 0 {
		hint += fmt.Sprintf(" — %d recipient(s) per server count", serverCount)
	}
	// a row cap only LIES if more data was actually left behind
	allFetched := serverCount >= 0 && recipientsSeen >= serverCount
	if rowCapHit && !allFetched {
		hint += fmt.Sprintf(" — CAPPED at --limit %d rows; raise --limit for the rest", limit)
	}
	if !rowCapHit && serverCount >= 0 && recipientsSeen < serverCount {
		hint += fmt.Sprintf(" — SCAN CEILING: --pages %d reached, %d of %d recipients fetched; raise --pages", pages, recipientsSeen, serverCount)
	}
	e.Hint = hint
	e.Data = output.Project(e.Data, splitFields(fields))
	_ = output.Print(e, c.pretty, stdout)
	return exitOK
}
