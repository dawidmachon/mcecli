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

// tsSoapRoute serves TSD/List/ListSubscriber retrieves with wire capture.
func tsSoapRoute(t *testing.T, bodies *[]string) func(*http.ServeMux) {
	return func(mux *http.ServeMux) {
		mux.HandleFunc("/Service.asmx", func(w http.ResponseWriter, r *http.Request) {
			b := make([]byte, 16384)
			n, _ := r.Body.Read(b)
			body := string(b[:n])
			*bodies = append(*bodies, body)
			var results string
			switch {
			case strings.Contains(body, "TriggeredSendDefinition"):
				results = `<Results xsi:type="TriggeredSendDefinition"><CustomerKey>TS_A</CustomerKey><Name>TS Alpha</Name><TriggeredSendStatus>Canceled</TriggeredSendStatus></Results>` +
					`<Results xsi:type="TriggeredSendDefinition"><CustomerKey>TS_B</CustomerKey><Name>TS Beta</Name><TriggeredSendStatus>Active</TriggeredSendStatus></Results>`
			case strings.Contains(body, "<tns:ObjectType>List<"):
				results = `<Results xsi:type="List"><ID>19</ID><ListName>All Subscribers</ListName><Type>Private</Type></Results>`
			case strings.Contains(body, "ListSubscriber"):
				if !strings.Contains(body, "<tns:Value>19</tns:Value>") {
					t.Errorf("members must filter ListID equals 19: %s", body)
				}
				results = `<Results xsi:type="ListSubscriber"><ListID>19</ListID><SubscriberKey>sk-1</SubscriberKey><Status>Active</Status></Results>`
			}
			_, _ = w.Write([]byte(fmt.Sprintf(`<soap:Envelope xmlns:soap="http://www.w3.org/2003/05/soap-envelope">
<soap:Body><RetrieveResponseMsg xmlns="http://exacttarget.com/wsdl/partnerAPI">
<OverallStatus>OK</OverallStatus><RequestID>r1</RequestID>%s
</RetrieveResponseMsg></soap:Body></soap:Envelope>`, results)))
		})
	}
}

func TestTSListSearchAndStatus(t *testing.T) {
	var bodies []string
	fakeSFMC(t, tsSoapRoute(t, &bodies))
	code, out, _ := run(t, "ts", "list", "--search", "alpha")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	e := envelope(t, out)
	if e["count"] != float64(1) {
		t.Fatalf("search filter failed: %v", e)
	}
	code, out, _ = run(t, "ts", "list", "--status", "Active")
	if code != exitOK {
		t.Fatalf("exit=%d", code)
	}
	e = envelope(t, out)
	if e["count"] != float64(1) {
		t.Fatalf("status filter failed: %v", e)
	}
}

func TestTSGetByKey(t *testing.T) {
	var bodies []string
	fakeSFMC(t, tsSoapRoute(t, &bodies))
	code, out, _ := run(t, "ts", "get", "TS_A")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	// ts get matches CLIENT-side: server-side CustomerKey filter returns 0
	// rows on the reference org even for existing keys (verified live 2026-09-17)
	wire := bodies[len(bodies)-1]
	if strings.Contains(wire, "<tns:SimpleOperator>equals</tns:SimpleOperator><tns:Value>TS_A</tns:Value>") {
		t.Fatalf("get must NOT use a server-side CustomerKey filter (broken on tenant): %s", wire)
	}
	e := envelope(t, out)
	d := e["data"].(map[string]any)
	if d["CustomerKey"] != "TS_A" {
		t.Fatalf("client-side match failed: %v", d)
	}
}

func TestListsMembersFiltersListID(t *testing.T) {
	var bodies []string
	fakeSFMC(t, tsSoapRoute(t, &bodies))
	code, out, _ := run(t, "lists", "members", "19", "--limit", "10")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	e := envelope(t, out)
	if e["count"] != float64(1) {
		t.Fatalf("member row expected: %v", e)
	}
	if !strings.Contains(bodies[len(bodies)-1], "<tns:ObjectType>ListSubscriber</tns:ObjectType>") {
		t.Fatalf("wrong object: %s", bodies[len(bodies)-1])
	}
}

func TestListsAll(t *testing.T) {
	var bodies []string
	fakeSFMC(t, tsSoapRoute(t, &bodies))
	code, out, _ := run(t, "lists")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	e := envelope(t, out)
	d := e["data"].([]any)[0].(map[string]any)
	if d["ListName"] != "All Subscribers" {
		t.Fatalf("list row wrong: %v", d)
	}
}

func TestSessionUseStdoutIsExportLineOnly(t *testing.T) {
	fakeSFMC(t, func(mux *http.ServeMux) {})
	code, out, _ := run(t, "session", "use", "agent-x")
	if code != exitOK {
		t.Fatalf("exit=%d", code)
	}
	if !strings.HasPrefix(out, "export MCECLI_SESSION=agent-x\n") || strings.Contains(out, "{") {
		t.Fatalf("stdout must be the bare export line (source-safe): %q", out)
	}
}
