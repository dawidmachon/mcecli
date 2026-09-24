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
		`<Results xsi:type="SentEvent"><SendID>12345</SendID><SubscriberKey>sk-1</SubscriberKey><EventDate>2026-09-10T01:26:37</EventDate><BatchID>790</BatchID></Results>`))

	code, out, errOut := run(t, "dv", "sent", "--since", "7d", "--limit", "5")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s err=%s", code, out, errOut)
	}
	e := envelope(t, out)
	if e["count"] != float64(1) {
		t.Fatalf("count=1 expected: %v", e)
	}
	row := e["data"].([]any)[0].(map[string]any)
	if row["SubscriberKey"] != "sk-1" || row["SendID"] != "12345" {
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
		`<Results xsi:type="Send"><ID>12345</ID><EmailName>automation_events_email</EmailName><FromName>Ops Automation Monitoring</FromName></Results>`))

	code, out, _ := run(t, "dv", "send", "12345")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	if len(bodies) == 0 || !strings.Contains(bodies[0], "<tns:ObjectType>Send</tns:ObjectType>") {
		t.Fatalf("wire wrong: %v", bodies)
	}
	if !strings.Contains(bodies[0], "<tns:Property>ID</tns:Property><tns:SimpleOperator>equals</tns:SimpleOperator><tns:Value>12345</tns:Value>") {
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
	code, out, _ := run(t, "dv", "send", "12345", "--subscriber-key", "x")
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
