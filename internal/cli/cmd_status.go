// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/dawidmachon/mcecli/internal/auth"
	"github.com/dawidmachon/mcecli/internal/config"
	"github.com/dawidmachon/mcecli/internal/output"
)

func cmdStatus(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	var c common
	addCommon(fs, &c)
	fs.BoolVar(&c.check, "check", false, "fetch a fresh token (network) and show scopes")
	if err := parseCmd(fs, args); err != nil {
		return exitUsage
	}

	cfg, err := config.Load()
	if err != nil {
		_ = output.Print(output.Fail(0, err.Error(), "fix or remove "+config.Dir()+"/config.json"), c.pretty, stdout)
		return exitCfg
	}
	res, err := config.Resolve(cfg, config.LoadState(), c.profile, c.bu)
	if err != nil {
		_ = output.Print(output.Fail(0, err.Error(), "see: mcecli help use"), c.pretty, stdout)
		return exitCfg
	}

	data := map[string]any{
		"profile":            res.Name,
		"bu":                 res.BUName,
		"mid":                res.MID,
		"creds":              res.CredSource, // "profile" or "per-BU"
		"subdomain":          res.Profile.Subdomain,
		"auth_host":          hostOnly(res.AuthURL()),
		"rest_host":          hostOnly(res.RestURL()),
		"token_cached":       auth.Peek(res) != nil,
		"token_expires_in_s": auth.TTLSeconds(res),
		"session":            config.EnvGet(config.EnvSession),
	}
	if t := auth.Peek(res); t != nil && t.Scope != "" {
		data["scopes"] = truncScopes(t.Scope)
	}
	if res.MID == "" {
		data["mid"] = "(account-level — no BU selected)"
		data["hint"] = "scope to a BU: mcecli bu add <name> <mid> ; mcecli use " + res.Name + " <name>"
	}
	if len(res.Profile.BUs) == 0 {
		data["buses"] = "none configured"
	}
	e := output.OK(0, data)

	if c.check {
		tok, err := auth.Get(res, true)
		if err != nil {
			_ = output.Print(output.Fail(0, err.Error(), "verify credentials with: mcecli auth test"), c.pretty, stdout)
			return exitAPI
		}
		data["token_cached"] = true
		data["token_expires_in_s"] = auth.TTLSeconds(res)
		data["scopes"] = truncScopes(tok.Scope)
		data["rest_instance_url"] = tok.RestInstanceURL
	}
	_ = output.Print(e, c.pretty, stdout)
	return exitOK
}

func cmdAuth(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "test" {
		fmt.Fprint(stderr, "usage: mcecli auth test [--refresh] [--all-bus]\n")
		return exitUsage
	}
	fs := flag.NewFlagSet("auth test", flag.ContinueOnError)
	var c common
	addCommon(fs, &c)
	fs.BoolVar(&c.refresh, "refresh", false, "force a new token (ignore cache)")
	fs.BoolVar(&c.allBus, "all-bus", false, "test the account token AND every configured BU")
	if err := parseCmd(fs, args[1:]); err != nil {
		return exitUsage
	}
	if c.allBus && c.bu != "" {
		_ = output.Print(output.Fail(0, "--all-bus and --bu are mutually exclusive", ""), c.pretty, stdout)
		return exitUsage
	}

	cfg, err := config.Load()
	if err != nil {
		_ = output.Print(output.Fail(0, err.Error(), ""), c.pretty, stdout)
		return exitCfg
	}
	res, err := config.Resolve(cfg, config.LoadState(), c.profile, c.bu)
	if err != nil {
		_ = output.Print(output.Fail(0, err.Error(), "see: mcecli help use"), c.pretty, stdout)
		return exitCfg
	}

	if !c.allBus {
		tok, err := auth.Get(res, c.refresh)
		if err != nil {
			hint := "check subdomain/client_id/client_secret for profile " + res.Name
			if res.CredSource == "per-BU" {
				hint = "credential has no access to this BU — verify per-BU creds of BU '" + res.BUName + "' in config.json"
			}
			_ = output.Print(output.Fail(0, err.Error(), hint), c.pretty, stdout)
			return exitAPI
		}
		_ = output.Print(output.OK(200, map[string]any{
			"profile":           res.Name,
			"bu":                res.BUName,
			"mid":               res.MID,
			"creds":             res.CredSource,
			"scopes":            tok.Scope,
			"expires_in":        tok.ExpiresIn,
			"rest_instance_url": tok.RestInstanceURL,
			"soap_instance_url": tok.SoapInstanceURL,
			"cached":            !c.refresh,
		}), c.pretty, stdout)
		return exitOK
	}

	// --all-bus: validate the account-level token plus every configured BU
	// pairing. Catches credentials that cannot access their BU before any
	// real command runs. Cached tokens count as ok (validated when minted);
	// pass --refresh to re-mint everything.
	targets := []string{"-"} // sentinel: account-level
	names := make([]string, 0, len(res.Profile.BUs))
	for n := range res.Profile.BUs {
		names = append(names, n)
	}
	sort.Strings(names)
	targets = append(targets, names...)

	results := []any{}
	anyFail := false
	for _, bu := range targets {
		r2, rerr := config.Resolve(cfg, config.State{}, c.profile, bu)
		if rerr != nil {
			results = append(results, map[string]any{"bu": bu, "ok": false, "error": rerr.Error()})
			anyFail = true
			continue
		}
		label := r2.BUName
		if label == "" {
			label = "(account)"
		}
		entry := map[string]any{"bu": label, "mid": r2.MID, "creds": r2.CredSource}
		tok, gerr := auth.Get(r2, c.refresh)
		if gerr != nil {
			entry["ok"] = false
			entry["error"] = gerr.Error()
			anyFail = true
		} else {
			entry["ok"] = true
			entry["cached"] = !c.refresh
			if tok.Scope != "" {
				entry["scopes"] = tok.Scope
			}
		}
		results = append(results, entry)
	}

	var e *output.Envelope
	if anyFail {
		e = &output.Envelope{OK: false, Count: len(results), Data: results,
			Hint: "fix or remove the failing BU entries; per-BU creds live inside the bus entry in config.json"}
	} else {
		e = output.OK(0, results)
		e.Count = len(results)
		e.Hint = "all credential/BU pairings valid"
	}
	_ = output.Print(e, c.pretty, stdout)
	if anyFail {
		return exitAPI
	}
	return exitOK
}
func hostOnly(base string) string {
	for _, p := range []string{"https://", "http://"} {
		if len(base) > len(p) && base[:len(p)] == p {
			rest := base[len(p):]
			for i := 0; i < len(rest); i++ {
				if rest[i] == '/' {
					return rest[:i]
				}
			}
			return rest
		}
	}
	return base
}

// truncScopes keeps status output small: full scope lists cost agents
// hundreds of tokens; auth test shows the complete list.
func truncScopes(s string) string {
	f := strings.Fields(s)
	if len(f) <= 8 {
		return s
	}
	return strings.Join(f[:8], " ") + fmt.Sprintf(" …(+%d more — mcecli auth test for full list)", len(f)-8)
}
