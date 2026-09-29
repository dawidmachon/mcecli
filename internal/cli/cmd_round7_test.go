// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"net/http"
	"strings"
	"testing"
)

// Round-7 field-report fixes: rest --query $-preservation, rest --fields
// collection projection (+ --raw conflict), auto detail schedule surfacing,
// folders type-aware hints, explain KB coverage for automation scheduling.

func TestRestQueryPreservesDollarKeys(t *testing.T) {
	// round-7: --query "$filter=..." was stripped to "filter=..." — SFMC
	// OData endpoints ignore $-less params, silently disabling the filter
	var gotQuery []string
	fakeSFMC(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/automation/v1/queries", func(w http.ResponseWriter, r *http.Request) {
			gotQuery = append(gotQuery, r.URL.RawQuery)
			_, _ = w.Write([]byte(`{"count":1,"page":1,"pageSize":25,"items":[{"name":"X","key":"X"}]}`))
		})
	})
	code, out, _ := run(t, "rest", "GET", "automation/v1/queries", "--query", "$filter=name eq 'AGT_E2E_Q'")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	if len(gotQuery) != 1 || !strings.Contains(gotQuery[0], "%24filter=") {
		t.Fatalf("$filter must reach the wire with its $: %q", gotQuery)
	}
	// --page also keeps the $-form
	code, _, _ = run(t, "rest", "GET", "automation/v1/queries", "--page", "2")
	if code != exitOK || len(gotQuery) != 2 || !strings.Contains(gotQuery[1], "%24page=2") {
		t.Fatalf("--page must send $page=2: %q", gotQuery)
	}
}

func TestRestFieldsProjectsCollectionItems(t *testing.T) {
	fakeSFMC(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/automation/v1/queries", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"count":2,"page":1,"pageSize":25,"items":[{"name":"A","key":"a","queryText":"long sql"},{"name":"B","key":"b","queryText":"more sql"}]}`))
		})
	})
	code, out, _ := run(t, "rest", "GET", "automation/v1/queries", "--fields", "name,key")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	e := envelope(t, out)
	data := e["data"].(map[string]any)
	items := data["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("items must survive projection: %v", data)
	}
	row := items[0].(map[string]any)
	if len(row) != 2 || row["name"] != "A" || row["key"] != "a" {
		t.Fatalf("projection must apply to each item: %v", row)
	}
}

func TestRestFieldsRawConflict(t *testing.T) {
	fakeSFMC(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/automation/v1/queries", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"count":0,"items":[]}`))
		})
	})
	code, out, _ := run(t, "rest", "GET", "automation/v1/queries", "--fields", "name", "--raw")
	if code != exitUsage {
		t.Fatalf("--fields + --raw must be a loud usage error, got %d", code)
	}
	if h, _ := envelope(t, out)["hint"].(string); !strings.Contains(h, "--raw") {
		t.Fatalf("hint must name the conflict: %q", h)
	}
}

func TestAutoDetailSurfacesSchedule(t *testing.T) {
	fakeSFMC(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/automation/v1/automations", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"count":1,"items":[{"id":"aid-1","key":"AGT_E2E_AUTO","name":"AGT_E2E_AUTO","queryDefinitionId":"qid-1"}]}`))
		})
		mux.HandleFunc("/automation/v1/automations/aid-1", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"id":"aid-1","key":"AGT_E2E_AUTO","name":"AGT_E2E_AUTO","status":"PausedSchedule","statusId":4,
				"schedule":{"scheduleStatus":"paused","startDate":"2026-10-01T00:00:00Z","iCalRecur":"FREQ=HOURLY;INTERVAL=1;COUNT=2","timezoneId":5,"typeId":1},
				"startSource":{"typeId":1},
				"steps":[{"name":"","objectTypeId":43,"activities":[{"name":"AGT_E2E_Q","objectTypeId":43}]}]}`))
		})
	})
	code, out, _ := run(t, "auto", "AGT_E2E_AUTO")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	e := envelope(t, out)
	data := e["data"].(map[string]any)
	if data["statusId"] != float64(4) {
		t.Fatalf("statusId must surface: %v", data["statusId"])
	}
	sched, ok := data["schedule"].(map[string]any)
	if !ok {
		t.Fatalf("schedule must surface: %v", data)
	}
	if sched["scheduleStatus"] != "paused" || sched["iCalRecur"] != "FREQ=HOURLY;INTERVAL=1;COUNT=2" || sched["timezoneId"] != float64(5) {
		t.Fatalf("schedule summary wrong: %v", sched)
	}
	if h, _ := e["hint"].(string); !strings.Contains(h, "PAUSED") {
		t.Fatalf("hint must explain the paused default: %q", h)
	}
	// objectTypeId 43 must label as query (live-verified REST mapping)
	steps := data["steps"].([]any)[0].(map[string]any)
	if types := steps["types"].([]any); len(types) == 0 || types[0] != "query" {
		t.Fatalf("objectTypeId 43 must label 'query': %v", steps["types"])
	}
}

func TestFoldersTypeAwareHints(t *testing.T) {
	fakeSFMC(t, dvSoapRoute(&[]string{}, "OK",
		`<Results><ID>1</ID><Name>F</Name><ContentType>automations</ContentType></Results>`))
	// automations context → REST-body hint, not de/query --category hint
	code, out, _ := run(t, "folders", "--type", "automations")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	if h, _ := envelope(t, out)["hint"].(string); !strings.Contains(h, "REST automation-create body") {
		t.Fatalf("automations hint wrong: %q", h)
	}
	// singular empty result → plural suggestion
	code, out, _ = run(t, "folders", "--type", "automation")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	e := envelope(t, out)
	if n, _ := e["count"].(float64); n != 0 { // count 0 is omitempty → absent
		t.Fatalf("expected 0 rows for singular type: %v", e["count"])
	}
	if h, _ := e["hint"].(string); !strings.Contains(h, "--type automations") {
		t.Fatalf("hint must suggest the plural form: %q", h)
	}
}

func TestExplainKnowledgeBaseCoversScheduling(t *testing.T) {
	for _, q := range []string{
		"The value specified for the following field is not valid: 'objectTypeId'.",
		"The following field is required: 'startDate'.",
		"JSON Deserialization Exception: Location Unknown",
		"The following field is required: 'Steps'.",
	} {
		code, out, _ := run(t, "explain", q)
		if code != exitOK {
			t.Fatalf("explain(%q) exit=%d out=%s", q, code, out)
		}
		e := envelope(t, out)
		if e["data"] == nil {
			t.Fatalf("explain(%q) must match a KB entry: %v", q, e)
		}
	}
}

// --- round-8: de rows --fields must survive platform-lowercased keys and
// the "(key) " PK marker (agents using schema casing from de get got
// data:[{},{},{}] — silent empty success) ---

func TestDeRowsFieldsCaseInsensitiveWithPKMarker(t *testing.T) {
	fakeSFMC(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/data/v1/customobjectdata/key/DE1/rowset", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"links":{"self":"/v1/customobjectdata/token/T1/rowset"},"requestToken":"T1","count":2,"page":1,"pageSize":2500,"items":[{"keys":{"emailaddress":"a@b.c"},"values":{"fullname":"A One","counter":"1"}},{"keys":{"emailaddress":"x@y.z"},"values":{"fullname":"B Two","counter":"2"}}]}`))
		})
	})
	// schema casing from de get (what agents naturally pass)
	code, out, _ := run(t, "de", "rows", "DE1", "--fields", "EmailAddress,Counter")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	e := envelope(t, out)
	items := e["data"].([]any)
	row := items[0].(map[string]any)
	if len(row) != 2 || row["EmailAddress"] != "a@b.c" || row["Counter"] != "1" {
		t.Fatalf("schema-cased --fields must resolve (output keyed as requested): %v", row)
	}
	// PK-only projection: the (key)-prefixed row key must resolve too
	code, out, _ = run(t, "de", "rows", "DE1", "--fields", "emailaddress")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	items = envelope(t, out)["data"].([]any)
	row = items[1].(map[string]any)
	if len(row) != 1 || row["emailaddress"] != "x@y.z" {
		t.Fatalf("PK field must project despite the (key) marker: %v", row)
	}
}

func TestProjectRowFieldsUnit(t *testing.T) {
	rows := []any{
		map[string]any{"(key) emailaddress": "a@b.c", "fullname": "A", "counter": "1"},
	}
	got := projectRowFields(rows, []string{"EMAILADDRESS", "FullName"})
	row := got.([]any)[0].(map[string]any)
	if row["EMAILADDRESS"] != "a@b.c" || row["FullName"] != "A" {
		t.Fatalf("case-insensitive + PK-strip matching broken: %v", row)
	}
	// non-collection data passes through untouched
	if keep := projectRowFields("scalar", []string{"x"}); keep != "scalar" {
		t.Fatalf("non-collection must pass through: %v", keep)
	}
	// nil fields = no projection
	if same := projectRowFields(rows, nil); len(same.([]any)) != 1 {
		t.Fatalf("nil fields must pass through")
	}
}

// --- round-8: SOAP DE retrieve root cause — wildcard Properties returns 0
// results; explicit columns work. The --where path must resolve columns
// from the fields API and send the Client context block. ---

func TestDeRowsWhereSendsExplicitColumns(t *testing.T) {
	var soapBodies []string
	fakeSFMC(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/data/v1/customObjects", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"count":1,"items":[{"id":"guid-9","key":"DE1","name":"DE1"}]}`))
		})
		mux.HandleFunc("/data/v1/customObjects/guid-9/fields", func(w http.ResponseWriter, r *http.Request) {
			t.Logf("FIELDS HIT %s", r.URL.Path)
			_, _ = w.Write([]byte(`{"fields":[{"name":"EmailAddress","isPrimaryKey":true},{"name":"Counter"}]}`))
		})
		mux.HandleFunc("/Service.asmx", func(w http.ResponseWriter, r *http.Request) {
			b := make([]byte, 8192)
			n, _ := r.Body.Read(b)
			soapBodies = append(soapBodies, string(b[:n]))
			_, _ = w.Write([]byte(`<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/"><soap:Body><RetrieveResponseMsg xmlns="http://exacttarget.com/wsdl/partnerAPI"><OverallStatus>OK</OverallStatus><RequestID>r1</RequestID><Results><EmailAddress>a@b.c</EmailAddress><Counter>1</Counter></Results></RetrieveResponseMsg></soap:Body></soap:Envelope>`))
		})
	})
	code, out, _ := run(t, "de", "rows", "DE1", "--where", "emailaddress=a@b.c")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	if len(soapBodies) != 1 {
		t.Fatalf("expected one SOAP request, got %d", len(soapBodies))
	}
	wire := soapBodies[0]
	// THE root-cause assertion: explicit columns, never the wildcard
	if strings.Contains(wire, "Properties>*</Properties>") {
		t.Fatalf("wildcard Properties must never be sent: %s", wire)
	}
	for _, want := range []string{"<tns:Properties>EmailAddress</tns:Properties>", "<tns:Properties>Counter</tns:Properties>", "<tns:ObjectType>DataExtensionObject[DE1]</tns:ObjectType>"} {
		if !strings.Contains(wire, want) {
			t.Fatalf("wire missing %q: %s", want, wire)
		}
	}
	// Client context: best-effort — the fake token carries no eid claim, so
	// the block must be ABSENT here (soap-level test asserts it with a JWT)
	if strings.Contains(wire, "<tns:Client>") {
		t.Fatalf("token has no eid — Client block must not be fabricated: %s", wire)
	}
	// and the row actually came back
	e := envelope(t, out)
	if e["count"] != float64(1) {
		t.Fatalf("expected 1 row: %v", e["count"])
	}
}
