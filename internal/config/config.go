// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package config loads and saves mcecli configuration (profiles, business units)
// and per-machine state (current profile / BU).
//
// Files live in ~/.mcecli (override with MCECLI_HOME — used by tests):
//
//	config.json  profiles: subdomain, credentials, named BUs
//	state.json   current profile + BU (set via `mcecli use`)
//	tokens.json  OAuth token cache (owned by internal/auth)
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// BUEntry is one business unit. Simple config form:
//
//	"bus": { "parent": "111111112" }
//
// inherits the profile credentials. Object form carries per-BU credentials
// for installed packages scoped to a single BU:
//
//	"bus": { "region": {"mid":"111111116","client_id":"...","client_secret":"..."} }
type BUEntry struct {
	MID          string `json:"mid"`
	ClientID     string `json:"client_id,omitempty"`
	ClientSecret string `json:"client_secret,omitempty"`
}

// UnmarshalJSON accepts both the string ("123") and object form.
func (b *BUEntry) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		b.MID = s
		return nil
	}
	type alias BUEntry
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*b = BUEntry(a)
	return nil
}

// Profile is one SFMC installed-package credential set.
type Profile struct {
	Subdomain    string              `json:"subdomain"` // e.g. "abc123" -> https://abc123.rest.marketingcloudapis.com
	ClientID     string              `json:"client_id"`
	ClientSecret string              `json:"client_secret"`
	BUs          map[string]*BUEntry `json:"bus,omitempty"`       // name -> BU (may carry own creds)
	AuthBase     string              `json:"auth_base,omitempty"` // override, for testing/probing
	RestBase     string              `json:"rest_base,omitempty"` // override, for testing/probing
	SoapBase     string              `json:"soap_base,omitempty"` // override, for testing/probing
}

// Config is the whole config.json.
type Config struct {
	Profiles map[string]*Profile `json:"profiles"`
}

// State is state.json: the current profile and BU.
type State struct {
	Profile string `json:"profile,omitempty"`
	BU      string `json:"bu,omitempty"`
}

// Environment variable overrides.
const (
	EnvProfile   = "MCECLI_PROFILE"
	EnvBU        = "MCECLI_BU"
	EnvSubdomain = "MCECLI_SUBDOMAIN"
	EnvClientID  = "MCECLI_CLIENT_ID"
	EnvClientSec = "MCECLI_CLIENT_SECRET"
	EnvAccountID = "MCECLI_ACCOUNT_ID"
	EnvSession   = "MCECLI_SESSION"
	EnvNoProd    = "MCECLI_NO_PROD"
)

// envGet reads an MCECLI_* variable, falling back to its legacy MCX_* name
// so pre-rename scripts keep working during transition.
func EnvGet(fullName string) string {
	if v := os.Getenv(fullName); v != "" {
		return v
	}
	if strings.HasPrefix(fullName, "MCECLI_") {
		return os.Getenv("MCX_" + strings.TrimPrefix(fullName, "MCECLI_"))
	}
	return ""
}

// defaultStateName is the state file used when MCECLI_SESSION is unset.
const defaultStateName = "state.json"

// Session returns the current session name ("" = default session).
func Session() string {
	s := EnvGet(EnvSession)
	if s == "" {
		return ""
	}
	return SanitizeSessionName(s)
}

// stateFileName returns the state file name for the current session.
// MCECLI_SESSION=agent1 -> state.agent1.json
// The session name is sanitized ([a-z0-9_-], everything else -> '-') so a
// hostile MCECLI_SESSION cannot traverse out of the mcecli home directory.
func stateFileName() string {
	if s := EnvGet(EnvSession); s != "" {
		return "state." + SanitizeSessionName(s) + ".json"
	}
	return defaultStateName
}

func SanitizeSessionName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	if b.Len() == 0 {
		return defaultSessionName
	}
	return b.String()
}

const defaultSessionName = "default"

// Dir returns the mcecli home directory.
func Dir() string {
	if d := os.Getenv("MCECLI_HOME"); d != "" {
		return d
	}
	// MCX_HOME legacy fallback (pre-rename installs)
	if d := os.Getenv("MCX_HOME"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	newDir := filepath.Join(home, ".mcecli")
	legacy := filepath.Join(home, ".mcx")
	// one-time migration: keep credentials/state when upgrading from mcx
	if _, err := os.Stat(newDir); os.IsNotExist(err) {
		if _, err := os.Stat(legacy); err == nil {
			if rerr := os.Rename(legacy, newDir); rerr != nil {
				// concurrent migration race — new dir may exist now
				if _, err2 := os.Stat(newDir); err2 != nil {
					return legacy // rename failed for a real reason; stay on legacy
				}
			}
		}
	}
	return newDir
}

// Load reads config.json; a missing file yields an empty config.
func Load() (*Config, error) {
	cfg := &Config{Profiles: map[string]*Profile{}}
	b, err := os.ReadFile(filepath.Join(Dir(), "config.json"))
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	if err := json.Unmarshal(b, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", filepath.Join(Dir(), "config.json"), err)
	}
	if cfg.Profiles == nil {
		cfg.Profiles = map[string]*Profile{}
	}
	return cfg, nil
}

// Save writes config.json.
func Save(cfg *Config) error {
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(Dir(), "config.json"), append(b, '\n'), 0o600)
}

// LoadState reads the session state file; a missing file yields zero state.
// File is state.json by default, or state.<MCECLI_SESSION>.json when set.
// Multiple agents can run concurrently with different MCECLI_SESSION values.
func LoadState() State {
	var s State
	if b, err := os.ReadFile(filepath.Join(Dir(), stateFileName())); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

// SaveState writes the session state file atomically (temp file + rename).
// This is safe for concurrent writes: different MCECLI_SESSION values write different
// files; same session writes are atomic so the worst-case is a lost update,
// not a corrupted file.
func SaveState(st State) error {
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(Dir(), stateFileName())
	tmp, err := os.CreateTemp(Dir(), "state-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	tmp.Close()
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, path)
}

// Resolved is the effective connection for one call.
type Resolved struct {
	Name       string   // profile name
	Profile    *Profile // effective profile (per-BU + env overrides applied to a copy)
	BUName     string   // "" if no BU selected (account-level token)
	MID        string   // account id sent with the token request ("" = parent)
	CredSource string   // "profile" or "per-BU" (BU entry carried own creds)
}

// Context describes the effective execution context for one call.
// Injected into every envelope (additive field) so agents can always
// verify WHICH org/profile/session they just hit — the "silent context
// switch" class of incident becomes visible in stdout itself.
type Context struct {
	Profile string `json:"profile,omitempty"`
	MID     string `json:"mid,omitempty"`
	BU      string `json:"bu,omitempty"`
	Session string `json:"session,omitempty"`
	Prod    bool   `json:"prod,omitempty"`
}

// CurrentContext resolves the best-effort execution context from the
// session state (never errors — context is informational).
func CurrentContext() Context {
	var ctx Context
	ctx.Session = Session()
	st := LoadState()
	ctx.Profile = st.Profile
	ctx.BU = st.BU
	ctx.Prod = IsProdName(st.Profile)
	if cfg, err := Load(); err == nil {
		if st.Profile != "" {
			if pr, canonical := FindProfile(cfg, st.Profile); pr != nil {
				ctx.Profile = canonical
				if pr.BUs != nil {
					if bu, ok := pr.BUs[st.BU]; ok && bu != nil {
						ctx.MID = bu.MID
					}
				}
			}
		}
	}
	return ctx
}

var prodNoticeOnce sync.Once

// WarnProdOnce prints the PROD-context notice at most once per process
// (it used to fire on every call — noisy for multi-call agent runs).
func WarnProdOnce(profile string) {
	prodNoticeOnce.Do(func() {
		if !IsProdName(profile) {
			return
		}
		fmt.Fprintf(os.Stderr, "\n⚠️  PROD-named profile %q in use. Writes stay gated; MCECLI_NO_PROD=1 hard-refuses PROD profiles.\n\n", profile)
	})
}

// Resolve picks profile and BU: command flags > state.json > env vars >
// single-profile fallback.
func Resolve(cfg *Config, st State, profileFlag, buFlag string) (*Resolved, error) {
	name := profileFlag
	if name == "" {
		name = st.Profile
	}
	if name == "" {
		name = EnvGet(EnvProfile)
	}
	if name == "" && len(cfg.Profiles) == 1 {
		for k := range cfg.Profiles {
			name = k
		}
	}
	if name == "" {
		return nil, errors.New("no profile selected; run 'mcecli profile add <name> --subdomain ... --client-id ... --client-secret ...' then 'mcecli use <name>'")
	}
	argName := name
	src := cfg.Profiles[name]
	if src == nil {
		src, name = lookupProfileCI(cfg, name) // case-insensitive fallback
	}
	if src == nil {
		return nil, fmt.Errorf("unknown profile %q (have %v)", argName, profileNames(cfg))
	}

	p := *src // shallow copy; BUs map shared, read-only here

	// PROD safety: warn (or hard-fail with MCECLI_NO_PROD=1) when a profile whose
	// name looks like a production context is selected. This prevents the
	// "one agent switches the shared state to PROD while another writes to it"
	// class of incident. The check uses the canonical profile name from config,
	// so it fires even when the caller passes a case-variant or substring flag.
	if IsProdName(name) && EnvGet(EnvNoProd) != "" {
		return nil, fmt.Errorf("PROD profile %q refused: set MCECLI_NO_PROD=1 to allow", name)
	}
	WarnProdOnce(name)

	bu := buFlag
	if bu == "" {
		bu = st.BU
	}
	if bu == "" {
		bu = EnvGet(EnvBU)
	}
	if bu == "-" {
		bu = "" // sentinel: explicitly account-level ("mcecli auth test --bu -")
	}
	mid := ""
	credSource := "profile"
	if bu != "" {
		if entry, ok := p.BUs[bu]; ok {
			mid = entry.MID
			if entry.ClientID != "" {
				if entry.ClientSecret == "" {
					return nil, fmt.Errorf("BU %q has per-BU client_id but no client_secret in config", bu)
				}
				p.ClientID = entry.ClientID
				p.ClientSecret = entry.ClientSecret
				credSource = "per-BU"
			}
		} else if entry, canonical, ok := lookupBUCI(p, bu); ok {
			mid, bu = entry.MID, canonical // case-insensitive BU match
			if entry.ClientID != "" {
				if entry.ClientSecret == "" {
					return nil, fmt.Errorf("BU %q has per-BU client_id but no client_secret in config", bu)
				}
				p.ClientID = entry.ClientID
				p.ClientSecret = entry.ClientSecret
				credSource = "per-BU"
			}
		} else if isMID(bu) {
			mid = bu // raw numeric MID passed directly
		} else {
			return nil, fmt.Errorf("unknown BU %q in profile %q (have %v) — or pass a raw numeric MID", bu, name, buNames(p))
		}
	}

	// env overrides win over everything (explicit session intent)
	if v := EnvGet(EnvSubdomain); v != "" {
		p.Subdomain = v
	}
	if v := EnvGet(EnvClientID); v != "" {
		p.ClientID = v
	}
	if v := EnvGet(EnvClientSec); v != "" {
		p.ClientSecret = v
	}
	if v := EnvGet(EnvAccountID); v != "" && mid == "" {
		mid = v
	}
	return &Resolved{Name: name, Profile: &p, BUName: bu, MID: mid, CredSource: credSource}, nil
}

// AuthURL returns the auth host base URL.
func (r *Resolved) AuthURL() string {
	if r.Profile.AuthBase != "" {
		return trimSlash(r.Profile.AuthBase)
	}
	return "https://" + r.Profile.Subdomain + ".auth.marketingcloudapis.com"
}

// RestURL returns the REST host base URL.
func (r *Resolved) RestURL() string {
	if r.Profile.RestBase != "" {
		return trimSlash(r.Profile.RestBase)
	}
	return "https://" + r.Profile.Subdomain + ".rest.marketingcloudapis.com"
}

// SoapURL returns the SOAP endpoint base URL.
func (r *Resolved) SoapURL() string {
	if r.Profile.SoapBase != "" {
		return trimSlash(r.Profile.SoapBase)
	}
	return "https://" + r.Profile.Subdomain + ".soap.marketingcloudapis.com"
}

// FindProfile case-insensitively finds a profile; returns canonical name.
func FindProfile(cfg *Config, name string) (*Profile, string) {
	return lookupProfileCI(cfg, name)
}

// FindBU case-insensitively finds a BU; returns canonical name.
func FindBU(p Profile, name string) (*BUEntry, string, bool) {
	return lookupBUCI(p, name)
}

func profileNames(cfg *Config) []string {
	out := make([]string, 0, len(cfg.Profiles))
	for k := range cfg.Profiles {
		out = append(out, k)
	}
	return out
}

// lookupProfileCI finds a profile case-insensitively; returns canonical name.
func lookupProfileCI(cfg *Config, name string) (*Profile, string) {
	for k, v := range cfg.Profiles {
		if strings.EqualFold(k, name) {
			return v, k
		}
	}
	return nil, ""
}

// lookupBUCI finds a BU case-insensitively; returns canonical name.
func lookupBUCI(p Profile, name string) (*BUEntry, string, bool) {
	for k, v := range p.BUs {
		if strings.EqualFold(k, name) {
			return v, k, true
		}
	}
	return nil, "", false
}

func buNames(p Profile) []string {
	out := make([]string, 0, len(p.BUs))
	for k := range p.BUs {
		out = append(out, k)
	}
	return out
}

func isMID(s string) bool {
	_, err := strconv.Atoi(s)
	return err == nil
}

// isProdName returns true if the profile name looks like a production context.
// Used to surface a safety warning (or hard-block via MCECLI_NO_PROD).
// IsProdName returns true if name looks like a production context.
// Used to surface a safety warning (or hard-block via MCECLI_NO_PROD).
func IsProdName(name string) bool {
	n := strings.ToLower(name)
	return strings.Contains(n, "prod") || strings.Contains(n, "prd") ||
		strings.Contains(n, "production") || strings.Contains(n, "live") ||
		strings.Contains(n, "dc-prod")
}

// Sessions returns the list of named session state files found in Dir().
// state.json (the default session) is excluded. Used by the session list command.
func Sessions() []string {
	entries, err := os.ReadDir(Dir())
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		// Exclude state.json (the default session — not a named session).
		if name == defaultStateName {
			continue
		}
		if strings.HasPrefix(name, "state.") && strings.HasSuffix(name, ".json") {
			session := strings.TrimSuffix(strings.TrimPrefix(name, "state."), ".json")
			if session != "" {
				out = append(out, session)
			}
		}
	}
	return out
}

func trimSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}
