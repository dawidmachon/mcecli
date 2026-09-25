// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// healthFakeCSV serves an N-row healthreport CSV (agent-feedback round 3:
// the real endpoint returns the whole report in ONE response — any
// truncation is client-side and must be loud).
func healthFakeCSV(mux *http.ServeMux, rows int) {
	mux.HandleFunc("/automation/v1/automations/healthreport", func(w http.ResponseWriter, r *http.Request) {
		var b strings.Builder
		b.WriteString("AutomationName,30DaySuccessRate,30DayErrorCount\n")
		for i := 0; i < rows; i++ {
			fmt.Fprintf(&b, "auto-%02d,100,0\n", i)
		}
		_, _ = w.Write([]byte(b.String()))
	})
}

func TestAutoHealthDefaultIsFullScan(t *testing.T) {
	fakeSFMC(t, func(mux *http.ServeMux) { healthFakeCSV(mux, 5) })
	// wire assertion: no --limit → every row of the one-shot report comes
	// back; a diagnostic that silently caps would ship false answers
	code, out, _ := run(t, "auto", "health")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	e := envelope(t, out)
	items, _ := e["data"].([]any)
	if len(items) != 5 {
		t.Fatalf("default --limit must return the full report, got %d rows", len(items))
	}
	if e["count"].(float64) != 5 {
		t.Fatalf("count must equal returned rows: %v", e["count"])
	}
	if h, _ := e["hint"].(string); strings.Contains(h, "CAPPED") {
		t.Fatalf("full scan must not be hinted as capped: %q", h)
	}
}

func TestAutoHealthLimitCapsLoudly(t *testing.T) {
	fakeSFMC(t, func(mux *http.ServeMux) { healthFakeCSV(mux, 5) })
	code, out, _ := run(t, "auto", "health", "--limit", "2")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	e := envelope(t, out)
	items, _ := e["data"].([]any)
	if len(items) != 2 {
		t.Fatalf("--limit 2 must return 2 rows, got %d", len(items))
	}
	h, _ := e["hint"].(string)
	if !strings.Contains(h, "CAPPED by --limit 2") || !strings.Contains(h, "contains 5 rows") {
		t.Fatalf("truncation must be loudly hinted, hint=%q", h)
	}
}

func TestAutoHealthCoercesCountersToNumbers(t *testing.T) {
	fakeSFMC(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/automation/v1/automations/healthreport", func(w http.ResponseWriter, r *http.Request) {
			// wire assertion on shape: counters arrive as CSV strings and
			// must leave the tool as JSON numbers
			_, _ = w.Write([]byte("AutomationName,30DaySuccessRate,30DayErrorCount\njob,87.5,3\n"))
		})
	})
	code, out, _ := run(t, "auto", "health")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	e := envelope(t, out)
	items, _ := e["data"].([]any)
	row, _ := items[0].(map[string]any)
	if row["30DayErrorCount"] != float64(3) {
		t.Fatalf("integer counter must be a number, got %T %v", row["30DayErrorCount"], row["30DayErrorCount"])
	}
	if row["30DaySuccessRate"] != 87.5 {
		t.Fatalf("decimal counter must be a number, got %T %v", row["30DaySuccessRate"], row["30DaySuccessRate"])
	}
	if _, ok := row["AutomationName"].(string); !ok {
		t.Fatalf("name must stay a string: %T", row["AutomationName"])
	}
}
