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
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/dawidmachon/mcecli/internal/auth"
	"github.com/dawidmachon/mcecli/internal/config"
	"github.com/dawidmachon/mcecli/internal/output"
	"github.com/dawidmachon/mcecli/internal/soap"
)

const usageUse = `mcecli use — set the current profile / business unit (persisted in state.json)

  mcecli use                 show current profile + BU
  mcecli use <profile>       switch profile (clears BU)
  mcecli use <profile> <bu>  switch profile and BU (bu = configured name or raw MID)
  mcecli use <profile> -     switch profile and clear BU

One-shot alternative per call: --bu <name|MID>
Token scope follows the BU: each MID gets its own cached token.
`

const usageSession = `mcecli session — multi-agent session isolation

  mcecli session             list all sessions found in ~/.mcecli/
  mcecli session list        same as: mcecli session
  mcecli session use <name>  print export line for shell (does NOT write state)
  mcecli session show        print current session name (default if unset)

Session isolation: set MCECLI_SESSION=<name> in the calling shell.
Each session has its own state.<name>.json, isolated from all others.
Agents can run concurrently with different MCECLI_SESSION values.

  Agent A: export MCECLI_SESSION=agent-a && mcecli dv sent --since 7d
  Agent B: export MCECLI_SESSION=agent-b && mcecli use dev-limited prod

The default session (no MCECLI_SESSION set) uses state.json — safe for single-agent.
PROD profiles: set MCECLI_NO_PROD=1 as a hard guard.
`

func cmdUse(args []string, stdout, stderr io.Writer) int {
	cfg, err := config.Load()
	if err != nil {
		_ = output.Print(output.Fail(0, err.Error(), "fix or remove "+config.Dir()+"/config.json"), false, stdout)
		return exitCfg
	}
	st := config.LoadState()

	if len(args) == 0 {
		e := output.OK(0, map[string]any{"profile": st.Profile, "bu": st.BU})
		if st.Profile == "" {
			e.Hint = "mcecli profile add <name> --subdomain ... --client-id ... --client-secret ... ; then mcecli use <name>"
		}
		_ = output.Print(e, false, stdout)
		return exitOK
	}

	name := args[0]
	p, canonical := config.FindProfile(cfg, name)
	if p == nil {
		_ = output.Print(output.Fail(0, "unknown profile "+name,
			"known: "+fmt.Sprint(profileNames(cfg))+" (names are case-insensitive)"), false, stdout)
		return exitCfg
	}
	name = canonical
	bu := ""
	if len(args) >= 2 {
		bu = args[1]
	}
	if bu == "-" {
		bu = ""
	}
	// PROD hard-guard: block selection of production-named profiles unless overridden.
	if config.IsProdName(name) && os.Getenv(config.EnvNoProd) != "" {
		_ = output.Print(output.Fail(0,
			fmt.Sprintf("PROD profile %q refused (MCECLI_NO_PROD=1 is set)", name),
			"unset MCECLI_NO_PROD=1 to allow PROD profiles, or use a named session"), false, stdout)
		return exitCfg
	}
	if bu != "" {
		if _, buCanonical, ok := config.FindBU(*p, bu); ok {
			bu = buCanonical
		} else if !isMID(bu) {
			_ = output.Print(output.Fail(0, fmt.Sprintf("unknown BU %q in profile %q", bu, name),
				"known: "+fmt.Sprint(buNames(*p))+" — or pass a raw numeric MID"), false, stdout)
			return exitCfg
		}
	}
	st.Profile = name
	st.BU = bu
	if err := config.SaveState(st); err != nil {
		_ = output.Print(output.Fail(0, err.Error(), ""), false, stdout)
		return exitCfg
	}
	_ = output.Print(output.OK(0, map[string]any{"profile": st.Profile, "bu": st.BU}), false, stdout)
	return exitOK
}

// cmdSession implements: mcecli session list|use|show
func cmdSession(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || (len(args) == 1 && args[0] == "list") {
		sessions := config.Sessions()
		curEnv := config.EnvGet(config.EnvSession)
		out := []any{}
		for _, sess := range sessions {
			file := "state." + sess + ".json"
			if sess == "(default)" {
				file = "state.json"
			}
			entry := map[string]any{"session": sess, "file": file}
			if sess == curEnv {
				entry["active"] = true
			}
			out = append(out, entry)
		}
		e := output.OK(0, out)
		if curEnv != "" {
			e.Hint = fmt.Sprintf("current session: %q (from MCECLI_SESSION env)", curEnv)
		} else {
			e.Hint = "no MCECLI_SESSION set — using default state.json (safe for single-agent)"
			if len(sessions) > 0 {
				e.Hint += fmt.Sprintf("; to activate a session: source <(mcecli session use %s)", sessions[0])
			}
		}
		_ = output.Print(e, false, stdout)
		return exitOK
	}

	switch args[0] {
	case "use":
		if len(args) < 2 {
			fmt.Fprint(stderr, "usage: mcecli session use <name>\n")
			return exitUsage
		}
		name := args[1]
		// Validate: no path separators, no ..
		if strings.ContainsAny(name, "/\\") || name == ".." {
			_ = output.Print(output.Fail(0, "invalid session name: "+name, "use alphanumeric, - and _ only"), false, stdout)
			return exitCfg
		}
		// stdout carries ONLY the export line so `source <(mcecli session use X)` works;
		// envelope goes to stderr (raw-style exception, like --raw successes).
		fmt.Fprintf(stdout, "export MCECLI_SESSION=%s\n", name)
		e := output.OK(0, map[string]any{
			"session": name,
			"file":    "state." + name + ".json",
		})
		_ = output.Print(e, false, stderr) // envelope on stderr: stdout is reserved for the export line
		return exitOK

	case "show":
		cur := config.EnvGet(config.EnvSession)
		file := "state.json"
		if cur != "" {
			file = "state." + config.SanitizeSessionName(cur) + ".json"
		} else {
			cur = "(default)"
		}
		e := output.OK(0, map[string]any{"session": cur, "file": file})
		_ = output.Print(e, false, stdout)
		return exitOK

	default:
		fmt.Fprintf(stderr, "unknown 'session' subcommand %q\n", args[0])
		return exitUsage
	}
}

// cmdProfile implements: mcecli profile add|list
func cmdProfile(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, "usage: mcecli profile add <name> --subdomain S --client-id ID --client-secret SEC | mcecli profile list\n")
		return exitUsage
	}
	cfg, err := config.Load()
	if err != nil {
		_ = output.Print(output.Fail(0, err.Error(), ""), false, stdout)
		return exitCfg
	}
	st := config.LoadState()

	switch args[0] {
	case "add":
		if len(args) < 2 {
			fmt.Fprint(stderr, "usage: mcecli profile add <name> --subdomain S --client-id ID --client-secret SEC\n")
			return exitUsage
		}
		name := args[1]
		fs := flag.NewFlagSet("profile add", flag.ContinueOnError)
		var sub, id, sec string
		fs.StringVar(&sub, "subdomain", "", "subdomain, e.g. abc123 (from base URI of installed package)")
		fs.StringVar(&id, "client-id", "", "installed package client id")
		fs.StringVar(&sec, "client-secret", "", "installed package client secret")
		if err := parseCmd(fs, args[2:]); err != nil {
			return exitUsage
		}
		if sub == "" || id == "" || sec == "" {
			_ = output.Print(output.Fail(0, "missing --subdomain / --client-id / --client-secret",
				"get all three from SFMC Setup > Installed Packages > your package"), false, stdout)
			return exitUsage
		}
		if cfg.Profiles == nil {
			cfg.Profiles = map[string]*config.Profile{}
		}
		cfg.Profiles[name] = &config.Profile{Subdomain: sub, ClientID: id, ClientSecret: sec, BUs: map[string]*config.BUEntry{}}
		if err := config.Save(cfg); err != nil {
			_ = output.Print(output.Fail(0, err.Error(), ""), false, stdout)
			return exitCfg
		}
		_ = output.Print(output.OK(0, map[string]any{"profile": name, "subdomain": sub,
			"hint": "next: mcecli use " + name + " ; mcecli bu add <name> <mid>"}), false, stdout)
		return exitOK

	case "list":
		out := []any{}
		names := profileNames(cfg)
		sort.Strings(names)
		for _, n := range names {
			p := cfg.Profiles[n]
			out = append(out, map[string]any{
				"name": n, "subdomain": p.Subdomain,
				"bus": len(p.BUs), "current": n == st.Profile,
			})
		}
		_ = output.Print(output.OK(0, out), false, stdout)
		return exitOK

	default:
		fmt.Fprintf(stderr, "unknown 'profile' subcommand %q\n", args[0])
		return exitUsage
	}
}

// cmdBU implements: mcecli bu add <name> <mid> [--client-id ID --client-secret SEC] | mcecli bu list
// A BU may carry its own credential pair for installed packages scoped to
// that single BU; otherwise it inherits the profile credentials.
func cmdBU(args []string, stdout, stderr io.Writer) int {
	usage := "usage: mcecli bu add <name> <mid> [--client-id ID --client-secret SEC] [--profile P] | mcecli bu list [--profile P]"
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	cfg, err := config.Load()
	if err != nil {
		_ = output.Print(output.Fail(0, err.Error(), ""), false, stdout)
		return exitCfg
	}
	st := config.LoadState()

	// discover owns its flag parsing (--add, --profile via addCommon) and
	// always runs at account level, so it bypasses the base flagset.
	if args[0] == "discover" {
		return buDiscover(args[1:], stdout, stderr)
	}

	fs := flag.NewFlagSet("bu", flag.ContinueOnError)
	var prof, cid, sec string
	fs.StringVar(&prof, "profile", "", "profile (defaults to current)")
	if args[0] == "add" {
		fs.StringVar(&cid, "client-id", "", "per-BU client id (packages scoped to this BU only)")
		fs.StringVar(&sec, "client-secret", "", "per-BU client secret")
	}
	if err := parseCmd(fs, args[1:]); err != nil {
		return exitUsage
	}

	name := st.Profile
	if prof != "" {
		name = prof
	}
	if name == "" && len(cfg.Profiles) == 1 {
		for k := range cfg.Profiles {
			name = k
		}
	}
	if name == "" || cfg.Profiles[name] == nil {
		_ = output.Print(output.Fail(0, "no profile", "mcecli profile add ... ; mcecli use <name>"), false, stdout)
		return exitCfg
	}
	p := cfg.Profiles[name]

	switch args[0] {
	case "add":
		rest := fs.Args()
		if len(rest) != 2 {
			fmt.Fprint(stderr, usage)
			return exitUsage
		}
		buName, mid := rest[0], rest[1]
		if !isMID(mid) {
			_ = output.Print(output.Fail(0, "MID must be numeric", "find the MID in SFMC Setup > Account, or the BU switcher"), false, stdout)
			return exitUsage
		}
		if (cid == "") != (sec == "") {
			_ = output.Print(output.Fail(0, "--client-id and --client-secret go together", "omit both to inherit the profile credentials"), false, stdout)
			return exitUsage
		}
		if p.BUs == nil {
			p.BUs = map[string]*config.BUEntry{}
		}
		p.BUs[buName] = &config.BUEntry{MID: mid, ClientID: cid, ClientSecret: sec}
		if err := config.Save(cfg); err != nil {
			_ = output.Print(output.Fail(0, err.Error(), ""), false, stdout)
			return exitCfg
		}
		creds := "profile"
		if cid != "" {
			creds = "per-BU"
		}
		_ = output.Print(output.OK(0, map[string]any{"profile": name, "bu": buName, "mid": mid, "creds": creds}), false, stdout)
		return exitOK

	case "list":
		out := []any{}
		buNamesSorted := buNames(*p)
		sort.Strings(buNamesSorted)
		for _, b := range buNamesSorted {
			entry := p.BUs[b]
			creds := "profile"
			if entry.ClientID != "" {
				creds = "per-BU"
			}
			out = append(out, map[string]any{"name": b, "mid": entry.MID, "creds": creds, "current": b == st.BU})
		}
		e := output.OK(0, out)
		e.Hint = "switch with: mcecli use " + name + " <bu>"
		_ = output.Print(e, false, stdout)
		return exitOK

	default:
		fmt.Fprintf(stderr, "unknown 'bu' subcommand %q\n", args[0])
		return exitUsage
	}
}

// flagBUValue extracts the --bu flag value without parsing the full set,
// so discovery can distinguish "not given" from "-".
func flagBUValue(args []string) string {
	for i, a := range args {
		if a == "--bu" && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(a, "--bu=") {
			return strings.TrimPrefix(a, "--bu=")
		}
	}
	return ""
}

// sanitizeBUName turns a discovered account name into a stable config key:
// lowercase, [a-z0-9_-], spaces -> "_".
func sanitizeBUName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-':
			b.WriteRune(r)
		case r == ' ' || r == '/' || r == '\\':
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" {
		out = "bu"
	}
	return out
}

// buDiscover lists the business units visible to the installed package via a
// SOAP Account retrieve. It ALWAYS runs at account level: discovery wants the
// parent/account-level credential. With a package scoped to a single BU, SFMC
// returns only that BU — that is expected, not an error, and the envelope says
// so explicitly (LLMs must not misread partial results as the full estate).
func buDiscover(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("bu discover", flag.ContinueOnError)
	var c common
	var add bool
	addCommon(fs, &c)
	fs.BoolVar(&add, "add", false, "add discovered BUs to the profile (local config only; existing entries untouched)")
	if err := parseCmd(fs, args); err != nil {
		return exitUsage
	}

	c.bu = "-" // default: account-level token (the package home account)
	if buFlagOverride := flagBUValue(args); buFlagOverride != "" {
		c.bu = buFlagOverride // explicit --bu <name|MID> scopes discovery there
	}
	s, env, code := newSession(&c)
	if env != nil {
		_ = output.Print(env, c.pretty, stdout)
		return code
	}

	rows, soapStatus, soapErr := soap.Retrieve(context.Background(), s.res.SoapURL(), s.tok.AccessToken,
		"Account", []string{"ID", "Name", "AccountType", "ParentID", "ParentName", "Country"})

	out := make([]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, map[string]any{
			"mid":         r["ID"],
			"name":        r["Name"],
			"type":        r["AccountType"],
			"parent_mid":  r["ParentID"],
			"parent_name": r["ParentName"],
		})
	}

	// REST situation report (works even when SOAP is unavailable):
	// enterprise id + the BUs the credential can access per the Contacts schema.
	data := map[string]any{"accounts": out}
	_, csResp, csEnv2, _ := s.call(http.MethodGet, "contacts/v1/schema", nil, nil)
	var accessible []int
	if csEnv2 == nil {
		if m, ok := parseJSON(csResp).(map[string]any); ok {
			if item, ok := m["item"].(map[string]any); ok {
				cs := map[string]any{}
				if v, ok := item["enterpriseID"].(float64); ok {
					cs["enterprise_id"] = int(v)
				}
				if v, ok := item["availableBusinessUnits"].([]any); ok {
					for _, x := range v {
						if f, ok := x.(float64); ok {
							accessible = append(accessible, int(f))
						}
					}
				}
				if len(accessible) > 0 {
					cs["available_business_units"] = accessible
				}
				if len(cs) > 0 {
					data["contacts"] = cs
				}
			}
		}
	}

	// Full-tree attempt: with an ENTERPRISE-scoped token, BusinessUnit +
	// QueryAllAccounts returns the whole hierarchy (VERIFIED live). Limited
	// credentials fail here — reported, never guessed around.
	// Enterprise MID sources: contacts/v1/schema, then the token JWT (eid).
	entMID := 0
	if csMap, ok := data["contacts"].(map[string]any); ok {
		if ent, ok := csMap["enterprise_id"].(int); ok {
			entMID = ent
		}
	}
	if entMID == 0 {
		if eid, ok := auth.JWTEnterpriseID(s.tok.AccessToken); ok {
			entMID = eid
			data["enterprise_source"] = "jwt"
		}
	}
	if entMID > 0 {
		entRes := *s.res
		entRes.MID = strconv.Itoa(entMID)
		if entTok, terr := auth.Get(&entRes, false); terr == nil {
			buRows, _, berr := soap.RetrieveBusinessUnits(context.Background(),
				s.res.SoapURL(), entTok.AccessToken, soap.Opts{
					Refresher: func() (string, error) {
						nt, rerr := auth.Get(&entRes, true)
						if rerr != nil {
							return "", rerr
						}
						return nt.AccessToken, nil
					},
				})
			switch {
			case berr != nil:
				data["enterprise"] = map[string]any{"error": berr.Error()}
			case len(buRows) > len(out):
				out = out[:0]
				for _, r := range buRows {
					out = append(out, map[string]any{
						"mid":         r["ID"],
						"name":        r["Name"],
						"type":        r["AccountType"],
						"parent_mid":  r["ParentID"],
						"parent_name": r["ParentName"],
					})
				}
				data["accounts"] = out
				data["source"] = "enterprise"
			default:
				data["enterprise"] = map[string]any{
					"attempted": true, "rows": len(buRows),
					"note": "enterprise reachable, but the BusinessUnit catalog returned no additional rows — the grant may not include enterprise-node visibility, or is still propagating",
				}
			}
		} else {
			data["enterprise"] = map[string]any{
				"mid": entMID, "error": terr.Error(),
				"note": "enterprise tree unavailable with these credentials",
			}
		}
	}

	e := output.OK(200, data)
	e.Count = len(out)

	// Scope-aware guidance: data["source"] == "enterprise" means the full tree
	// was used (len(buRows) > len(soapRows)).
	switch {
	case data["source"] == "enterprise":
		e.Hint = fmt.Sprintf("full hierarchy (%d BUs) via enterprise context; add manually or re-run with --add", len(out))
	case soapErr != nil:
		e.Hint = "SOAP Account retrieve failed (" + soapErr.Error() + ") — contacts schema section may still show accessible BUs"
	case len(rows) == 0:
		e.Hint = "no accounts visible via SOAP — the package is likely BU-scoped; " +
			"contacts schema shows accessible BUs (see data.contacts) — re-run with --add to populate config"
	case len(rows) == 1:
		e.Hint = "only the calling context was returned — with a BU-scoped package this is EXPECTED (discovery wants parent/account-level credentials); " +
			"do not treat this as the full BU estate"
	default:
		e.Hint = fmt.Sprintf("%d accounts visible; add manually with 'mcecli bu add <name> <mid>' or re-run with --add", len(rows))
	}
	if soapErr == nil && soapStatus == "MoreDataAvailable" {
		e.Hint += "; more results exist (SOAP ContinueRequest not implemented)"
	}

	if add {
		// import the FINAL account list (enterprise tree when available,
		// SOAP subset otherwise) plus contacts-only MIDs
		added, skipped := importBUs(s.res.Name, out)
		midAdded, midSkipped := importAccessibleMIDs(s.res.Name, accessible)
		e.Hint += fmt.Sprintf("; --add: %d added (%d from contacts schema), %d skipped (already configured)",
			added+midAdded, midAdded, skipped+midSkipped)
	}
	_ = output.Print(e, c.pretty, stdout)
	return exitOK
}

// importAccessibleMIDs adds contacts-schema BUs (name unknown) as bu-<mid>
// entries, skipping any MID already configured. Local config write only.
func importAccessibleMIDs(profileName string, mids []int) (added, skipped int) {
	if len(mids) == 0 {
		return 0, 0
	}
	cfg, err := config.Load()
	if err != nil {
		return 0, len(mids)
	}
	p := cfg.Profiles[profileName]
	if p == nil {
		return 0, len(mids)
	}
	if p.BUs == nil {
		p.BUs = map[string]*config.BUEntry{}
	}
	knownMIDs := map[string]bool{}
	for _, e := range p.BUs {
		knownMIDs[e.MID] = true
	}
	for _, mid := range mids {
		ms := strconv.Itoa(mid)
		if knownMIDs[ms] {
			skipped++
			continue
		}
		p.BUs["bu-"+ms] = &config.BUEntry{MID: ms}
		added++
	}
	if err := config.Save(cfg); err != nil {
		return 0, len(mids)
	}
	return added, skipped
}

// importBUs merges discovered accounts (envelope-shaped rows: "mid"/"name"
// keys) into the profile config as plain string-form entries. Existing
// entries (incl. per-BU credentials) are never touched, and MIDs already
// configured under another name are skipped. Local config write only —
// SFMC is not modified.
func importBUs(profileName string, rows []any) (added, skipped int) {
	cfg, err := config.Load()
	if err != nil {
		return 0, len(rows)
	}
	p := cfg.Profiles[profileName]
	if p == nil {
		return 0, len(rows)
	}
	if p.BUs == nil {
		p.BUs = map[string]*config.BUEntry{}
	}
	knownMIDs := map[string]bool{}
	for _, e := range p.BUs {
		knownMIDs["bu-"+e.MID] = true
	}
	for _, it := range rows {
		m, ok := it.(map[string]any)
		if !ok {
			skipped++
			continue
		}
		mid, _ := m["mid"].(string)
		name, _ := m["name"].(string)
		if mid == "" || knownMIDs["bu-"+mid] {
			skipped++
			continue
		}
		key := sanitizeBUName(name)
		if key == "" {
			key = "bu-" + mid
		}
		if _, exists := p.BUs[key]; exists {
			skipped++
			continue
		}
		p.BUs[key] = &config.BUEntry{MID: mid}
		added++
	}
	if err := config.Save(cfg); err != nil {
		return 0, len(rows)
	}
	return added, skipped
}
