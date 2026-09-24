// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dawidmachon/mcecli/internal/auth"
	"github.com/dawidmachon/mcecli/internal/config"
	"github.com/dawidmachon/mcecli/internal/httpc"
	"github.com/dawidmachon/mcecli/internal/journal"
	"github.com/dawidmachon/mcecli/internal/output"
)

// Exit codes (stable contract, documented in SKILL.md).
const (
	exitOK    = 0
	exitAPI   = 1
	exitUsage = 2
	exitCfg   = 3
)

// common holds flags shared by network commands.
type common struct {
	profile    string
	bu         string
	pretty     bool
	raw        bool
	write      bool
	confirm    bool
	noSnapshot bool
	check      bool
	refresh    bool
	allBus     bool
	fields     string
	page       int
	size       int
}

func addCommon(fs *flag.FlagSet, c *common) {
	fs.StringVar(&c.profile, "profile", "", "profile name (overrides current)")
	fs.StringVar(&c.bu, "bu", "", "BU name or numeric MID (overrides current)")
	fs.BoolVar(&c.pretty, "pretty", false, "pretty-print JSON output")
	fs.BoolVar(&c.raw, "raw", false, "print raw response body only (no envelope)")
}

func addPaging(fs *flag.FlagSet, c *common) {
	fs.IntVar(&c.page, "page", 0, "page number (1-based)")
	fs.IntVar(&c.size, "size", 0, "page size / max rows")
}

func addProjection(fs *flag.FlagSet, c *common) {
	fs.StringVar(&c.fields, "fields", "", "comma-separated field projection (dot paths ok)")
}

// session is a resolved, authenticated connection.
type session struct {
	res *config.Resolved
	tok *auth.Token
}

// newSession resolves config and returns a token (network on cache miss).
func newSession(c *common) (*session, *output.Envelope, int) {
	cfg, err := config.Load()
	if err != nil {
		return nil, output.Fail(0, err.Error(), "fix or remove "+config.Dir()+"/config.json"), exitCfg
	}
	res, err := config.Resolve(cfg, config.LoadState(), c.profile, c.bu)
	if err != nil {
		return nil, output.Fail(0, err.Error(), "see: mcecli help use"), exitCfg
	}
	if res.Profile.Subdomain == "" && res.Profile.RestBase == "" {
		return nil, output.Fail(0, "profile has no subdomain", "mcecli profile add <name> --subdomain ..."), exitCfg
	}
	tok, err := auth.Get(res, false)
	if err != nil {
		hint := "verify credentials with: mcecli auth test --profile " + res.Name
		if res.CredSource == "per-BU" {
			hint = "credential has no access to this BU — verify per-BU creds: mcecli auth test --bu " + res.BUName
		}
		return nil, output.Fail(0, err.Error(), hint), exitAPI
	}
	return &session{res: res, tok: tok}, nil, exitOK
}

// call performs one API call against the REST host. On 401 it refreshes the
// token once and retries. Returns (status, body) or a ready error envelope.
// Transport failures come back as status 0 with the error message in body.
func (s *session) call(method, path string, body []byte, headers map[string]string) (int, []byte, *output.Envelope, int) {
	full := s.res.RestURL() + "/" + strings.TrimPrefix(path, "/")
	status, resp := s.doCall(method, full, body, headers)
	if status == 0 {
		return 0, nil, output.Fail(0, string(resp), "network error reaching "+s.res.RestURL()), exitAPI
	}
	if status == http.StatusUnauthorized {
		auth.Invalidate(s.res)
		if tok, err := auth.Get(s.res, true); err == nil {
			s.tok = tok
			status, resp = s.doCall(method, full, body, headers)
			if status == 0 {
				return 0, nil, output.Fail(0, string(resp), "network error reaching "+s.res.RestURL()), exitAPI
			}
		}
	}
	return status, resp, nil, exitOK
}

// doCall is a thin wrapper around httpc.Do: on transport error it returns
// (0, errMessage).
func (s *session) doCall(method, full string, body []byte, headers map[string]string) (int, []byte) {
	res, err := httpc.Do(context.Background(), method, full, s.tok.AccessToken, body, headers)
	if err != nil {
		return 0, []byte(err.Error())
	}
	return res.Status, res.Body
}

// dangerPatterns flag operationally dangerous paths: message sends,
// journey lifecycle (stop/pause/resume/publish), automation runs,
// scheduling, and data destruction/clearing.
var dangerPatterns = []string{
	"/send", "/stop", "/start", "/pause", "/resume", "/publish",
	"/unpublish", "/cancel", "/schedule", "/run", "/execute",
	"/cleardata", "/clear", "/delete",
}

// overwriteCollections: PUT/PATCH against these collections replaces whole
// definitions (journeys, events, message definitions, automations).
var overwriteCollections = []string{
	"/interactions", "/eventdefinitions", "/messagedefinitionsends", "/automations", "/journey",
}

// classifyWrite tiers a non-GET call:
// \	tier 1: plain content write — needs --write
// \	tier 2: dangerous — needs --write AND --confirm
func classifyWrite(method, path string) (dangerous bool, reason string) {
	m := strings.ToUpper(method)
	if m == http.MethodDelete {
		return true, "DELETE destroys data"
	}
	lp := "/" + strings.ToLower(strings.Trim(path, "/")) + "/"
	for _, p := range dangerPatterns {
		// substring containment is intentional: /publish must also catch
		// /publishAsync, /send must catch segment-final /send — bias to
		// over-flagging dangerous candidates, never under.
		if strings.Contains(lp, p) {
			return true, fmt.Sprintf("path matches dangerous operation %q (sends, journey/automation lifecycle, scheduling, or data clearing)", p)
		}
	}
	if m == http.MethodPut || m == http.MethodPatch {
		for _, c := range overwriteCollections {
			if strings.Contains(lp, c) {
				return true, fmt.Sprintf("PUT/PATCH overwrites a whole %q definition", c)
			}
		}
	}
	return false, ""
}

// scopeAdvisory returns an informational note when NONE of the required
// scopes appear in the token's scope list. Advisory only: some tenants do not
// enforce scope names strictly (verified live — calls succeed without the
// scope), so the wording must never predict failure for a call that worked.
func scopeAdvisory(tok *auth.Token, anyOf ...string) string {
	if tok == nil || tok.Scope == "" || len(anyOf) == 0 {
		return ""
	}
	have := strings.Fields(tok.Scope)
	for _, need := range anyOf {
		for _, h := range have {
			if strings.EqualFold(h, need) {
				return ""
			}
		}
	}
	return "scope note: token lacks [" + strings.Join(anyOf, ", ") + "] (call succeeded — tenant may not enforce scope names)"
}

// addHelp registers a --help flag; callers print their usage when it is set.
func addHelp(fs *flag.FlagSet) *bool {
	h := fs.Bool("help", false, "show command help")
	return h
}

// undoSnapshot captures the CURRENT server state of a resource before an
// irreversible operation (DELETE). Stored under
// ~/.mcecli/work/<profile>/undo/<stamp>/ so rollback is possible with ordinary
// commands (re-POST the saved body). Returns the snapshot dir, or "" when
// the resource had no retrievable state.
func (s *session) undoSnapshot(method, fullURL, path string, gate *common) string {
	if !strings.EqualFold(method, http.MethodDelete) {
		return "" // only DELETE auto-snapshots today
	}
	status, resp, _, _ := s.call(http.MethodGet, path, nil, nil)
	if status >= 400 || len(resp) == 0 {
		return "" // nothing retrievable — recorded in journal as server_error
	}
	stamp := time.Now().UTC().Format("20060102-150405.000000000")
	dir := filepath.Join(config.Dir(), "work", s.res.Name, "undo", stamp+"-DELETE")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}
	_ = os.WriteFile(filepath.Join(dir, "response.json"), resp, 0o600)
	_ = os.WriteFile(filepath.Join(dir, "request.json"), []byte("DELETE "+fullURL), 0o600)
	return dir
}

// journalWriteSnap records a gated write with full snapshot metadata.
func (s *session) journalWriteSnap(c *common, method, fullURL string, status int, body []byte, dur time.Duration, requestID, snapshotState, undoPath string) {
	entry := journal.Entry{
		Profile:    s.res.Name,
		BU:         s.res.BUName,
		MID:        s.res.MID,
		Method:     method,
		URL:        fullURL,
		Status:     status,
		BodySHA256: journal.HashBody(body),
		BodyLen:    len(body),
		RequestID:  requestID,
		UndoPath:   undoPath,
		Snapshot:   snapshotState,
		DurationMS: dur.Milliseconds(),
	}
	if entry.BU == "" {
		entry.BU = "account"
	}
	if err := journal.Append(entry); err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: journal write failed: %v\n", err)
	}
}

// resolvePath validates the passthrough path: absolute URLs must match the
// profile's REST host; returns a path relative to the REST host root.
func resolvePath(r *config.Resolved, p string) (string, error) {
	if strings.HasPrefix(p, "http://") || strings.HasPrefix(p, "https://") {
		u, err := url.Parse(p)
		if err != nil {
			return "", fmt.Errorf("bad URL: %w", err)
		}
		hu, err := url.Parse(r.RestURL())
		if err != nil {
			return "", err
		}
		if !strings.EqualFold(u.Host, hu.Host) {
			return "", fmt.Errorf("refusing cross-host URL %q — profile REST host is %q", u.Host, hu.Host)
		}
		return strings.TrimPrefix(u.RequestURI(), "/"), nil
	}
	return strings.TrimPrefix(p, "/"), nil
}

// envelopeFor wraps a successful HTTP response in an envelope.
func envelopeFor(status int, body []byte) *output.Envelope {
	var parsed any
	if len(body) > 0 && json.Unmarshal(body, &parsed) == nil {
		return output.OK(status, parsed) // arrays auto-count
	}
	return output.OK(status, string(body))
}

// errorEnvelope converts an SFMC error response into an agent-actionable
// envelope with a hint.
func errorEnvelope(status int, body []byte, method string) *output.Envelope {
	var sf struct {
		Message   string `json:"message"`
		ErrorCode string `json:"errorcode"`
		Errors    []struct {
			Message string `json:"message"`
		} `json:"errors"`
		ValidationError struct {
			Message string `json:"message"`
		} `json:"validationErrors"`
	}
	_ = json.Unmarshal(body, &sf)
	msg := sf.Message
	if msg == "" && len(sf.Errors) > 0 {
		msg = sf.Errors[0].Message
	}
	if msg == "" {
		msg = strings.TrimSpace(string(body))
		if len(msg) > 500 {
			msg = msg[:500]
		}
	}
	if msg == "" {
		msg = fmt.Sprintf("HTTP %d", status)
	}
	hint := ""
	switch {
	case status == 401:
		hint = "token rejected even after refresh — check installed package validity/scopes in SFMC Setup"
	case status == 403:
		hint = "insufficient scope for " + method + " — extend the installed package scopes"
	case status == 404:
		hint = "path or resource key not found — verify endpoint and key spelling"
	case status == 429:
		hint = "rate limited — honor Retry-After and slow down (use mcecli de dump for bulk reads)"
	case status >= 500:
		hint = "server error — may be transient or an invalid job/id; retry once"
	}
	return output.Fail(status, msg, hint)
}

// local config helpers (config package keeps its own copies private)
func profileNames(cfg *config.Config) []string {
	out := make([]string, 0, len(cfg.Profiles))
	for k := range cfg.Profiles {
		out = append(out, k)
	}
	return out
}

func buNames(p config.Profile) []string {
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

// loadBody resolves --body: inline JSON, @file, @- / - (stdin).
func loadBody(arg string) ([]byte, error) {
	switch {
	case arg == "":
		return nil, nil
	case arg == "-" || arg == "@-": // both forms read stdin (Windows-safe)
		return io.ReadAll(stdin())
	case strings.HasPrefix(arg, "@"):
		return osReadFile(strings.TrimPrefix(arg, "@"))
	default:
		b := []byte(arg)
		if !json.Valid(b) {
			return nil, fmt.Errorf("--body is not valid JSON")
		}
		return b, nil
	}
}

// parseCmd parses flags that may appear in any position — Go's flag package
// stops at the first positional argument, but natural CLI usage is
// "mcecli rest GET data/v1/... --fields a" with flags after the path.
func parseCmd(fs *flag.FlagSet, args []string) error {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if len(a) > 1 && a[0] == '-' {
			flags = append(flags, a)
			name := strings.TrimLeft(a, "-")
			if !strings.Contains(name, "=") {
				if f := fs.Lookup(name); f != nil && !isBoolFlag(f) && i+1 < len(args) {
					i++
					flags = append(flags, args[i])
				}
			}
			continue
		}
		pos = append(pos, a)
	}
	return fs.Parse(append(flags, pos...))
}

func isBoolFlag(f *flag.Flag) bool {
	bf, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && bf.IsBoolFlag()
}

// multiFlag collects repeated --header "Key: value" flags.
type multiFlag struct{ items []string }

func (m *multiFlag) String() string { return strings.Join(m.items, ",") }
func (m *multiFlag) Set(v string) error {
	m.items = append(m.items, v)
	return nil
}
func (m *multiFlag) headers() map[string]string {
	h := map[string]string{}
	for _, it := range m.items {
		if i := strings.Index(it, ":"); i > 0 {
			h[strings.TrimSpace(it[:i])] = strings.TrimSpace(it[i+1:])
		}
	}
	return h
}

// small indirections so tests can avoid real OS handles
var (
	stdin      = func() io.Reader { return os.Stdin }
	osReadFile = os.ReadFile
)
