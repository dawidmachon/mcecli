// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package journal is the append-only audit trail for gated writes.
//
// Every write that passes the tier gates is recorded as one JSON line in
// ~/.mcecli/journal.jsonl: timestamp, profile, BU, MID, method, URL, response
// status, body hash/size, and the async request id when present.
//
// Privacy: request BODIES are intentionally NOT stored (rows can contain
// personal data). The URL + body hash identify the operation; the envelope
// the agent received holds the details.
package journal

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dawidmachon/mcecli/internal/config"
)

// Entry is one audited write operation.
type Entry struct {
	TS          string `json:"ts"`                    // RFC3339 UTC
	Profile     string `json:"profile"`               // profile name
	BU          string `json:"bu,omitempty"`          // BU name ("account" when none)
	MID         string `json:"mid,omitempty"`         // account id the token was scoped to
	Method      string `json:"method"`                // HTTP method
	URL         string `json:"url"`                   // full request URL (no secrets; auth is a header)
	Status      int    `json:"status"`                // HTTP response status (0 = transport error)
	BodySHA256  string `json:"body_sha256,omitempty"` // sha256 of the request body (first 16 hex)
	BodyLen     int    `json:"body_len,omitempty"`    // request body bytes
	RequestID   string `json:"request_id,omitempty"`  // async request id when the API returned one
	Snapshot    string `json:"snapshot,omitempty"`    // captured(n) / skipped(--no-snapshot) / n/a
	UndoPath    string `json:"undo_path,omitempty"`   // before-image dir for DELETE ops
	DurationMS  int64  `json:"duration_ms,omitempty"`
	ServerError string `json:"server_error,omitempty"` // short server error text on failures
}

// Path returns the journal file location.
func Path() string { return filepath.Join(config.Dir(), "journal.jsonl") }

// HashBody returns the first 16 hex chars of the body's sha256 ("" if empty).
func HashBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])[:16]
}

// Append writes one entry as a JSON line. Best-effort by design: a journal
// write failure must never break the operation itself, but the caller is
// expected to surface the returned error as a warning.
func Append(e Entry) error {
	if e.TS == "" {
		e.TS = time.Now().UTC().Format(time.RFC3339)
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(config.Dir(), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(Path(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(b, '\n')); err != nil {
		return err
	}
	return f.Sync()
}

// Last returns up to n most recent entries (newest first), optionally
// filtered by profile name (case-insensitive, empty = all).
func Last(n int, profile string) ([]Entry, error) {
	f, err := os.Open(Path())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var out []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e Entry
		if json.Unmarshal([]byte(line), &e) != nil {
			continue // skip corrupt lines, keep going
		}
		if profile != "" && !strings.EqualFold(e.Profile, profile) {
			continue
		}
		out = append(out, e)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	// newest first
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	if len(out) > n {
		out = out[:n]
	}
	return out, nil
}

// Count returns the total number of entries (0 when the journal is absent).
func Count() int {
	f, err := os.Open(Path())
	if err != nil {
		return 0
	}
	defer f.Close()
	n := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) != "" {
			n++
		}
	}
	return n
}
