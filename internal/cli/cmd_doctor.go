// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

// doctor: one-shot self-diagnostic for agent onboarding and troubleshooting.
// Read-only except for an optional token mint (which is cached anyway).

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/dawidmachon/mcecli/internal/auth"
	"github.com/dawidmachon/mcecli/internal/config"
	"github.com/dawidmachon/mcecli/internal/journal"
	"github.com/dawidmachon/mcecli/internal/output"
)

const usageDoctor = `mcecli doctor — self-diagnostic: config, credentials, tokens, caches, journal

  mcecli doctor                  # current profile: config + token + REST reachability
  mcecli doctor --all-profiles   # every profile's account-level token
  mcecli doctor --offline        # skip all network checks

Checks are listed with ok/warn/fail. Exit code: 0 = healthy/warnings,
1 = at least one FAIL. The envelope carries the full check list.

Examples:
  mcecli doctor
  mcecli doctor --profile <name> --all-profiles
`

type check struct {
	name   string
	ok     bool
	warn   bool
	detail string
}

func cmdDoctor(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	var c common
	var allProfiles, offline bool
	addCommon(fs, &c)
	fs.BoolVar(&allProfiles, "all-profiles", false, "check every profile's account-level token")
	fs.BoolVar(&offline, "offline", false, "skip network checks (config/caches only)")
	if err := parseCmd(fs, args); err != nil {
		return exitUsage
	}
	if help := addHelp(fs); *help {
		fmt.Fprint(stdout, usageDoctor)
		return exitOK
	}

	var checks []check
	add := func(ok bool, name, detail string) {
		checks = append(checks, check{name: name, ok: ok, detail: detail})
	}
	addWarn := func(name, detail string) {
		checks = append(checks, check{name: name, ok: true, warn: true, detail: detail})
	}

	// 1. config
	cfg, err := config.Load()
	if err != nil {
		add(false, "config", err.Error())
		e := doctorEnvelope(checks)
		_ = output.Print(e, c.pretty, stdout)
		return exitAPI
	}
	add(true, fmt.Sprintf("config (%d profiles)", len(cfg.Profiles)), config.Dir())
	if len(cfg.Profiles) == 0 {
		add(false, "profiles", "none configured — mcecli profile add <name> --subdomain ...")
	}

	// 2. state
	st := config.LoadState()
	if st.Profile == "" {
		addWarn("state", "no current profile — mcecli use <profile>")
	} else if p, canonical := config.FindProfile(cfg, st.Profile); p != nil {
		add(true, "state", fmt.Sprintf("profile %s (BU: %s, %d BUs)", canonical, st.BU, len(p.BUs)))
	} else {
		add(false, "state", "current profile '"+st.Profile+"' no longer exists in config")
	}

	// 3. credentials per profile (local check)
	for name, p := range cfg.Profiles {
		missing := ""
		if p.Subdomain == "" {
			missing = "subdomain"
		}
		if p.ClientID == "" {
			if missing != "" {
				missing += ", "
			}
			missing += "client_id"
		}
		if p.ClientSecret == "" {
			if missing != "" {
				missing += ", "
			}
			missing += "client_secret"
		}
		if missing != "" {
			add(false, "credentials:"+name, "missing "+missing)
		} else {
			add(true, "credentials:"+name, fmt.Sprintf("%d BUs configured", len(p.BUs)))
		}
	}

	// 4. token + REST reachability (network unless --offline)
	if !offline {
		checkToken := func(profileName string, res *config.Resolved) {
			tok, err := auth.Get(res, false)
			if err != nil {
				add(false, "token:"+profileName, err.Error())
				return
			}
			ttl := auth.TTLSeconds(res)
			add(true, fmt.Sprintf("token:%s (mid=%s)", profileName, res.MID),
				fmt.Sprintf("valid, %ds left, %d scopes", ttl, len(strings.Fields(tok.Scope))))
		}

		if res, err := config.Resolve(cfg, st, c.profile, c.bu); err == nil && res.Profile.Subdomain != "" {
			checkToken(res.Name, res)
		} else if err != nil {
			addWarn("token:current", "cannot resolve: "+err.Error())
		}
		if allProfiles {
			for name := range cfg.Profiles {
				if name == st.Profile {
					continue // already checked
				}
				if res, err := config.Resolve(cfg, config.State{}, name, ""); err == nil {
					checkToken(name, res)
				}
			}
		}
	} else {
		addWarn("network", "offline mode — token checks skipped")
	}

	// 5. caches
	tokFile := filepath.Join(config.Dir(), "tokens.json")
	if _, err := os.Stat(tokFile); err == nil {
		add(true, "token-cache", tokFile)
	} else {
		addWarn("token-cache", "no cached tokens yet")
	}
	if n := journal.Count(); n > 0 {
		add(true, "journal", fmt.Sprintf("%d audited writes in %s", n, journal.Path()))
	} else {
		addWarn("journal", "no audited writes yet — entries appear after gated writes")
	}

	// summary + envelope
	pass, warn, fail := 0, 0, 0
	data := make([]map[string]any, 0, len(checks))
	for _, ch := range checks {
		status := "ok"
		switch {
		case !ch.ok:
			status = "FAIL"
			fail++
		case ch.warn:
			status = "WARN"
			warn++
		default:
			status = "ok"
			pass++
		}
		data = append(data, map[string]any{
			"check": ch.name, "status": status, "detail": ch.detail,
		})
	}

	healthy := fail == 0
	e := output.OK(0, data)
	e.Count = len(data)
	if healthy {
		e.Hint = fmt.Sprintf("healthy: %d pass, %d warnings", pass, warn)
	} else {
		e.OK = false
		e.Hint = fmt.Sprintf("%d FAIL / %d warn / %d pass — fix FAIL items above", fail, warn, pass)
	}
	_ = output.Print(e, c.pretty, stdout)
	if !healthy {
		return exitAPI
	}
	return exitOK
}

// doctorEnvelope renders a failure that happened before checks could run.
func doctorEnvelope(checks []check) *output.Envelope {
	data := make([]map[string]any, 0, len(checks))
	for _, ch := range checks {
		data = append(data, map[string]any{"check": ch.name, "status": ch.detail})
	}
	e := output.Fail(0, "doctor aborted early", "fix config and re-run: mcecli doctor")
	e.Data = data
	return e
}
