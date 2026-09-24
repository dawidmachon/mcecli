// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package cli implements the mcecli command surface. Every handler returns an
// exit code: 0 ok, 1 API/network failure, 2 usage error, 3 config error.
// Output is always a JSON envelope on stdout (except --raw successes), so
// agents can read structured results and rely on exit codes.
package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/dawidmachon/mcecli/internal/config"
	"github.com/dawidmachon/mcecli/internal/output"
)

const usage = `mcecli — Salesforce Marketing Cloud CLI for local agents

Usage: mcecli <command> [args] [flags]

Commands:
  status            current profile/BU/token state (no network; --check fetches)
  use               mcecli use [profile [bu|-]] — set current profile and BU
  profile add|list  mcecli profile add <name> --subdomain S --client-id ID --client-secret SEC
  bu add|list|discover  mcecli bu add <name> <mid> [--client-id ID --client-secret SEC]
                    | list | discover [--add]  (discover: SOAP Account retrieve,
                    runs at ACCOUNT level; needs a parent-access package for
                    full coverage — BU-scoped packages see only themselves)
  session list|use|show  multi-agent session isolation (MCECLI_SESSION env)
  auth test         fetch a token, show scopes + instance URLs
                    (--refresh forces; --all-bus validates account + every BU)
  de list|get|rows|add|dump|diff|find  data extensions (add = gated write;
                    diff = live-vs-dump drift check; find = cross-BU search)
  dv sent|clicks|opens|bounces|unsubs|notsent|send  data-view reads via SOAP
                    event objects (read-only; BEST option for tracking data;
                    server-side filters: --since 7d --send-id N --limit N)
  sub <key|email>   subscriber subscription state + list memberships
                    (read-only; answers "why is this person not getting mail?")
  describe [object] SOAP object catalog: verified props + quirks (offline)
  auto list|health  automation operational reads: list/detail/healthreport
                    (30-day success/error counts — "what is failing?")
  ts list|get       triggered send definitions (Canceled/Inactive = sends
                    silently not going out)
  lists [members <id>]  subscriber lists + who-is-on-list (read-only)
  guc list          global unsubscribe categories (enterprise)
  esd list|get      email send definitions (user-initiated sends)
  ens callbacks|subs  event-notification webhooks (what streams where)
  users list        platform users (read-only)
  folders [--type T]  content folders → categoryId for de/query create
  query list|run|status           SQL-on-platform query orchestration (run = gated write)
  md types|pull <type>  metadata retrieves to files (journeys, automations, ...)
  asset search|pull deliver assets to ~/.mcecli/work/<profile>/asset/ (index + bodies)
  api               explore the REST API surface: mcecli api <section> [--filter X]
  journal           read the write audit trail: mcecli journal [--last N]
  explain           look up SFMC error patterns: mcecli explain <text>
  doctor            self-diagnostic: config, credentials, tokens, caches
  undo              list/show before-image snapshots (DELETE auto-captured)
  rest              generic passthrough: mcecli rest GET data/v1/... [--body @f.json]
  skill             print the agent skill document
  version           print version
  help [command]    help text

Multi-agent: set MCECLI_SESSION=<name> per process — each session keeps its
own current profile/BU (state.<name>.json); tokens are shared safely.
PROD-named profiles warn on selection; MCECLI_NO_PROD=1 hard-refuses them.

Common flags (after any command): --profile NAME --bu NAME|MID --pretty --raw
Paging: --page N --size N where supported; envelope carries "next".
Projection: --fields a,b.c  keeps responses small.

WRITE SAFETY (hard rule):
  Any method other than GET requires --write; DELETE additionally --confirm.
  An agent must NEVER pass these without the user's explicit approval
  of that exact operation.
`

// unknownNameHint catches agents treating profile/BU names as commands.
func unknownNameHint(arg string) string {
	cfg, err := config.Load()
	if err != nil {
		return ""
	}
	for name := range cfg.Profiles {
		if strings.EqualFold(name, arg) {
			return fmt.Sprintf("%q looks like a profile, not a command — profiles: mcecli profile list; switch: mcecli use %s; BUs: mcecli bu list / bu discover", arg, name)
		}
	}
	st := config.LoadState()
	if p := cfg.Profiles[st.Profile]; p != nil {
		for buName, e := range p.BUs {
			if strings.EqualFold(buName, arg) || e.MID == arg {
				return fmt.Sprintf("%q looks like a BU, not a command — switch: mcecli use %s %s; one-shot: mcecli --bu %s <command>", arg, st.Profile, buName, buName)
			}
		}
	}
	return ""
}

// hoistSessionFlags moves session-scope flags (--profile/--bu/--pretty/--raw/--help/-h)
// from before the command to the end, so both forms work:
//
//	mcecli --profile P bu discover   and   mcecli bu discover --profile P
//	mcecli --help                   and   mcecli help
//
// Command-specific flags (--write, --refresh, ...) stay where they are.
func hoistSessionFlags(args []string) []string {
	global := map[string]bool{"--profile": true, "--bu": true, "--pretty": true, "--raw": true, "--jsonl": true, "--help": true, "-h": true}
	var lead, tail []string
	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		name, hasVal := a, false
		if eq := strings.Index(a, "="); eq >= 0 {
			name, hasVal = a[:eq], true
		}
		if !global[name] {
			break
		}
		lead = append(lead, a)
		if !hasVal && (name == "--profile" || name == "--bu") && i+1 < len(args) {
			i++
			lead = append(lead, args[i])
		}
	}
	if len(lead) == 0 {
		return args
	}
	tail = append(tail, args[i:]...)
	return append(tail, lead...)
}

// helpTopics holds per-command extended help.
var helpTopics = map[string]string{
	"rest":     usageRest,
	"de":       usageDE,
	"dv":       usageDV,
	"asset":    usageAsset,
	"api":      usageAPI,
	"use":      usageUse,
	"session":  usageSession,
	"query":    usageQuery,
	"ts":       usageTS,
	"lists":    usageLists,
	"sub":      usageSub,
	"auto":     usageAuto,
	"ens":      usageEns,
	"guc":      usageGUC,
	"esd":      usageESD,
	"describe": usageDescribe,
	"users":    usageUsers,
	"folders":  usageFolders,
	"journal":  usageJournal,
	"md":       usageMD,
	"doctor":   usageDoctor,
	"explain":  usageExplain,
}

// Run dispatches a command line and returns the process exit code.
func Run(args []string, stdout, stderr io.Writer, version, skillMD string) int {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprint(stdout, usage)
		return 0
	}
	args = hoistSessionFlags(args)
	var jsonl bool
	filtered := args[:0]
	for _, a := range args {
		if a == "--jsonl" {
			jsonl = true
			continue // stripped: no command defines it; it is a global output mode
		}
		filtered = append(filtered, a)
	}
	args = filtered
	if jsonl {
		output.JSONLMode = true
	}
	switch cmd, rest := args[0], args[1:]; cmd {
	case "--help", "-h":
		fmt.Fprint(stdout, usage)
		return exitOK
	case "version":
		fmt.Fprintln(stdout, "mcecli "+version)
		return exitOK
	case "help":
		if len(rest) == 0 {
			fmt.Fprint(stdout, usage)
			return exitOK
		}
		if h, ok := helpTopics[rest[0]]; ok {
			fmt.Fprint(stdout, h)
			return exitOK
		}
		fmt.Fprintf(stderr, "no extended help for %q — see 'mcecli help'\n", rest[0])
		return exitUsage
	case "status":
		return cmdStatus(rest, stdout, stderr)
	case "use":
		return cmdUse(rest, stdout, stderr)
	case "profile":
		return cmdProfile(rest, stdout, stderr)
	case "bu":
		return cmdBU(rest, stdout, stderr)
	case "session":
		return cmdSession(rest, stdout, stderr)
	case "auth":
		return cmdAuth(rest, stdout, stderr)
	case "rest":
		return cmdRest(rest, stdout, stderr)
	case "de":
		return cmdDE(rest, stdout, stderr)
	case "dv":
		return cmdDV(rest, stdout, stderr)
	case "sub":
		return cmdSub(rest, stdout, stderr)
	case "describe":
		return cmdDescribe(rest, stdout, stderr)
	case "auto":
		return cmdAuto(rest, stdout, stderr)
	case "ts":
		return cmdTS(rest, stdout, stderr)
	case "lists":
		return cmdLists(rest, stdout, stderr)
	case "ens":
		return cmdENS(rest, stdout, stderr)
	case "users":
		return cmdUsers(rest, stdout, stderr)
	case "folders":
		return cmdFolders(rest, stdout, stderr)
	case "guc":
		return cmdGUC(rest, stdout, stderr)
	case "esd":
		return cmdESD(rest, stdout, stderr)
	case "api":
		return cmdAPI(rest, stdout, stderr)
	case "query":
		return cmdQuery(rest, stdout, stderr)
	case "undo":
		return cmdUndo(rest, stdout, stderr)
	case "journal":
		return cmdJournal(rest, stdout, stderr)
	case "explain":
		return cmdExplain(rest, stdout, stderr)
	case "doctor":
		return cmdDoctor(rest, stdout, stderr)
	case "md":
		return cmdMD(rest, stdout, stderr)
	case "asset":
		return cmdAsset(rest, stdout, stderr)
	case "skill":
		fmt.Fprint(stdout, skillMD)
		return exitOK
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", cmd, usage)
		if h := unknownNameHint(cmd); h != "" {
			fmt.Fprintln(stderr, h)
		}
		return exitUsage
	}
}
