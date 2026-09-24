// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package auth obtains and caches SFMC v2 OAuth server-to-server tokens.
//
// Tokens are cached on disk (~/.mcecli/tokens.json) keyed by subdomain|mid,
// refreshed 120s before expiry, and never printed. Tokens are scoped per
// business unit when a MID is resolved.
package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dawidmachon/mcecli/internal/config"
	"github.com/dawidmachon/mcecli/internal/httpc"
)

// Token is the subset of the v2/token response mcecli cares about.
type Token struct {
	AccessToken     string `json:"access_token"`
	TokenType       string `json:"token_type"`
	ExpiresIn       int    `json:"expires_in"`
	Scope           string `json:"scope"`
	RestInstanceURL string `json:"rest_instance_url"`
	SoapInstanceURL string `json:"soap_instance_url"`
}

type cached struct {
	Token
	ExpiresAt int64 `json:"expires_at"` // unix seconds
}

type cache map[string]*cached

const refreshMargin = 120 * time.Second

func cacheFile() string { return filepath.Join(config.Dir(), "tokens.json") }

func loadCache() cache {
	m := cache{}
	if b, err := os.ReadFile(cacheFile()); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func saveCache(m cache) {
	if err := os.MkdirAll(config.Dir(), 0o700); err != nil {
		return
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return
	}
	cf := cacheFile()
	tmp := cf + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return
	}
	// Atomic on Windows (same volume guaranteed). Ignore rename errors —
	// worst case: next Get() re-fetches. Never truncate the live file first.
	_ = os.Rename(tmp, cf)
}

func key(r *config.Resolved) string {
	// client_id is part of the key: two credentials targeting the same
	// subdomain+MID (e.g. parent package vs per-BU package) must not share
	// cache entries.
	return r.Profile.Subdomain + "|" + r.MID + "|" + r.Profile.ClientID
}

// Get returns a valid token for the resolved profile/BU, using the on-disk
// cache unless force is set. Network only on cache miss or expiry.
func Get(r *config.Resolved, force bool) (*Token, error) {
	k := key(r)
	m := loadCache()
	if !force {
		if c := m[k]; c != nil && time.Now().Unix() < c.ExpiresAt {
			return &c.Token, nil
		}
	}

	body := map[string]any{
		"grant_type":    "client_credentials",
		"client_id":     r.Profile.ClientID,
		"client_secret": r.Profile.ClientSecret,
	}
	if r.MID != "" {
		n, err := strconv.Atoi(r.MID)
		if err != nil {
			return nil, fmt.Errorf("account id %q is not numeric", r.MID)
		}
		body["account_id"] = n
	}
	b, _ := json.Marshal(body)

	res, err := httpc.Do(context.Background(), http.MethodPost, r.AuthURL()+"/v2/token", "", b, nil)
	if err != nil {
		return nil, fmt.Errorf("auth request failed: %w", err)
	}
	if res.Status != http.StatusOK {
		var e struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		_ = json.Unmarshal(res.Body, &e)
		msg := e.Error
		if e.Description != "" {
			msg = e.Error + ": " + e.Description
		}
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", res.Status)
		}
		detail := "check subdomain/client_id/client_secret in config"
		if r.CredSource == "per-BU" {
			detail = "check per-BU client_id/client_secret of BU '" + r.BUName + "' in config"
		}
		return nil, errors.New("auth failed — " + msg + " (" + detail + ")")
	}

	var tok Token
	if err := json.Unmarshal(res.Body, &tok); err != nil {
		return nil, fmt.Errorf("parse token response: %w", err)
	}
	margin := refreshMargin
	if d := time.Duration(tok.ExpiresIn) * time.Second; d < margin*2 {
		margin = d / 10
	}
	m[k] = &cached{
		Token:     tok,
		ExpiresAt: time.Now().Add(time.Duration(tok.ExpiresIn)*time.Second - margin).Unix(),
	}
	saveCache(m)
	return &tok, nil
}

// Invalidate drops the cached token (e.g. after an unexpected 401).
func Invalidate(r *config.Resolved) {
	m := loadCache()
	delete(m, key(r))
	saveCache(m)
}

// Peek returns the cached token without touching the network, or nil.
func Peek(r *config.Resolved) *Token {
	c := loadCache()[key(r)]
	if c == nil || time.Now().Unix() >= c.ExpiresAt {
		return nil
	}
	return &c.Token
}

// TTLSeconds returns remaining cache lifetime in seconds (0 if none).
func TTLSeconds(r *config.Resolved) int {
	c := loadCache()[key(r)]
	if c == nil {
		return 0
	}
	if d := c.ExpiresAt - time.Now().Unix(); d > 0 {
		return int(d)
	}
	return 0
}

// JWTEnterpriseID decodes the enterprise MID (eid) from the access token's
// JWT payload. VERIFIED live: the v2/token JWT carries eid. Returns
// (0, false) when the token is not a JWT or carries no eid — callers must
// fall back to other discovery sources (contacts/v1/schema).
func JWTEnterpriseID(accessToken string) (int, bool) {
	parts := strings.Split(accessToken, ".")
	if len(parts) < 2 {
		return 0, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return 0, false
	}
	var m struct {
		EID int `json:"eid"`
	}
	if json.Unmarshal(payload, &m) != nil || m.EID == 0 {
		return 0, false
	}
	return m.EID, true
}
