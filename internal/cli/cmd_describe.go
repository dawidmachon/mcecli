// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/dawidmachon/mcecli/internal/output"
)

// SOAP object catalog: tenant-verified knowledge, embedded (the SOAP
// Describe method is BLOCKED on this tenant — returns empty
// DefinitionResponseMsg; evidence wire-dumped 2026-09-17 — so property
// knowledge lives here instead). Sources:
//
//	live   = retrieved successfully on this tenant (bisected)
//	docs   = listed in official docs, NOT retrievable/verified here
//
// Declared in one place; dv/sub read their defaults from this table.
type soapObject struct {
	objectType string
	alias      string // dv/sub alias ("" = none)
	dataView   string // SQL data view name (_Sent etc.), "" = none
	use        string // what it's for, agent-facing
	props      []prop
	quirks     []string
}

type prop struct {
	name   string
	source string // "live" | "docs"
}

var soapCatalog = []soapObject{
	{
		objectType: "SentEvent", alias: "dv sent", dataView: "_Sent", use: "_Sent — who received which send, when",
		props:  []prop{{"SubscriberKey", "live"}, {"EventDate", "live"}, {"SendID", "live"}, {"BatchID", "live"}, {"ListID", "live"}, {"TriggeredSendDefinitionObjectID", "live"}, {"EventType", "live"}, {"SubscriberID", "live"}, {"Client.ID", "live"}},
		quirks: []string{"SubscriberID arrives inside PartnerProperties (mcecli flattens it)"},
	},
	{
		objectType: "ClickEvent", alias: "dv clicks", dataView: "_Clicks", use: "_Clicks — link clicks (URL)",
		props: []prop{{"SubscriberKey", "live"}, {"EventDate", "live"}, {"SendID", "live"}, {"BatchID", "live"}, {"URL", "live"}, {"EventType", "live"}},
	},
	{
		objectType: "OpenEvent", alias: "dv opens", dataView: "_Opens", use: "_Opens — email opens",
		props: []prop{{"SubscriberKey", "live"}, {"EventDate", "live"}, {"SendID", "live"}, {"BatchID", "live"}, {"EventType", "live"}},
	},
	{
		objectType: "BounceEvent", alias: "dv bounces", dataView: "_Bounce", use: "_Bounce — delivery failures (type/category)",
		props:  []prop{{"SubscriberKey", "live"}, {"EventDate", "live"}, {"SendID", "live"}, {"BatchID", "live"}, {"BounceType", "live"}, {"BounceCategory", "live"}, {"EventType", "live"}},
		quirks: []string{"BounceReason is in official docs but NOT retrievable on this tenant"},
	},
	{
		objectType: "UnsubEvent", alias: "dv unsubs", dataView: "_Unsubscribes", use: "_Unsubscribes — opt-outs",
		props:  []prop{{"SubscriberKey", "live"}, {"EventDate", "live"}, {"SendID", "live"}, {"BatchID", "live"}, {"IsMasterUnsubscribed", "live"}, {"EventType", "live"}},
		quirks: []string{"UnsubscribeType, OptOut, ListID are in docs but NOT retrievable here"},
	},
	{
		objectType: "NotSentEvent", alias: "dv notsent", dataView: "_NotSent", use: "_NotSent — suppressed sends",
		props: []prop{{"SubscriberKey", "live"}, {"EventDate", "live"}, {"SendID", "live"}, {"BatchID", "live"}, {"EventType", "live"}},
	},
	{
		objectType: "Send", alias: "dv send <id>", dataView: "_Job metadata", use: "_Job metadata — EmailName/Subject/FromName per send",
		props:  []prop{{"ID", "live"}, {"EmailName", "live"}, {"Subject", "live"}, {"FromName", "live"}, {"SentDate", "live"}},
		quirks: []string{"ComplexFilterPart → silent 0 rows; use single ID-equals (mcecli dv send handles this)"},
	},
	{
		objectType: "Subscriber", alias: "sub", use: "subscription state (Active/Bounced/Held/Unsubscribed)",
		props:  []prop{{"SubscriberKey", "live"}, {"EmailAddress", "live"}, {"Status", "live"}, {"UnsubscribedDate", "live"}, {"CreatedDate", "live"}, {"EmailTypePreference", "live"}, {"ModifiedDate", "docs"}},
		quirks: []string{"Status returns the NAME (\"Active\"), not the enum number", "ModifiedDate is in docs but NOT retrievable here", "same email may exist on multiple MID contexts → mcecli sub surfaces all matches"},
	},
	{
		objectType: "ListSubscriber", alias: "sub (join)", use: "per-list membership status",
		props: []prop{{"ListID", "live"}, {"SubscriberKey", "live"}, {"Status", "live"}, {"CreatedDate", "live"}},
	},
	{
		objectType: "BusinessUnit", alias: "bu discover", use: "BU tree (needs enterprise token + QueryAllAccounts)",
		props: []prop{{"ID", "live"}, {"Name", "live"}, {"AccountType", "live"}, {"ParentID", "live"}, {"ParentName", "live"}, {"IsActive", "live"}},
	},
	{
		objectType: "AccountUser", alias: "users list", use: "platform users (who has access)",
		props: []prop{{"ID", "live"}, {"UserID", "live"}, {"Name", "live"}, {"Email", "live"}, {"ActiveFlag", "live"}, {"DefaultBusinessUnit", "live"}, {"Delete", "live"}},
	},
	{
		objectType: "DataFolder", alias: "folders", use: "content folders → categoryId for de/query create",
		props:  []prop{{"ID", "live"}, {"Name", "live"}, {"ContentType", "live"}, {"ParentFolderID", "docs"}},
		quirks: []string{"ParentFolderID is in docs but NOT retrievable here", "ContentType server-side filter WORKS (rare)"},
	},
	{
		objectType: "DataExtensionObject[KEY]", alias: "de rows --where", use: "DE rows, server-side filter",
		props:  []prop{{"(DE columns)", "live"}},
		quirks: []string{"BROKEN on this tenant: returns OK with 0 rows always — use mcecli query instead"},
	},
}

const usageDescribe = `mcecli describe — SOAP object catalog (embedded, offline, tenant-verified)

  mcecli describe               — all known SOAP objects + purpose
  mcecli describe <object>      — retrievable properties for one object
               (SentEvent, Subscriber, Send, … — or dv/sub alias)

SOAP Describe API is BLOCKED on this tenant (empty response, wire-dumped
2026-09-17) — this catalog is compiled from live bisection + official
docs. props marked "live" were retrieved successfully here; "docs" are
listed officially but NOT retrievable on this tenant.
`

func cmdDescribe(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		rows := make([]map[string]string, 0, len(soapCatalog))
		for _, o := range soapCatalog {
			rows = append(rows, map[string]string{
				"object":  o.objectType,
				"use_as":  o.alias,
				"purpose": o.use,
			})
		}
		e := output.OK(0, rows)
		e.Count = len(rows)
		e.Hint = "properties: mcecli describe <object>"
		_ = output.Print(e, false, stdout)
		return exitOK
	}
	q := strings.ToLower(strings.TrimSpace(args[0]))
	var found *soapObject
	for i := range soapCatalog {
		o := soapCatalog[i]
		// strip a leading "_" so "describe _Sent" finds SentEvent
		probe := strings.TrimPrefix(strings.ToLower(args[0]), "_")
		if strings.EqualFold(o.objectType, args[0]) ||
			strings.EqualFold(strings.ToLower(o.alias), q) ||
			strings.EqualFold(o.dataView, strings.ToUpper(args[0])) ||
			strings.EqualFold(strings.ToLower(o.dataView), probe) ||
			strings.HasPrefix(strings.ToLower(o.alias), q) {
			found = &soapCatalog[i]
			break
		}
	}
	if found == nil {
		e := output.Fail(0, fmt.Sprintf("unknown SOAP object %q", args[0]),
			"mcecli describe — list known objects")
		_ = output.Print(e, false, stdout)
		return exitUsage
	}

	live := make([]string, 0, len(found.props))
	docsOnly := make([]string, 0)
	for _, pr := range found.props {
		if pr.source == "live" {
			live = append(live, pr.name)
		} else {
			docsOnly = append(docsOnly, pr.name)
		}
	}
	data := map[string]any{
		"object":         found.objectType,
		"use_as":         found.alias,
		"purpose":        found.use,
		"props_verified": live,
	}
	if len(docsOnly) > 0 {
		data["props_docs_only_not_retrievable"] = docsOnly
	}
	if len(found.quirks) > 0 {
		data["quirks"] = found.quirks
	}
	e := output.OK(0, data)
	e.Hint = "read rows with: " + found.alias + " (--fields to project)"
	_ = output.Print(e, false, stdout)
	return exitOK
}
