// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"net/http"
	"strings"
	"testing"
)

// Agent-feedback round 3: $search is REQUIRED even when categoryId narrows
// results (platform 400s on categoryId alone — it is an AND filter, not an
// alternative). The client must fail fast with the TRUE contract instead of
// forwarding a doomed request.

func TestDeListWithoutSearchFailsFastEvenWithCategory(t *testing.T) {
	fakeSFMC(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/data/v1/customObjects", func(w http.ResponseWriter, r *http.Request) {
			t.Errorf("request must not reach the platform: categoryId cannot stand alone")
		})
	})
	code, out, _ := run(t, "de", "list", "--category", "123")
	if code != exitUsage {
		t.Fatalf("expected usage exit 2, got %d out=%s", code, out)
	}
	e := envelope(t, out)
	h, _ := e["hint"].(string)
	if !strings.Contains(h, "AND filter") || !strings.Contains(h, "--search") {
		t.Fatalf("hint must state the true contract ($search required, --category is AND): %q", h)
	}
}

func TestDeListBareHintStatesTrueContract(t *testing.T) {
	fakeSFMC(t, nil)
	code, out, _ := run(t, "de", "list")
	if code != exitUsage {
		t.Fatalf("expected usage exit 2, got %d out=%s", code, out)
	}
	e := envelope(t, out)
	if h, _ := e["hint"].(string); strings.Contains(h, "or --category") {
		t.Fatalf("hint must not offer --category as an OR-alternative: %q", h)
	}
}

// --- de list --all: SOAP full-DE enumeration (agent-feedback round 3) ---

func deListAllResults() string {
	// deliberately unsorted — de list --all must return a deterministic
	// name-sorted inventory
	return `<Results><Name>Gamma</Name><CustomerKey>GK</CustomerKey><CategoryID>8003</CategoryID><CreatedDate>2020-01-03T00:00:00.000</CreatedDate><IsSendable>false</IsSendable></Results>` +
		`<Results><Name>alpha</Name><CustomerKey>AK</CustomerKey><CategoryID>8001</CategoryID><CreatedDate>2020-01-01T00:00:00.000</CreatedDate><IsSendable>true</IsSendable></Results>` +
		`<Results><Name>Beta</Name><CustomerKey>BK</CustomerKey><CategoryID>8002</CategoryID><CreatedDate>2020-01-02T00:00:00.000</CreatedDate><IsSendable>false</IsSendable></Results>`
}

func TestDeListAllEnumeratesViaSOAP(t *testing.T) {
	var bodies []string
	fakeSFMC(t, dvSoapRoute(&bodies, "OK", deListAllResults()))
	code, out, _ := run(t, "de", "list", "--all")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	// wire assertions: DataExtension retrieve, full property list,
	// enterprise-wide scoping must NOT be requested from a BU context
	if len(bodies) == 0 {
		t.Fatal("no SOAP request captured")
	}
	wire := bodies[0]
	for _, want := range []string{
		"<tns:ObjectType>DataExtension</tns:ObjectType>",
		"<tns:Properties>Name</tns:Properties>",
		"<tns:Properties>CustomerKey</tns:Properties>",
		"<tns:Properties>CategoryID</tns:Properties>",
	} {
		if !strings.Contains(wire, want) {
			t.Errorf("wire missing %s: %s", want, wire)
		}
	}
	if strings.Contains(wire, "QueryAllAccounts") {
		t.Errorf("--all must scope to the current BU, not QueryAllAccounts: %s", wire)
	}
	e := envelope(t, out)
	items, _ := e["data"].([]any)
	if len(items) != 3 || e["count"].(float64) != 3 {
		t.Fatalf("expected 3 rows, got %d (%v)", len(items), e["count"])
	}
	first := items[0].(map[string]any)
	if first["name"] != "alpha" {
		t.Fatalf("inventory must be name-sorted, first=%v", first["name"])
	}
	if first["isSendable"] != true {
		t.Fatalf("isSendable must be a JSON bool, got %T", first["isSendable"])
	}
	if first["categoryId"] != "8001" {
		t.Fatalf("categoryId must be surfaced: %v", first["categoryId"])
	}
	if h, _ := e["hint"].(string); !strings.Contains(h, "full enumeration via SOAP") {
		t.Fatalf("hint must mark the enumeration mode: %q", h)
	}
}

func TestDeListAllCategoryFiltersServerSide(t *testing.T) {
	var bodies []string
	fakeSFMC(t, dvSoapRoute(&bodies, "OK", deListAllResults()))
	if code, out, _ := run(t, "de", "list", "--all", "--category", "8002"); code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	// wire assertion: --category narrows ON PLATFORM (SOAP filter), so the
	// inventory is small before it ever reaches the client
	wire := bodies[0]
	if !strings.Contains(wire, "<tns:Property>CategoryID</tns:Property>") ||
		!strings.Contains(wire, "<tns:SimpleOperator>equals</tns:SimpleOperator>") ||
		!strings.Contains(wire, "<tns:Value>8002</tns:Value>") {
		t.Fatalf("--category must be a server-side SOAP filter: %s", wire)
	}
}

func TestDeListAllSearchFiltersClientSide(t *testing.T) {
	var bodies []string
	fakeSFMC(t, dvSoapRoute(&bodies, "OK", deListAllResults()))
	code, out, _ := run(t, "de", "list", "--all", "--search", "alp")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	e := envelope(t, out)
	items, _ := e["data"].([]any)
	if len(items) != 1 || e["count"].(float64) != 1 {
		t.Fatalf("--search must filter client-side to 1 row, got %d", len(items))
	}
	if h, _ := e["hint"].(string); !strings.Contains(h, "1 of 3") {
		t.Fatalf("hint must report matched vs total: %q", h)
	}
}

func TestDeListAllLimitCapsLoudly(t *testing.T) {
	var bodies []string
	fakeSFMC(t, dvSoapRoute(&bodies, "OK", deListAllResults()))
	code, out, _ := run(t, "de", "list", "--all", "--limit", "2")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	e := envelope(t, out)
	items, _ := e["data"].([]any)
	if len(items) != 2 || e["count"].(float64) != 2 {
		t.Fatalf("--limit 2 must return 2 rows, got %d", len(items))
	}
	h, _ := e["hint"].(string)
	if !strings.Contains(h, "CAPPED by --limit 2") || !strings.Contains(h, "3 DEs") {
		t.Fatalf("truncation must be loudly hinted, hint=%q", h)
	}
}

func TestDeListAllFlagGuards(t *testing.T) {
	// no silently-ignored flags: --limit without --all, and paging with
	// --all, are usage errors — and must not reach the network
	fakeSFMC(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/Service.asmx", func(w http.ResponseWriter, r *http.Request) {
			t.Errorf("SOAP must not be called for a usage error")
		})
	})
	for _, args := range [][]string{
		{"de", "list", "--limit", "5"},
		{"de", "list", "--all", "--page", "2"},
		{"de", "list", "--all", "--size", "10"},
		{"de", "list", "--all", "--limit", "-1"},
	} {
		code, out, _ := run(t, args...)
		if code != exitUsage {
			e := envelope(t, out)
			t.Errorf("%v: expected usage exit 2, got %d (hint=%v)", args, code, e["hint"])
		}
	}
}

// --- de list REST path: lean default projection (roadmap v1.1) ---
// Default output is curated to name/key/rowCount; --full returns raw
// objects; --fields picks columns. Default-behavior change → wire assertion
// guarantees the REQUEST is unchanged ($search still reaches the platform).

func deListRESTRoute(t *testing.T, captured *[]string) func(*http.ServeMux) {
	return func(mux *http.ServeMux) {
		mux.HandleFunc("/data/v1/customObjects", func(w http.ResponseWriter, r *http.Request) {
			*captured = append(*captured, r.URL.RawQuery)
			_, _ = w.Write([]byte(`{"count":7,"page":1,"pageSize":25,"items":[{` +
				`"name":"MyDE","key":"MyDE_Key","rowCount":42,"categoryId":8001,` +
				`"createdDate":"2026-01-01T00:00:00","description":"d","isActive":true,` +
				`"dataRetentionProperties":{"rule":"delete"}}]}`))
		})
	}
}

func TestDeListDefaultOutputIsLean(t *testing.T) {
	var captured []string
	fakeSFMC(t, deListRESTRoute(t, &captured))
	code, out, _ := run(t, "de", "list", "--search", "my")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	// wire assertion: curation is output-only — $search still reaches the wire
	if len(captured) != 1 || !strings.Contains(captured[0], "%24search=my") {
		t.Fatalf("$search must still be sent: %q", captured)
	}
	e := envelope(t, out)
	items, _ := e["data"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected 1 row, got %d", len(items))
	}
	row := items[0].(map[string]any)
	if len(row) != 3 || row["name"] != "MyDE" || row["key"] != "MyDE_Key" || row["rowCount"] != float64(42) {
		t.Fatalf("lean default must be exactly name/key/rowCount, got: %v", row)
	}
	if h, _ := e["hint"].(string); !strings.Contains(h, "lean default projection") || !strings.Contains(h, "--full") {
		t.Fatalf("hint must offer the escape hatches: %q", h)
	}
}

func TestDeListFullReturnsRawObjects(t *testing.T) {
	var captured []string
	fakeSFMC(t, deListRESTRoute(t, &captured))
	code, out, _ := run(t, "de", "list", "--search", "my", "--full")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	e := envelope(t, out)
	row := e["data"].([]any)[0].(map[string]any)
	for _, k := range []string{"name", "key", "rowCount", "categoryId", "createdDate", "description", "isActive", "dataRetentionProperties"} {
		if _, ok := row[k]; !ok {
			t.Fatalf("--full must keep %q", k)
		}
	}
}

func TestDeListFieldsOverridesLean(t *testing.T) {
	var captured []string
	fakeSFMC(t, deListRESTRoute(t, &captured))
	code, out, _ := run(t, "de", "list", "--search", "my", "--fields", "key,createdDate")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	e := envelope(t, out)
	row := e["data"].([]any)[0].(map[string]any)
	if len(row) != 2 || row["key"] != "MyDE_Key" || row["createdDate"] != "2026-01-01T00:00:00" {
		t.Fatalf("--fields must override the lean default: %v", row)
	}
}

func TestDeListFullAndFieldsAreExclusive(t *testing.T) {
	var captured []string
	fakeSFMC(t, deListRESTRoute(t, &captured))
	code, out, _ := run(t, "de", "list", "--search", "my", "--full", "--fields", "key")
	if code != exitUsage {
		t.Fatalf("expected usage exit 2, got %d", code)
	}
	e := envelope(t, out)
	if msg, _ := e["error"].(map[string]any)["message"].(string); !strings.Contains(msg, "mutually exclusive") {
		t.Fatalf("error must name the conflict: %v", e["error"])
	}
	if len(captured) != 0 {
		t.Fatalf("conflicting flags must not reach the network")
	}
}

func TestDeListAllRejectsFull(t *testing.T) {
	var bodies []string
	fakeSFMC(t, dvSoapRoute(&bodies, "OK", deListAllResults()))
	code, out, _ := run(t, "de", "list", "--all", "--full")
	if code != exitUsage {
		t.Fatalf("--full is meaningless with --all: expected usage exit 2, got %d", code)
	}
	if len(bodies) != 0 {
		t.Fatalf("usage error must not reach the network")
	}
	if h, _ := envelope(t, out)["hint"].(string); !strings.Contains(h, "--all") {
		t.Fatalf("hint must explain the conflict: %q", h)
	}
}
