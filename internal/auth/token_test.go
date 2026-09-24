// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dawidmachon/mcecli/internal/config"
)

func testResolved(authBase string) *config.Resolved {
	return &config.Resolved{
		Name: "p",
		Profile: &config.Profile{
			Subdomain: "fake", ClientID: "cid", ClientSecret: "sec", AuthBase: authBase,
		},
		MID: "123",
	}
}

func TestGetCachesAndRefreshes(t *testing.T) {
	t.Setenv("MCECLI_HOME", t.TempDir())
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if r.URL.Path != "/v2/token" {
			t.Errorf("path=%s", r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["grant_type"] != "client_credentials" {
			t.Errorf("grant_type=%v", body["grant_type"])
		}
		if body["client_id"] != "cid" || body["client_secret"] != "sec" {
			t.Errorf("credentials not sent")
		}
		if body["account_id"] != float64(123) {
			t.Errorf("account_id must be numeric, got %v", body["account_id"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"T1","token_type":"Bearer","expires_in":1200,"scope":"data_extensions_read","rest_instance_url":"https://r.example/","soap_instance_url":"https://s.example/"}`))
	}))
	defer srv.Close()

	r := testResolved(srv.URL)
	tok, err := Get(r, false)
	if err != nil || tok.AccessToken != "T1" {
		t.Fatalf("first get: %v %v", tok, err)
	}
	// second call must hit the cache, not the network
	tok2, err := Get(r, false)
	if err != nil || tok2.AccessToken != "T1" || n != 1 {
		t.Fatalf("cache miss! n=%d err=%v", n, err)
	}
	if Peek(r) == nil || TTLSeconds(r) <= 0 {
		t.Fatal("peek/ttl must see the cached token")
	}
	// force refresh
	if _, err = Get(r, true); err != nil || n != 2 {
		t.Fatalf("force refresh failed n=%d err=%v", n, err)
	}
	// invalidate drops the cache
	Invalidate(r)
	if Peek(r) != nil || TTLSeconds(r) != 0 {
		t.Fatal("invalidate must clear the cache")
	}
}

func TestCacheKeyIncludesClientID(t *testing.T) {
	t.Setenv("MCECLI_HOME", t.TempDir())
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		_, _ = w.Write([]byte(`{"access_token":"T","expires_in":1200}`))
	}))
	defer srv.Close()

	mk := func(cid string) *config.Resolved {
		r := testResolved(srv.URL)
		r.Profile.ClientID = cid
		return r
	}
	// same subdomain+MID, different credentials -> separate cache entries
	if _, err := Get(mk("credA"), false); err != nil {
		t.Fatal(err)
	}
	if _, err := Get(mk("credB"), false); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("different creds must not share cache entries (n=%d)", n)
	}
	if _, err := Get(mk("credA"), false); err != nil || n != 2 {
		t.Fatalf("cache hit expected (n=%d err=%v)", n, err)
	}
}

func TestAuthErrorNormalization(t *testing.T) {
	t.Setenv("MCECLI_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_client","error_description":"Client ID not valid"}`))
	}))
	defer srv.Close()

	_, err := Get(testResolved(srv.URL), false)
	if err == nil {
		t.Fatal("expected error")
	}
	if msg := err.Error(); !strings.Contains(msg, "invalid_client") || !strings.Contains(msg, "Client ID not valid") {
		t.Fatalf("error must surface SFMC error detail: %v", err)
	}
}

func TestPerBUAuthErrorContext(t *testing.T) {
	t.Setenv("MCECLI_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_client","error_description":"not accessible for account"}`))
	}))
	defer srv.Close()

	r := testResolved(srv.URL)
	r.CredSource = "per-BU"
	r.BUName = "region"
	_, err := Get(r, false)
	if err == nil {
		t.Fatal("expected error")
	}
	if msg := err.Error(); !strings.Contains(msg, "region") || !strings.Contains(msg, "per-BU") {
		t.Fatalf("error must name the failing BU entry: %v", err)
	}
}

func TestTokenCacheFileIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX perm bits are not enforced on Windows; 0600 still set for Unix")
	}
	t.Setenv("MCECLI_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"T","expires_in":1200}`))
	}))
	defer srv.Close()

	if _, err := Get(testResolved(srv.URL), false); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(config.Dir(), "tokens.json"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("tokens.json must be owner-only, got %v", fi.Mode().Perm())
	}
}
