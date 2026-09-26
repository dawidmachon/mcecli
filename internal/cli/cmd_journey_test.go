// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"net/http"
	"strings"
	"testing"
)

// Journey reads: curated list + version history via the status endpoint.
// Round-5 probe facts locked here: the collection returns ONE item per key
// (newest version), ignores $pageSize (pages at 50) and $filter; the FULL
// version history lives at /status/key:{key}?AllVersions=true (bare call
// 400s with "AllVersions=true or VersionNumber required"; unknown version
// number returns an empty array — data, not an error).

func journeyListRoute(captured *[]string, body string) func(*http.ServeMux) {
	return func(mux *http.ServeMux) {
		mux.HandleFunc("/interaction/v1/interactions", func(w http.ResponseWriter, r *http.Request) {
			*captured = append(*captured, r.URL.RawQuery)
			_, _ = w.Write([]byte(body))
		})
	}
}

func journeyStatusRoute(captured *[]string, body string) func(*http.ServeMux) {
	return func(mux *http.ServeMux) {
		mux.HandleFunc("/interaction/v1/interactions/status/key:jk-1", func(w http.ResponseWriter, r *http.Request) {
			*captured = append(*captured, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
			_, _ = w.Write([]byte(body))
		})
	}
}

const journeyListBody = `{"count":2,"page":1,"pageSize":50,"items":[` +
	`{"id":"def-id-9","definitionId":"def-id-9","key":"jk-1","name":"Welcome Journey","version":4,"status":"Published","channel":"email","executionMode":"Production","lastPublishedDate":"2026-08-01T10:00:00","modifiedDate":"2026-08-01T10:00:00","goals":{},"exits":{}},` +
	`{"id":"def-id-2","definitionId":"def-id-2","key":"jk-2","name":"Abandon Cart","version":1,"status":"Draft","channel":"email","executionMode":"Production","lastPublishedDate":"","modifiedDate":"2026-09-01T09:00:00","goals":{},"exits":{}}]}`

// version 1 first — the command must sort by versionNumber
const journeyVersionsBody = `[{"id":"def-id-9","status":"Published","versionNumber":4},` +
	`{"id":"def-id-9","status":"Stopped","versionNumber":1},` +
	`{"id":"def-id-9","status":"Stopped","versionNumber":3},` +
	`{"id":"def-id-9","status":"Stopped","versionNumber":2}]`

func TestJourneyListCuratedDefault(t *testing.T) {
	var captured []string
	fakeSFMC(t, journeyListRoute(&captured, journeyListBody))
	code, out, _ := run(t, "journey", "list", "--page", "2")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	// wire: plain collection read, $page passthrough
	if len(captured) != 1 || !strings.Contains(captured[0], "%24page=2") {
		t.Fatalf("unexpected wire: %q", captured)
	}
	e := envelope(t, out)
	items, _ := e["data"].([]any)
	if len(items) != 2 || e["count"].(float64) != 2 {
		t.Fatalf("expected 2 journeys, got %d (%v)", len(items), e["count"])
	}
	row := items[0].(map[string]any)
	for _, want := range []string{"key", "name", "version", "status", "channel", "executionMode", "lastPublishedDate"} {
		if _, ok := row[want]; !ok {
			t.Fatalf("curated default must keep %q: %v", want, row)
		}
	}
	for _, gone := range []string{"goals", "exits", "id", "modifiedDate"} {
		if _, ok := row[gone]; ok {
			t.Fatalf("curated default must drop %q: %v", gone, row)
		}
	}
}

func TestJourneyListFullAndFields(t *testing.T) {
	var captured []string
	fakeSFMC(t, journeyListRoute(&captured, journeyListBody))
	code, out, _ := run(t, "journey", "list", "--full")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	row := envelope(t, out)["data"].([]any)[0].(map[string]any)
	if _, ok := row["goals"]; !ok {
		t.Fatalf("--full must keep raw properties: %v", row)
	}

	code, out, _ = run(t, "journey", "list", "--fields", "key,version")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	row = envelope(t, out)["data"].([]any)[0].(map[string]any)
	if len(row) != 2 || row["key"] != "jk-1" || row["version"] != float64(4) {
		t.Fatalf("--fields must override curated default: %v", row)
	}

	code, _, _ = run(t, "journey", "list", "--full", "--fields", "key")
	if code != exitUsage {
		t.Fatalf("--full + --fields must be a usage error, got %d", code)
	}
}

func TestJourneyListSearchIsClientSide(t *testing.T) {
	var captured []string
	fakeSFMC(t, journeyListRoute(&captured, journeyListBody))
	code, out, _ := run(t, "journey", "list", "--search", "cart")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	e := envelope(t, out)
	items, _ := e["data"].([]any)
	if len(items) != 1 || e["count"].(float64) != 1 {
		t.Fatalf("--search must filter client-side to 1, got %d", len(items))
	}
	if h, _ := e["hint"].(string); !strings.Contains(h, "1 of 2") || !strings.Contains(h, "client-side") {
		t.Fatalf("hint must report matched vs total and the client-side fact: %q", h)
	}
}

func TestJourneyListPageSizeTransparency(t *testing.T) {
	var captured []string
	fakeSFMC(t, journeyListRoute(&captured, journeyListBody))
	code, out, _ := run(t, "journey", "list", "--size", "1")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	if h, _ := envelope(t, out)["hint"].(string); !strings.Contains(h, "$pageSize not honored") {
		t.Fatalf("ignored $pageSize must be surfaced: %q", h)
	}
}

func TestJourneyVersionsHappyPath(t *testing.T) {
	var captured []string
	fakeSFMC(t, journeyStatusRoute(&captured, journeyVersionsBody))
	code, out, _ := run(t, "journey", "versions", "jk-1")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	// wire assertions: status endpoint + AllVersions=true (bare call 400s)
	if len(captured) != 1 || captured[0] != "GET /interaction/v1/interactions/status/key:jk-1?AllVersions=true" {
		t.Fatalf("unexpected wire: %v", captured)
	}
	e := envelope(t, out)
	items, _ := e["data"].([]any)
	if len(items) != 4 || e["count"].(float64) != 4 {
		t.Fatalf("expected 4 versions, got %d (%v)", len(items), e["count"])
	}
	if items[0].(map[string]any)["version"] != float64(1) {
		t.Fatalf("versions must be sorted ascending: %v", items[0])
	}
	last := items[3].(map[string]any)
	if last["version"] != float64(4) || last["status"] != "Published" || last["definitionId"] != "def-id-9" {
		t.Fatalf("latest row wrong: %v", last)
	}
	h, _ := e["hint"].(string)
	if !strings.Contains(h, "latest v4 is Published") || !strings.Contains(h, "immutable history") {
		t.Fatalf("hint must summarize the history: %q", h)
	}
}

func TestJourneyVersionsSingleVersion(t *testing.T) {
	var captured []string
	fakeSFMC(t, journeyStatusRoute(&captured, `[{"id":"def-id-9","status":"Stopped","versionNumber":2}]`))
	code, out, _ := run(t, "journey", "versions", "jk-1", "--version", "2")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	if len(captured) != 1 || !strings.Contains(captured[0], "VersionNumber=2") {
		t.Fatalf("--version must map to VersionNumber on the wire: %v", captured)
	}
	e := envelope(t, out)
	items, _ := e["data"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected 1 version, got %d", len(items))
	}
}

func TestJourneyVersionsEmptyIsHinted(t *testing.T) {
	var captured []string
	fakeSFMC(t, journeyStatusRoute(&captured, `[]`))
	code, out, _ := run(t, "journey", "versions", "jk-1")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	e := envelope(t, out)
	if n, _ := e["count"].(float64); n != 0 {
		t.Fatalf("expected 0 versions, got %v", e["count"])
	}
	if h, _ := e["hint"].(string); !strings.Contains(h, "no versions") {
		t.Fatalf("empty result must be hinted (unknown key/version): %q", h)
	}
	// --version N empty → version-specific hint
	code, out, _ = run(t, "journey", "versions", "jk-1", "--version", "9")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	if h, _ := envelope(t, out)["hint"].(string); !strings.Contains(h, "no version 9") {
		t.Fatalf("unknown version must be hinted: %q", h)
	}
}

func TestJourneyVersionsUsageGuards(t *testing.T) {
	fakeSFMC(t, journeyStatusRoute(nil, journeyVersionsBody))
	code, _, _ := run(t, "journey", "versions")
	if code != exitUsage {
		t.Fatalf("missing key must be usage, got %d", code)
	}
	code, _, _ = run(t, "journey", "versions", "jk-1", "--version", "-1")
	if code != exitUsage {
		t.Fatalf("negative --version must be usage, got %d", code)
	}
}

func TestJourneyVersionsErrorPassthrough(t *testing.T) {
	// platform errors must flow through as API failures with the real message
	fakeSFMC(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/interaction/v1/interactions/status/key:jk-1", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"message":"AllVersions=true or VersionNumber required."}`))
		})
	})
	code, out, _ := run(t, "journey", "versions", "jk-1", "--version", "0")
	if code != exitAPI {
		t.Fatalf("expected API exit 1, got %d out=%s", code, out)
	}
	e := envelope(t, out)
	if msg, _ := e["error"].(map[string]any)["message"].(string); !strings.Contains(msg, "AllVersions=true") {
		t.Fatalf("platform error message must pass through: %v", e["error"])
	}
}

func TestJourneyListSizeTransparencyAfterSearch(t *testing.T) {
	// search filters client-side to 1 row, but the SERVER ignored --size 1
	// and returned 2 — the hint must report the server's row count
	var captured []string
	fakeSFMC(t, journeyListRoute(&captured, journeyListBody))
	code, out, _ := run(t, "journey", "list", "--search", "welcome", "--size", "1")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	h, _ := envelope(t, out)["hint"].(string)
	if !strings.Contains(h, "server returned 2 rows despite --size 1") {
		t.Fatalf("transparency must use the server row count: %q", h)
	}
	if !strings.Contains(h, "1 of 2") {
		t.Fatalf("search fact must survive: %q", h)
	}
}
