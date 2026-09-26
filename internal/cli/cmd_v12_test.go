// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Tests for the v1.2 completeness batch: journey stats, de delete,
// query delete, work prune.

package cli

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- journey stats: population + activity summary (key → id → summaries) ---

func TestJourneyStatsMergesBothSummaries(t *testing.T) {
	var reqs []string
	var bodies []string
	fakeSFMC(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/interaction/v1/interactions/status/key:jk-1", func(w http.ResponseWriter, r *http.Request) {
			reqs = append(reqs, "GET "+r.URL.Path+"?"+r.URL.RawQuery)
			_, _ = w.Write([]byte(`[{"id":"def-id-9","status":"Published","versionNumber":4}]`))
		})
		mux.HandleFunc("/interaction/v1/interactions/def-id-9/summary", func(w http.ResponseWriter, r *http.Request) {
			reqs = append(reqs, "GET "+r.URL.Path)
			_, _ = w.Write([]byte(`{"id":"def-id-9","activities":[{"type":"EMAILV2","count":2},{"type":"WAIT","count":3}]}`))
		})
		mux.HandleFunc("/interaction/v1/interactions/journeyhistory/summary", func(w http.ResponseWriter, r *http.Request) {
			b := make([]byte, 512)
			n, _ := r.Body.Read(b)
			bodies = append(bodies, string(b[:n]))
			reqs = append(reqs, "POST "+r.URL.Path)
			_, _ = w.Write([]byte(`{"total":7,"waiting":2,"expired":0,"warningCount":0,"errorCount":1,"successCount":4,"totalContactCount":100,"cameOffWait":3,"tags":[]}`))
		})
	})
	code, out, _ := run(t, "journey", "stats", "jk-1")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	// wire: status → activity summary → population query, in order
	if len(reqs) != 3 ||
		!strings.HasPrefix(reqs[0], "GET /interaction/v1/interactions/status/key:jk-1?AllVersions=true") ||
		reqs[1] != "GET /interaction/v1/interactions/def-id-9/summary" ||
		reqs[2] != "POST /interaction/v1/interactions/journeyhistory/summary" {
		t.Fatalf("unexpected call sequence: %v", reqs)
	}
	// wire assertion: the history POST carries the objectId query — it is a
	// read, and this locks it against drifting into a mutation
	if len(bodies) != 1 || bodies[0] != `{"objectId":"def-id-9"}` {
		t.Fatalf("history POST body must be the objectId query: %v", bodies)
	}
	e := envelope(t, out)
	data := e["data"].(map[string]any)
	if data["id"] != "def-id-9" || data["versions"] != float64(1) {
		t.Fatalf("data envelope wrong: %v", data)
	}
	pop := data["population"].(map[string]any)
	if pop["totalContactCount"] != float64(100) || pop["successCount"] != float64(4) {
		t.Fatalf("population counters missing: %v", pop)
	}
	acts := data["activities"].([]any)
	if len(acts) != 2 {
		t.Fatalf("activities not carried: %v", acts)
	}
	if h, _ := e["hint"].(string); !strings.Contains(h, "ALL versions") {
		t.Fatalf("hint must explain the population scope: %q", h)
	}
}

func TestJourneyStatsUnknownKey(t *testing.T) {
	fakeSFMC(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/interaction/v1/interactions/status/key:missing", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`[]`))
		})
	})
	code, out, _ := run(t, "journey", "stats", "missing")
	if code != exitAPI {
		t.Fatalf("expected API exit 1, got %d", code)
	}
	if h, _ := envelope(t, out)["hint"].(string); !strings.Contains(h, "journey list") {
		t.Fatalf("hint must point at journey list: %q", h)
	}
}

// --- de delete: gated destructive with undo image ---

func deDeleteRoutes(t *testing.T, captured *[]string) func(*http.ServeMux) {
	return func(mux *http.ServeMux) {
		mux.HandleFunc("/data/v1/customObjects", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.RawQuery != "" { // resolveDE search
				_, _ = w.Write([]byte(`{"count":1,"items":[{"id":"guid-1","key":"MyDE","name":"MyDE"}]}`))
				return
			}
			t.Errorf("unexpected bare customObjects call")
		})
		mux.HandleFunc("/data/v1/customObjects/guid-1", func(w http.ResponseWriter, r *http.Request) {
			*captured = append(*captured, r.Method)
			switch r.Method {
			case http.MethodGet: // undo image
				_, _ = w.Write([]byte(`{"id":"guid-1","key":"MyDE","name":"MyDE","fields":[]}`))
			case http.MethodDelete:
				w.WriteHeader(http.StatusNoContent)
			default:
				t.Errorf("unexpected method %s", r.Method)
			}
		})
	}
}

func TestDeDeleteRequiresGate(t *testing.T) {
	var captured []string
	fakeSFMC(t, deDeleteRoutes(t, &captured))
	code, out, _ := run(t, "de", "delete", "MyDE")
	if code != exitUsage {
		t.Fatalf("expected usage exit 2, got %d", code)
	}
	if len(captured) != 0 {
		t.Fatalf("ungated delete must not touch the network")
	}
	e := envelope(t, out)
	if msg, _ := e["error"].(map[string]any)["message"].(string); !strings.Contains(msg, "--write --confirm") {
		t.Fatalf("error must name the gate: %v", e["error"])
	}
}

func TestDeDeleteCapturesUndoThenDeletes(t *testing.T) {
	var captured []string
	fakeSFMC(t, deDeleteRoutes(t, &captured))
	code, out, _ := run(t, "de", "delete", "MyDE", "--write", "--confirm")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	// wire: undo GET must happen BEFORE the DELETE
	if len(captured) != 2 || captured[0] != "GET" || captured[1] != "DELETE" {
		t.Fatalf("expected GET(undo) then DELETE, got %v", captured)
	}
	e := envelope(t, out)
	data := e["data"].(map[string]any)
	if data["deleted"] != "MyDE" || data["undo"] != true {
		t.Fatalf("envelope wrong: %v", data)
	}
	if h, _ := e["hint"].(string); !strings.Contains(h, "undo") {
		t.Fatalf("hint must mention the undo image: %q", h)
	}
}

// --- query delete: gated destructive with undo image ---

func TestQueryDeleteCapturesUndoThenDeletes(t *testing.T) {
	var captured []string
	fakeSFMC(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/automation/v1/queries", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"count":1,"items":[{"key":"K1","queryDefinitionId":"qid-9"}]}`))
		})
		mux.HandleFunc("/automation/v1/queries/qid-9", func(w http.ResponseWriter, r *http.Request) {
			captured = append(captured, r.Method)
			switch r.Method {
			case http.MethodGet:
				_, _ = w.Write([]byte(`{"key":"K1","queryText":"SELECT 1"}`))
			case http.MethodDelete:
				w.WriteHeader(http.StatusNoContent)
			}
		})
	})
	code, out, _ := run(t, "query", "delete", "K1")
	if code != exitUsage {
		t.Fatalf("ungated delete must be usage, got %d", code)
	}
	code, out, _ = run(t, "query", "delete", "K1", "--write", "--confirm")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	if len(captured) != 2 || captured[0] != "GET" || captured[1] != "DELETE" {
		t.Fatalf("expected GET(undo) then DELETE, got %v", captured)
	}
	e := envelope(t, out)
	data := e["data"].(map[string]any)
	if data["deleted"] != "K1" || data["queryDefinitionId"] != "qid-9" {
		t.Fatalf("envelope wrong: %v", data)
	}
}

// --- work prune: report by default, --do deletes only stale entries ---

func TestWorkPruneReportThenDo(t *testing.T) {
	// offline: seed a fake MCECLI_HOME work tree directly
	dir := t.TempDir()
	t.Setenv("MCECLI_HOME", dir)
	w := filepath.Join(dir, "work", "P1", "undo")
	if err := os.MkdirAll(filepath.Join(w, "old-stuff"), 0o700); err != nil {
		t.Fatal(err)
	}
	oldFile := filepath.Join(w, "old-stuff", "response.json")
	if err := os.WriteFile(oldFile, []byte(`{"x":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-48 * time.Hour)
	// prune granularity is the leaf entry (the old-stuff DIR) — backdate it
	if err := os.Chtimes(filepath.Join(w, "old-stuff"), past, past); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(oldFile, past, past); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w, "fresh.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(dir, "journal.jsonl")
	if err := os.WriteFile(journalPath, []byte(`{"e":1}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// report: only the stale entry, nothing deleted
	code, out, _ := run(t, "work", "prune", "--older-than", "24h")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	e := envelope(t, out)
	items, _ := e["data"].([]any)
	if len(items) != 1 {
		t.Fatalf("report must list only the stale entry, got %d", len(items))
	}
	row := items[0].(map[string]any)
	if row["profile"] != "P1" || row["category"] != "undo" {
		t.Fatalf("report row wrong: %v", row)
	}
	if _, err := os.Stat(oldFile); err != nil {
		t.Fatal("report must not delete")
	}

	// --do: stale removed, fresh + journal survive
	code, out, _ = run(t, "work", "prune", "--older-than", "24h", "--do")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Fatal("stale entry must be deleted")
	}
	if _, err := os.Stat(filepath.Join(w, "fresh.json")); err != nil {
		t.Fatal("fresh entry must survive")
	}
	if _, err := os.Stat(journalPath); err != nil {
		t.Fatal("journal must NEVER be pruned")
	}
	e = envelope(t, out)
	if e["count"].(float64) != 1 {
		t.Fatalf("do-envelope count wrong: %v", e["count"])
	}
}

func TestWorkPruneBadCutoff(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MCECLI_HOME", dir)
	code, out, _ := run(t, "work", "prune", "--older-than", "banana")
	if code != exitUsage {
		t.Fatalf("expected usage exit 2, got %d", code)
	}
	if h, _ := envelope(t, out)["hint"].(string); !strings.Contains(h, "30d") {
		t.Fatalf("hint must show the age form: %q", h)
	}
}
