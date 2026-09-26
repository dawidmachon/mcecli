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

// Generic SOAP reads (v1.1 passthrough redesign). Safety contract locked
// here: Retrieve is the ONLY verb the command can build (structured flags,
// no body passthrough), --props is required, equals-only filters, loud caps,
// platform teaching errors pointed at describe.

func soapWire(bodies *[]string, overallStatus, resultsXML string) func(*http.ServeMux) {
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

func TestSoapRetrieveHappyPath(t *testing.T) {
	var bodies []string
	fakeSFMC(t, soapWire(&bodies, "OK",
		`<Results><ListName>News</ListName><ID>42</ID><Type>Public</Type></Results>`))
	code, out, _ := run(t, "soap", "retrieve", "List",
		"--props", "ID,ListName,Type", "--filter", "ListName=News")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	if len(bodies) != 1 {
		t.Fatalf("expected exactly one SOAP request, got %d", len(bodies))
	}
	wire := bodies[0]
	for _, want := range []string{
		"<tns:ObjectType>List</tns:ObjectType>",
		"<tns:Properties>ID</tns:Properties>",
		"<tns:Properties>ListName</tns:Properties>",
		"<tns:SimpleOperator>equals</tns:SimpleOperator>",
		"<tns:Property>ListName</tns:Property>",
		"<tns:Value>News</tns:Value>",
	} {
		if !strings.Contains(wire, want) {
			t.Errorf("wire missing %s: %s", want, wire)
		}
	}
	// RetrieveRequest is the only request shape this command can build —
	// a pass-through would show up as arbitrary XML with no RetrieveRequest
	if !strings.Contains(wire, "RetrieveRequest") || strings.Contains(wire, "UpdateRequest") ||
		strings.Contains(wire, "DeleteRequest") || strings.Contains(wire, "PerformRequestMsg") ||
		strings.Contains(wire, "ExecuteRequestMsg") {
		t.Fatalf("wire must be a plain RetrieveRequest: %s", wire)
	}
	e := envelope(t, out)
	items, _ := e["data"].([]any)
	if len(items) != 1 || e["count"].(float64) != 1 {
		t.Fatalf("expected 1 row, got %d (%v)", len(items), e["count"])
	}
	row := items[0].(map[string]any)
	if row["ListName"] != "News" || row["ID"] != "42" {
		t.Fatalf("row wrong: %v", row)
	}
	if h, _ := e["hint"].(string); !strings.Contains(h, "read-only") {
		t.Fatalf("hint must mark the read-only mode: %q", h)
	}
}

func TestSoapRetrieveRequiresProps(t *testing.T) {
	var bodies []string
	fakeSFMC(t, soapWire(&bodies, "OK", ""))
	code, out, _ := run(t, "soap", "retrieve", "List")
	if code != exitUsage {
		t.Fatalf("--props is required: expected usage exit 2, got %d", code)
	}
	if len(bodies) != 0 {
		t.Fatalf("usage error must not reach the network")
	}
	if h, _ := envelope(t, out)["hint"].(string); !strings.Contains(h, "describe") {
		t.Fatalf("hint must point at the describe catalog: %q", h)
	}
}

func TestSoapRetrieveMalformedFilter(t *testing.T) {
	var bodies []string
	fakeSFMC(t, soapWire(&bodies, "OK", ""))
	code, out, _ := run(t, "soap", "retrieve", "List", "--props", "ID", "--filter", "NoEqualsSign")
	if code != exitUsage {
		t.Fatalf("expected usage exit 2, got %d", code)
	}
	if len(bodies) != 0 {
		t.Fatalf("malformed filter must not reach the network")
	}
	if h, _ := envelope(t, out)["hint"].(string); !strings.Contains(h, "Prop=Value") {
		t.Fatalf("hint must show the filter form: %q", h)
	}
}

func TestSoapRetrievePropMismatchTeaches(t *testing.T) {
	var bodies []string
	fakeSFMC(t, soapWire(&bodies, "Error: The Request Property(s) Bogus do not match with the fields of List retrieve", ""))
	code, out, _ := run(t, "soap", "retrieve", "List", "--props", "Bogus")
	if code != exitAPI {
		t.Fatalf("expected API exit 1, got %d", code)
	}
	e := envelope(t, out)
	if h, _ := e["hint"].(string); !strings.Contains(h, "describe List") {
		t.Fatalf("hint must point at describe for the verified set: %q", h)
	}
}

func TestSoapRetrieveStatusErrorFails(t *testing.T) {
	var bodies []string
	fakeSFMC(t, soapWire(&bodies, "Error: Invalid ObjectType List2 for RetrievalOp", ""))
	code, out, _ := run(t, "soap", "retrieve", "List2", "--props", "ID")
	if code != exitAPI {
		t.Fatalf("expected API exit 1, got %d", code)
	}
	if h, _ := envelope(t, out)["hint"].(string); !strings.Contains(h, "describe") {
		t.Fatalf("hint must point at describe: %q", h)
	}
}

func TestSoapRetrieveLimitCapsLoudly(t *testing.T) {
	var bodies []string
	results := strings.Repeat(`<Results><ID>1</ID></Results>`, 5)
	fakeSFMC(t, soapWire(&bodies, "OK", results))
	code, out, _ := run(t, "soap", "retrieve", "List", "--props", "ID", "--limit", "2")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	e := envelope(t, out)
	items, _ := e["data"].([]any)
	if len(items) != 2 || e["count"].(float64) != 2 {
		t.Fatalf("--limit 2 must return 2 rows, got %d", len(items))
	}
	if h, _ := e["hint"].(string); !strings.Contains(h, "TRUNCATED to --limit 2") {
		t.Fatalf("truncation must be loud: %q", h)
	}
	// wire assertion: MaxRows travels as the client-side bound
	if !strings.Contains(bodies[0], "<tns:RetrieveRequest>") {
		t.Fatalf("missing RetrieveRequest: %s", bodies[0])
	}
}

func TestSoapRetrieveFieldsProjection(t *testing.T) {
	var bodies []string
	fakeSFMC(t, soapWire(&bodies, "OK", `<Results><ID>1</ID><ListName>News</ListName><Type>Public</Type></Results>`))
	code, out, _ := run(t, "soap", "retrieve", "List", "--props", "ID,ListName,Type", "--fields", "ID,ListName")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	row := envelope(t, out)["data"].([]any)[0].(map[string]any)
	if len(row) != 2 || row["ID"] != "1" || row["ListName"] != "News" {
		t.Fatalf("--fields must project output rows: %v", row)
	}
}
