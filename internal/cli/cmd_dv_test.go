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

// dvSoapRoute registers a canned SOAP RetrieveResponse on /Service.asmx and
// captures request bodies for wire assertions.
func dvSoapRoute(bodies *[]string, overallStatus, resultsXML string) func(*http.ServeMux) {
	return func(mux *http.ServeMux) {
		mux.HandleFunc("/Service.asmx", func(w http.ResponseWriter, r *http.Request) {
			b := make([]byte, 16384)
			n, _ := r.Body.Read(b)
			*bodies = append(*bodies, string(b[:n]))
			_, _ = w.Write([]byte(fmt.Sprintf(`<soap:Envelope xmlns:soap="http://www.w3.org/2003/05/soap-envelope">
<soap:Body><RetrieveResponseMsg xmlns="http://exacttarget.com/wsdl/partnerAPI">
<OverallStatus>%s</OverallStatus><RequestID>r1</RequestID>%s
</RetrieveResponseMsg></soap:Body></soap:Envelope>`, overallStatus, resultsXML)))
		})
	}
}

func TestDvSentHappyPath(t *testing.T) {
	var bodies []string
	fakeSFMC(t, dvSoapRoute(&bodies, "OK",
		`<Results xsi:type="SentEvent"><SendID>55012</SendID><SubscriberKey>sk-1</SubscriberKey><EventDate>2026-09-10T01:26:37</EventDate><BatchID>790</BatchID></Results>`))

	code, out, errOut := run(t, "dv", "sent", "--since", "7d", "--limit", "5")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s err=%s", code, out, errOut)
	}
	e := envelope(t, out)
	if e["count"] != float64(1) {
		t.Fatalf("count=1 expected: %v", e)
	}
	row := e["data"].([]any)[0].(map[string]any)
	if row["SubscriberKey"] != "sk-1" || row["SendID"] != "55012" {
		t.Fatalf("row not flattened: %v", row)
	}
	if len(bodies) == 0 {
		t.Fatal("no SOAP request captured")
	}
	wire := bodies[0]
	for _, want := range []string{
		"<tns:ObjectType>SentEvent</tns:ObjectType>",
		"<tns:Properties>SubscriberKey</tns:Properties>",
		"<tns:SimpleOperator>greaterThan</tns:SimpleOperator>",
		"<tns:Value>2", // --since 7d resolves to a UTC timestamp
	} {
		if !strings.Contains(wire, want) {
			t.Fatalf("wire missing %q: %s", want, wire)
		}
	}
}

func TestDvWrongFieldTeachesValidSet(t *testing.T) {
	var bodies []string
	fakeSFMC(t, dvSoapRoute(&bodies,
		"Error: The Request Property(s) Bogus do not match with the fields of SentEvent retrieve", ""))

	code, out, _ := run(t, "dv", "sent", "--fields", "Bogus")
	if code != exitAPI {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	e := envelope(t, out)
	if e["ok"] != false {
		t.Fatalf("must fail: %v", e)
	}
	hint, _ := e["hint"].(string)
	if !strings.Contains(hint, "SubscriberKey") || !strings.Contains(hint, "--fields") {
		t.Fatalf("hint must teach the verified property set: %v", e)
	}
}

func TestDvSendPositionalID(t *testing.T) {
	var bodies []string
	fakeSFMC(t, dvSoapRoute(&bodies, "OK",
		`<Results xsi:type="Send"><ID>12345</ID><EmailName>automation_email</EmailName><FromName>Ops Automation Monitoring</FromName></Results>`))

	code, out, _ := run(t, "dv", "send", "55012")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	if len(bodies) == 0 || !strings.Contains(bodies[0], "<tns:ObjectType>Send</tns:ObjectType>") {
		t.Fatalf("wire wrong: %v", bodies)
	}
	if !strings.Contains(bodies[0], "<tns:Property>ID</tns:Property><tns:SimpleOperator>equals</tns:SimpleOperator><tns:Value>55012</tns:Value>") {
		t.Fatalf("positional id must become ID equals filter: %s", bodies[0])
	}
}

func TestDvUnknownObjectRefuses(t *testing.T) {
	var bodies []string
	fakeSFMC(t, dvSoapRoute(&bodies, "OK", ""))
	code, out, _ := run(t, "dv", "jobs")
	if code != exitUsage {
		t.Fatalf("unknown object must exit 2: exit=%d out=%s", code, out)
	}
	e := envelope(t, out)
	if h, _ := e["hint"].(string); !strings.Contains(h, "mcecli dv list") {
		t.Fatalf("refusal must teach dv list: %v", e)
	}
}

func TestDvSendSubscriberKeyRefused(t *testing.T) {
	var bodies []string
	fakeSFMC(t, dvSoapRoute(&bodies, "OK",
		`<Results xsi:type="Send"><ID>12345</ID></Results>`))
	code, out, _ := run(t, "dv", "send", "55012", "--subscriber-key", "x")
	if code != exitUsage {
		t.Fatalf("--subscriber-key on send must refuse: exit=%d out=%s", code, out)
	}
}

func TestDvHelpPrints(t *testing.T) {
	var bodies []string
	fakeSFMC(t, dvSoapRoute(&bodies, "OK", ""))
	if code, out, _ := run(t, "dv", "--help"); code != exitOK || !strings.Contains(out, "mcecli dv") {
		t.Fatalf("dv --help must print usage: code=%d out=%q", code, out)
	}
	if code, _, errOut := run(t, "dv"); code != exitUsage || !strings.Contains(errOut, "mcecli dv") {
		t.Fatalf("bare dv must print usage to stderr: code=%d err=%q", code, errOut)
	}
}

func TestDvExtraPositionalRefused(t *testing.T) {
	var bodies []string
	fakeSFMC(t, dvSoapRoute(&bodies, "OK", ""))
	code, out, _ := run(t, "dv", "sent", "extra")
	if code != exitUsage {
		t.Fatalf("extra positional must refuse: exit=%d out=%s", code, out)
	}
}

// --- dv recipients: per-recipient send status via REST job stats (v1.1) ---
// jobId == SendID from dv sent (verified live: same id namespace).

func dvRecipientsRoute(pages map[string]string, gotPages *[]string, paths *[]string) func(*http.ServeMux) {
	return func(mux *http.ServeMux) {
		mux.HandleFunc("/messaging/v1/jobs/55012/stats/sends", func(w http.ResponseWriter, r *http.Request) {
			if paths != nil {
				*paths = append(*paths, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
			}
			page := r.URL.Query().Get("$page")
			if gotPages != nil {
				*gotPages = append(*gotPages, page)
			}
			body, ok := pages[page]
			if !ok {
				body = pages["1"]
			}
			_, _ = w.Write([]byte(body))
		})
	}
}

const dvRecipientsPage1 = `{"count":3,"page":1,"pageSize":25,"links":{},"items":[` +
	`{"subscriberId":801,"stats":[{"id":11,"transactionTime":"2026-09-01T05:42:07.9","domain":"example.com"},{"id":12,"transactionTime":"2026-09-02T05:42:08.1","domain":"example.com"}]},` +
	`{"subscriberId":802,"stats":[{"id":13,"transactionTime":"2026-09-02T15:00:34.97","domain":"example.org"}]}]}`

const dvRecipientsPage2 = `{"count":3,"page":2,"pageSize":25,"links":{},"items":[` +
	`{"subscriberId":803,"stats":[{"id":14,"transactionTime":"2026-09-03T01:26:37.37","domain":"example.com"}]}]}`

func TestDvRecipientsFlattensAndWalksPages(t *testing.T) {
	var pagesReq, paths []string
	fakeSFMC(t, dvRecipientsRoute(map[string]string{"1": dvRecipientsPage1, "2": dvRecipientsPage2}, &pagesReq, &paths))
	code, out, _ := run(t, "dv", "recipients", "55012")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	// wire assertions: GET on the job-stats path, $page walk hits page 2
	if len(paths) != 2 || paths[0] != "GET /messaging/v1/jobs/55012/stats/sends?%24page=1" ||
		paths[1] != "GET /messaging/v1/jobs/55012/stats/sends?%24page=2" {
		t.Fatalf("unexpected wire: %v", paths)
	}
	e := envelope(t, out)
	// rows are FLATTENED: one per (recipient, send transaction) — 3 recipients
	// carry 4 transactions total; the walk stops when recipients hit the
	// server count of 3, not when rows reach 3
	items, _ := e["data"].([]any)
	if len(items) != 4 || e["count"].(float64) != 4 {
		t.Fatalf("expected 4 flattened rows, got %d (%v)", len(items), e["count"])
	}
	first := items[0].(map[string]any)
	if first["subscriberId"] != float64(801) || first["transactionId"] != float64(11) ||
		first["domain"] != "example.com" || first["transactionTime"] != "2026-09-01T05:42:07.9" {
		t.Fatalf("row not flattened correctly: %v", first)
	}
	// the walk must STOP once the server count is consumed (3 recipients)
	h, _ := e["hint"].(string)
	if !strings.Contains(h, "3 recipient(s) per server count") {
		t.Fatalf("hint must carry the server count: %q", h)
	}
	if strings.Contains(h, "CAPPED") || strings.Contains(h, "SCAN CEILING") {
		t.Fatalf("complete fetch must not claim a cap: %q", h)
	}
}

func TestDvRecipientsRowCapIsLoud(t *testing.T) {
	fakeSFMC(t, dvRecipientsRoute(map[string]string{"1": dvRecipientsPage1, "2": dvRecipientsPage2}, nil, nil))
	code, out, _ := run(t, "dv", "recipients", "55012", "--limit", "2")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	e := envelope(t, out)
	items, _ := e["data"].([]any)
	if len(items) != 2 {
		t.Fatalf("--limit 2 must return 2 rows, got %d", len(items))
	}
	h, _ := e["hint"].(string)
	if !strings.Contains(h, "CAPPED at --limit 2") {
		t.Fatalf("row cap must be loud: %q", h)
	}
}

func TestDvRecipientsScanCeilingIsLoud(t *testing.T) {
	// server claims 3 recipients but the --pages ceiling stops the walk early
	fakeSFMC(t, dvRecipientsRoute(map[string]string{"1": dvRecipientsPage1, "2": dvRecipientsPage2}, nil, nil))
	code, out, _ := run(t, "dv", "recipients", "55012", "--pages", "1", "--limit", "0")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	e := envelope(t, out)
	h, _ := e["hint"].(string)
	if !strings.Contains(h, "SCAN CEILING") || !strings.Contains(h, "2 of 3") {
		t.Fatalf("scan ceiling must be loud: %q", h)
	}
}

func TestDvRecipients404TeachesTheJoin(t *testing.T) {
	fakeSFMC(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/messaging/v1/jobs/999/stats/sends", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
		})
	})
	code, out, _ := run(t, "dv", "recipients", "999")
	if code != exitAPI {
		t.Fatalf("expected API exit 1, got %d", code)
	}
	e := envelope(t, out)
	if h, _ := e["hint"].(string); !strings.Contains(h, "dv send 999") {
		t.Fatalf("404 hint must teach the job check: %q", h)
	}
}

func TestDvRecipientsRejectsNonNumericJobId(t *testing.T) {
	fakeSFMC(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/messaging/v1/jobs/", func(w http.ResponseWriter, r *http.Request) {
			t.Errorf("non-numeric jobId must not reach the network")
		})
	})
	code, out, _ := run(t, "dv", "recipients", "abc")
	if code != exitUsage {
		t.Fatalf("expected usage exit 2, got %d", code)
	}
	e := envelope(t, out)
	if h, _ := e["hint"].(string); !strings.Contains(h, "dv sent --fields SendID") {
		t.Fatalf("hint must teach where job ids come from: %q", h)
	}
}
