// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, dir string) {
	t.Helper()
	cfg := &Config{Profiles: map[string]*Profile{
		"p1": {
			Subdomain: "s1", ClientID: "id1", ClientSecret: "sec1",
			BUs: map[string]*BUEntry{
				"parent": {MID: "111"},
				"region": {MID: "222"},
				"solo":   {MID: "333", ClientID: "id3", ClientSecret: "sec3"}, // own creds
			},
		},
	}}
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	_ = dir
}

func TestResolvePrecedence(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MCECLI_HOME", dir)
	writeConfig(t, dir)

	// single-profile fallback + no BU -> account level
	r, err := Resolve(mustLoad(t), State{}, "", "")
	if err != nil || r.Name != "p1" || r.MID != "" || r.BUName != "" {
		t.Fatalf("single-profile fallback failed: %+v %v", r, err)
	}

	// state selects BU
	r, err = Resolve(mustLoad(t), State{Profile: "p1", BU: "parent"}, "", "")
	if err != nil || r.MID != "111" {
		t.Fatalf("state BU failed: %+v %v", r, err)
	}

	// flag overrides state
	r, err = Resolve(mustLoad(t), State{Profile: "p1", BU: "parent"}, "", "region")
	if err != nil || r.MID != "222" || r.BUName != "region" {
		t.Fatalf("flag BU override failed: %+v %v", r, err)
	}

	// raw numeric MID accepted
	r, err = Resolve(mustLoad(t), State{}, "", "999")
	if err != nil || r.MID != "999" {
		t.Fatalf("raw MID failed: %+v %v", r, err)
	}

	// '-' sentinel: explicitly account-level even when state has a BU
	r, err = Resolve(mustLoad(t), State{Profile: "p1", BU: "parent"}, "", "-")
	if err != nil || r.BUName != "" || r.MID != "" {
		t.Fatalf("'-' sentinel must clear BU: %+v %v", r, err)
	}

	// unknown BU rejected
	if _, err = Resolve(mustLoad(t), State{}, "", "nope"); err == nil {
		t.Fatal("unknown BU must fail")
	}

	// per-BU credentials override the profile copy (profile file untouched)
	r, err = Resolve(mustLoad(t), State{}, "", "solo")
	if err != nil || r.MID != "333" || r.Profile.ClientID != "id3" || r.Profile.ClientSecret != "sec3" || r.CredSource != "per-BU" {
		t.Fatalf("per-BU credentials failed: %+v %v", r, err)
	}
	fresh, _ := Load()
	if fresh.Profiles["p1"].ClientID != "id1" {
		t.Fatal("per-BU creds must not leak into stored profile")
	}

	// env account id used when nothing else set
	t.Setenv("MCECLI_ACCOUNT_ID", "555")
	r, err = Resolve(mustLoad(t), State{}, "", "")
	if err != nil || r.MID != "555" {
		t.Fatalf("env account id failed: %+v %v", r, err)
	}

	// env subdomain overrides profile
	t.Setenv("MCECLI_SUBDOMAIN", "zzz")
	r, err = Resolve(mustLoad(t), State{}, "", "")
	if err != nil || r.Profile.Subdomain != "zzz" {
		t.Fatalf("env subdomain override failed: %+v %v", r, err)
	}
	if r.RestURL() != "https://zzz.rest.marketingcloudapis.com" {
		t.Fatalf("RestURL: %s", r.RestURL())
	}
	if r.AuthURL() != "https://zzz.auth.marketingcloudapis.com" {
		t.Fatalf("AuthURL: %s", r.AuthURL())
	}
}

func TestLoadMissingConfig(t *testing.T) {
	t.Setenv("MCECLI_HOME", filepath.Join(t.TempDir(), "nonexistent"))
	cfg, err := Load()
	if err != nil || len(cfg.Profiles) != 0 {
		t.Fatalf("missing config must yield empty config: %v %v", cfg, err)
	}
}

func TestBUEntryJSONForms(t *testing.T) {
	t.Setenv("MCECLI_HOME", t.TempDir())
	json := `{"profiles":{"p":{"subdomain":"s","client_id":"i","client_secret":"c",
		"bus":{"x":"123","y":{"mid":"456","client_id":"i2","client_secret":"c2"}}}}}`
	if err := os.WriteFile(filepath.Join(Dir(), "config.json"), []byte(json), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.Profiles["p"]
	if p.BUs["x"].MID != "123" || p.BUs["x"].ClientID != "" {
		t.Fatalf("string form: %+v", p.BUs["x"])
	}
	if p.BUs["y"].MID != "456" || p.BUs["y"].ClientID != "i2" {
		t.Fatalf("object form: %+v", p.BUs["y"])
	}

	// per-BU entry with client_id but no secret must be rejected at resolve
	rp := *p
	rp.BUs = map[string]*BUEntry{"bad": {MID: "789", ClientID: "i3"}}
	if _, err := Resolve(&Config{Profiles: map[string]*Profile{"p": &rp}}, State{}, "", "bad"); err == nil {
		t.Fatal("per-BU client_id without secret must fail")
	}
}

func TestConfigRoundtrip(t *testing.T) {
	t.Setenv("MCECLI_HOME", t.TempDir())
	writeConfig(t, ".")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.Profiles["p1"]
	if p == nil || p.Subdomain != "s1" || p.BUs["parent"].MID != "111" {
		t.Fatalf("roundtrip failed: %+v", p)
	}
}

func TestStateRoundtrip(t *testing.T) {
	t.Setenv("MCECLI_HOME", t.TempDir())
	if err := SaveState(State{Profile: "p1", BU: "region"}); err != nil {
		t.Fatal(err)
	}
	st := LoadState()
	if st.Profile != "p1" || st.BU != "region" {
		t.Fatalf("state roundtrip failed: %+v", st)
	}
}

func TestStateFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX perm bits are not enforced on Windows; 0600 still set for Unix")
	}
	t.Setenv("MCECLI_HOME", t.TempDir())
	_ = SaveState(State{Profile: "p1"})
	fi, err := os.Stat(filepath.Join(Dir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("state.json must be owner-only, got %v", fi.Mode().Perm())
	}
}

func mustLoad(t *testing.T) *Config {
	t.Helper()
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestCaseInsensitiveLookups(t *testing.T) {
	t.Setenv("MCECLI_HOME", t.TempDir())
	writeConfig(t, ".")
	cfg := mustLoad(t)
	if p, canonical := FindProfile(cfg, "P1"); p == nil || canonical != "p1" {
		t.Fatalf("FindProfile failed: %v %q", p, canonical)
	}
	if _, _, ok := FindBU(*cfg.Profiles["p1"], "PARENT"); !ok {
		t.Fatal("FindBU case-insensitive failed")
	}
}

// TestSessionSanitization: a hostile MCECLI_SESSION must not traverse out of
// the mcecli home directory (multi-agent hardening 2026-09-17).
func TestSessionSanitization(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MCECLI_HOME", dir)
	t.Setenv("MCECLI_SESSION", "../../evil")
	got := stateFileName()
	if got != "state.--------evil.json" && got != "state.-----evil.json" {
		// exact '-' count varies with dots; the invariant is: no separators, no '..'
		if strings.Contains(got, "..") || strings.Contains(got, "/") || strings.Contains(got, "\\") {
			t.Fatalf("session name must not traverse: %q", got)
		}
	}
	p := filepath.Join(Dir(), stateFileName())
	if !strings.HasPrefix(p, dir) {
		t.Fatalf("state path escaped mcecli home: %q", p)
	}
	// SaveState must land inside Dir()
	if err := SaveState(State{Profile: "x"}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	found := false
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "state.") && strings.HasSuffix(e.Name(), ".json") {
			found = true
		}
	}
	if !found {
		t.Fatalf("state file not written into mcecli home")
	}
}

// TestSessionIsolation: two sessions hold independent states.
func TestSessionIsolation(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MCECLI_HOME", dir)

	t.Setenv("MCECLI_SESSION", "agentA")
	if err := SaveState(State{Profile: "dev"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MCECLI_SESSION", "agentB")
	if st := LoadState(); st.Profile != "" {
		t.Fatalf("agentB must not see agentA state: %v", st)
	}
	if err := SaveState(State{Profile: "prod-safe-guarded"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MCECLI_SESSION", "agentA")
	if st := LoadState(); st.Profile != "dev" {
		t.Fatalf("agentA state corrupted: %v", st)
	}
}
