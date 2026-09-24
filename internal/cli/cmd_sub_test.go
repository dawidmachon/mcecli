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

// subSoapRoute serves canned Subscriber + ListSubscriber retrieves and
// captures request bodies (wire assertions on the filter property choice).
func subSoapRoute(t *testing.T, bodies *[]string) func(*http.ServeMux) {
	return func(mux *http.ServeMux) {
		mux.HandleFunc("/Service.asmx", func(w http.ResponseWriter, r *http.Request) {
			b := make([]byte, 16384)
			n, _ := r.Body.Read(b)
			body := string(b[:n])
			*bodies = append(*bodies, body)
			var results string
			switch {
			case strings.Contains(body, "<tns:ObjectType>Subscriber</tns:ObjectType>"):
				results = `<Results xsi:type="Subscriber"><SubscriberKey>sk-9</SubscriberKey><EmailAddress>x@example.com</EmailAddress><Status>3</Status><UnsubscribedDate>2026-09-01T00:00:00</UnsubscribedDate></Results>`
			case strings.Contains(body, "<tns:ObjectType>ListSubscriber</tns:ObjectType>"):
				// wire assertion: list join must filter by the RESOLVED key
				if !strings.Contains(body, "<tns:Value>sk-9</tns:Value>") {
					t.Errorf("ListSubscriber must filter by resolved SubscriberKey sk-9: %s", body)
				}
				results = `<Results xsi:type="ListSubscriber"><ListID>503</ListID><SubscriberKey>sk-9</SubscriberKey><Status>Unsubscribed</Status></Results>` +
					`<Results xsi:type="ListSubscriber"><ListID>566</ListID><SubscriberKey>sk-9</SubscriberKey><Status>Active</Status></Results>`
			}
			_, _ = w.Write([]byte(fmt.Sprintf(`<soap:Envelope xmlns:soap="http://www.w3.org/2003/05/soap-envelope">
<soap:Body><RetrieveResponseMsg xmlns="http://exacttarget.com/wsdl/partnerAPI">
<OverallStatus>OK</OverallStatus><RequestID>r1</RequestID>%s
</RetrieveResponseMsg></soap:Body></soap:Envelope>`, results)))
		})
	}
}

func TestSubEmailLookupJoinsListsByResolvedKey(t *testing.T) {
	var bodies []string
	fakeSFMC(t, subSoapRoute(t, &bodies))
	code, out, errOut := run(t, "sub", "x@example.com")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s err=%s", code, out, errOut)
	}
	e := envelope(t, out)
	d := e["data"].(map[string]any)
	sub := d["subscriber"].(map[string]any)
	if sub["Status"] != "3" {
		t.Fatalf("subscriber row wrong: %v", sub)
	}
	lists := d["lists"].([]any)
	if len(lists) != 2 {
		t.Fatalf("2 memberships expected: %v", lists)
	}
	if e["count"] != float64(2) {
		t.Fatalf("count must be list count: %v", e)
	}
	// email input must filter Subscriber object by EmailAddress...
	if !strings.Contains(bodies[0], "<tns:Property>EmailAddress</tns:Property>") {
		t.Fatalf("email lookup must filter EmailAddress: %s", bodies[0])
	}
	// ...and decode the status enum in the hint
	if !strings.Contains(e["hint"].(string), "Unsubscribed") {
		t.Fatalf("hint must decode status enum: %v", e)
	}
}

func TestSubKeyLookupFiltersBySubscriberKey(t *testing.T) {
	var bodies []string
	fakeSFMC(t, subSoapRoute(t, &bodies))
	code, _, _ := run(t, "sub", "sk-9", "--no-lists")
	if code != exitOK {
		t.Fatalf("exit code wrong")
	}
	if !strings.Contains(bodies[0], "<tns:Property>SubscriberKey</tns:Property><tns:SimpleOperator>equals</tns:SimpleOperator><tns:Value>sk-9</tns:Value>") {
		t.Fatalf("key lookup must filter SubscriberKey: %s", bodies[0])
	}
	// --no-lists: exactly one SOAP call (no ListSubscriber join)
	if len(bodies) != 1 {
		t.Fatalf("--no-lists must skip the join call, got %d calls", len(bodies))
	}
}

func TestSubNotFound(t *testing.T) {
	fakeSFMC(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/Service.asmx", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`<soap:Envelope xmlns:soap="http://www.w3.org/2003/05/soap-envelope">
<soap:Body><RetrieveResponseMsg xmlns="http://exacttarget.com/wsdl/partnerAPI">
<OverallStatus>OK</OverallStatus><RequestID>r1</RequestID>
</RetrieveResponseMsg></soap:Body></soap:Envelope>`))
		})
	})
	code, out, _ := run(t, "sub", "nobody@example.com")
	if code != exitAPI {
		t.Fatalf("not found must exit 1: exit=%d out=%s", code, out)
	}
	e := envelope(t, out)
	if e["status"] != float64(404) {
		t.Fatalf("not-found must surface 404 status: %v", e)
	}
}

func TestSubAmbiguousPrintsEnvelope(t *testing.T) {
	// two rows match → must PRINT the envelope (never silent-exit) + exitUsage
	fakeSFMC(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/Service.asmx", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`<soap:Envelope xmlns:soap="http://www.w3.org/2003/05/soap-envelope">
<soap:Body><RetrieveResponseMsg xmlns="http://exacttarget.com/wsdl/partnerAPI">
<OverallStatus>OK</OverallStatus><RequestID>r1</RequestID>
<Results xsi:type="Subscriber"><SubscriberKey>sk-a</SubscriberKey><Status>1</Status></Results>
<Results xsi:type="Subscriber"><SubscriberKey>sk-b</SubscriberKey><Status>3</Status></Results>
</RetrieveResponseMsg></soap:Body></soap:Envelope>`))
		})
	})
	code, out, _ := run(t, "sub", "dup@example.com")
	if code != exitOK {
		t.Fatalf("ambiguous must exit 0 with matches surfaced: exit=%d out=%s", code, out)
	}
	if out == "" {
		t.Fatalf("ambiguous must PRINT the envelope (silent exit bug shipped once)")
	}
	if !strings.Contains(out, "AMBIGUOUS") {
		t.Fatalf("hint must flag ambiguity: %s", out)
	}
	e := envelope(t, out)
	d := e["data"].(map[string]any)
	if _, ok := d["ambiguous_matches"]; !ok {
		t.Fatalf("ambiguous_matches expected: %v", e)
	}
}
