// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package soap

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

const sampleResponse = `<?xml version="1.0" encoding="utf-8"?>
<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xmlns:tns="http://exacttarget.com/wsdl/partnerAPI">
  <soap:Body>
    <RetrieveResponseMsg xmlns="http://exacttarget.com/wsdl/partnerAPI">
      <Results xsi:type="tns:Account">
        <tns:ID>111111112</tns:ID>
        <tns:Name>_ParentBU_</tns:Name>
        <tns:AccountType>PRO_ACCOUNT</tns:AccountType>
        <tns:ParentID>111111112</tns:ParentID>
        <tns:Country>PL</tns:Country>
      </Results>
      <Results xsi:type="tns:Account">
        <tns:ID>111111116</tns:ID>
        <tns:Name>PreProd Parent</tns:Name>
        <tns:AccountType>PRO_BUSINESS_UNIT</tns:AccountType>
        <tns:ParentID>111111112</tns:ParentID>
      </Results>
      <OverallStatus>OK</OverallStatus>
    </RetrieveResponseMsg>
  </soap:Body>
</soap:Envelope>`

func soapFake(t *testing.T, body string, status int, seen *map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			b := make([]byte, 4096)
			n, _ := r.Body.Read(b)
			(*seen)["request"] = string(b[:n])
			(*seen)["soapaction"] = r.Header.Get("SOAPAction")
		}
		w.Header().Set("Content-Type", "text/xml; charset=utf-8")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRetrieveParsesResults(t *testing.T) {
	seen := map[string]string{}
	srv := soapFake(t, sampleResponse, 200, &seen)

	rows, status, err := Retrieve(context.Background(), srv.URL, "TOKEN1", "Account",
		[]string{"ID", "Name", "AccountType"})
	if err != nil {
		t.Fatal(err)
	}
	if status != "OK" {
		t.Fatalf("status=%q", status)
	}
	if len(rows) != 2 {
		t.Fatalf("rows=%d (%v)", len(rows), rows)
	}
	if rows[0]["ID"] != "111111112" || rows[0]["Name"] != "_ParentBU_" {
		t.Fatalf("row0: %v", rows[0])
	}
	if rows[1]["AccountType"] != "PRO_BUSINESS_UNIT" || rows[1]["ParentID"] != "111111112" {
		t.Fatalf("row1: %v", rows[1])
	}
	if !strings.Contains(seen["request"], "<fueloauth>TOKEN1</fueloauth>") {
		t.Fatalf("token must be in the security header: %s", seen["request"])
	}
	if !strings.Contains(seen["request"], "<tns:ObjectType>Account</tns:ObjectType>") {
		t.Fatalf("object type missing: %s", seen["request"])
	}
	if seen["soapaction"] != "Retrieve" {
		t.Fatalf("SOAPAction=%q", seen["soapaction"])
	}
}

func TestRetrieveFaultIsError(t *testing.T) {
	fault := `<?xml version="1.0" encoding="utf-8"?>
<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/">
  <soap:Body>
    <soap:Fault>
      <faultcode>soap:Client</faultcode>
      <faultstring>Invalid Object Type</faultstring>
    </soap:Fault>
  </soap:Body>
</soap:Envelope>`
	srv := soapFake(t, fault, 500, nil)

	_, _, err := Retrieve(context.Background(), srv.URL, "T", "Bogus", nil)
	if err == nil || !strings.Contains(err.Error(), "Invalid Object Type") {
		t.Fatalf("fault must surface: %v", err)
	}
}

func TestRetrievePagingStatus(t *testing.T) {
	paged := strings.Replace(sampleResponse, "<OverallStatus>OK</OverallStatus>",
		"<OverallStatus>MoreDataAvailable</OverallStatus>", 1)
	srv := soapFake(t, paged, 200, nil)

	_, status, err := Retrieve(context.Background(), srv.URL, "T", "Account", nil)
	if err != nil || status != "MoreDataAvailable" {
		t.Fatalf("status=%q err=%v", status, err)
	}
}

// Regression guard: RetrieveBusinessUnits MUST include QueryAllAccounts in
// the envelope — dropping it silently degrades discovery to the calling
// context (this exact bug shipped once).
func TestRetrieveBusinessUnitsSendsQueryAllAccounts(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 8192)
		n, _ := r.Body.Read(b)
		got = string(b[:n])
		_, _ = w.Write([]byte(`{"":null}`))
	}))
	defer srv.Close()

	_, _, err := RetrieveBusinessUnits(context.Background(), srv.URL, "T")
	_ = err // response unparseable is fine; we only care about the request
	if !strings.Contains(got, "<tns:QueryAllAccounts>true</tns:QueryAllAccounts>") {
		t.Fatalf("QueryAllAccounts missing from request body: %s", got)
	}
	if !strings.Contains(got, "<tns:ObjectType>BusinessUnit</tns:ObjectType>") {
		t.Fatalf("ObjectType missing: %s", got)
	}
}

// TestRetrieveEventObjectsSendsFilterAndProps locks the dv wire shape
// (found live 2026-09-16): event-object retrieves MUST carry Properties,
// the server-side EventDate filter (greatly cheaper than client-side
// truncation on multi-million-row data views), and honor MaxRows.
func TestRetrieveEventObjectsSendsFilterAndProps(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 8192)
		n, _ := r.Body.Read(b)
		got = string(b[:n])
		_, _ = w.Write([]byte(`{"":null}`))
	}))
	defer srv.Close()

	filters := []Filter{
		{Prop: "EventDate", Op: "greaterThan", Value: "2026-09-09T00:00:00"},
		{Prop: "SendID", Op: "equals", Value: "12345"},
	}
	_, _, err := Retrieve(context.Background(), srv.URL, "T", "SentEvent",
		[]string{"SubscriberKey", "EventDate", "SendID"}, Opts{Filters: filters, MaxRows: 100})
	_ = err

	if !strings.Contains(got, "<tns:ObjectType>SentEvent</tns:ObjectType>") {
		t.Fatalf("ObjectType missing: %s", got)
	}
	if !strings.Contains(got, "<tns:Properties>SubscriberKey</tns:Properties>") {
		t.Fatalf("Properties missing: %s", got)
	}
	// two ANDed conditions → ComplexFilterPart with LeftOperand/RightOperand
	// (official wire shape; bare alternation fails live with "Incorrect syntax
	// near the keyword 'AND'" — found live 2026-09-16)
	if !strings.Contains(got, `xsi:type="tns:ComplexFilterPart"`) {
		t.Fatalf("ComplexFilterPart wrapper missing for 2 filters: %s", got)
	}
	if !strings.Contains(got, `<tns:LeftOperand xsi:type="tns:SimpleFilterPart"><tns:Property>EventDate</tns:Property>`) {
		t.Fatalf("LeftOperand wrong: %s", got)
	}
	if !strings.Contains(got, `<tns:LogicalOperator>AND</tns:LogicalOperator><tns:RightOperand xsi:type="tns:SimpleFilterPart"><tns:Property>SendID</tns:Property>`) {
		t.Fatalf("RightOperand wrong: %s", got)
	}
	if !strings.Contains(got, "<tns:SimpleOperator>greaterThan</tns:SimpleOperator><tns:Value>2026-09-09T00:00:00</tns:Value>") {
		t.Fatalf("EventDate filter wrong: %s", got)
	}
	if !strings.Contains(got, "<tns:SimpleOperator>equals</tns:SimpleOperator><tns:Value>12345</tns:Value>") {
		t.Fatalf("SendID filter wrong: %s", got)
	}
	if strings.Contains(got, "<tns:QueryAllAccounts>") {
		t.Fatalf("QueryAllAccounts must stay off for event objects: %s", got)
	}
}

// TestRetrieveSingleFilterIsSimpleFilterPart: exactly one filter must NOT be
// wrapped in ComplexFilterPart (matches the shape verified live in probes).
func TestRetrieveSingleFilterIsSimpleFilterPart(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 8192)
		n, _ := r.Body.Read(b)
		got = string(b[:n])
		_, _ = w.Write([]byte(`{"":null}`))
	}))
	defer srv.Close()

	_, _, _ = Retrieve(context.Background(), srv.URL, "T", "Send",
		[]string{"ID"}, Opts{Filters: []Filter{{Prop: "ID", Op: "equals", Value: "12345"}}})

	if strings.Contains(got, "ComplexFilterPart") {
		t.Fatalf("single filter must be a bare SimpleFilterPart: %s", got)
	}
	if !strings.Contains(got, `xsi:type="tns:SimpleFilterPart"`) {
		t.Fatalf("filter missing: %s", got)
	}
}

// TestRetrieveMaxRowsStopsPaging: when MaxRows is reached the loop must NOT
// issue a ContinueRequest (cost control on huge data views).
func TestRetrieveMaxRowsStopsPaging(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		body := make([]byte, 8192)
		n, _ := r.Body.Read(body)
		if strings.Contains(string(body[:n]), "ContinueRequest") {
			t.Errorf("MaxRows reached but ContinueRequest was sent")
		}
		_, _ = w.Write([]byte(`<soap:Envelope xmlns:soap="http://www.w3.org/2003/05/soap-envelope">
<soap:Body><RetrieveResponseMsg xmlns="http://exacttarget.com/wsdl/partnerAPI">
<OverallStatus>MoreDataAvailable</OverallStatus><RequestID>req-1</RequestID>
<Results xsi:type="SentEvent"><SendID>1</SendID></Results>
<Results xsi:type="SentEvent"><SendID>2</SendID></Results>
</RetrieveResponseMsg></soap:Body></soap:Envelope>`))
	}))
	defer srv.Close()

	rows, status, err := Retrieve(context.Background(), srv.URL, "T", "SentEvent",
		[]string{"SendID"}, Opts{MaxRows: 2})
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 call, got %d", calls)
	}
	if status != "OK" || len(rows) != 2 {
		t.Fatalf("status=%q rows=%d (MaxRows must normalize MoreDataAvailable→OK)", status, len(rows))
	}
}

// TestParseFlattensPartnerProperties: SentEvent rows return some requested
// values (SubscriberID) inside PartnerProperties/Name+Value pairs instead of
// direct children — they must be flattened into the row, not dropped
// (found live 2026-09-16: SubscriberID vanished from dv sent output).
func TestParseFlattensPartnerProperties(t *testing.T) {
	body := `<soap:Envelope xmlns:soap="http://www.w3.org/2003/05/soap-envelope">
<soap:Body><RetrieveResponseMsg xmlns="http://exacttarget.com/wsdl/partnerAPI">
<OverallStatus>OK</OverallStatus><RequestID>r1</RequestID>
<Results xsi:type="SentEvent" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
<PartnerKey xsi:nil="true"/>
<PartnerProperties><Name>SubscriberID</Name><Value>8039923</Value></PartnerProperties>
<SendID>12345</SendID><SubscriberKey>sk-1</SubscriberKey>
</Results>
</RetrieveResponseMsg></soap:Body></soap:Envelope>`
	rows, status, _, err := parse([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if status != "OK" || len(rows) != 1 {
		t.Fatalf("status=%q rows=%d", status, len(rows))
	}
	if rows[0]["SubscriberID"] != "8039923" {
		t.Fatalf("SubscriberID not flattened from PartnerProperties: %v", rows[0])
	}
	if rows[0]["SendID"] != "12345" {
		t.Fatalf("direct child lost: %v", rows[0])
	}
	if _, has := rows[0]["PartnerProperties"]; has {
		t.Fatalf("PartnerProperties blob must not appear as a column: %v", rows[0])
	}
}

// TestSOAPDebugDumpRedactsToken: MCECLI_SOAP_DEBUG dumps must never carry the
// access token (production leak found 2026-09-17 — REST debug redacted,
// SOAP did not; SKILL promised "auth redacted" for both).
func TestSOAPDebugDumpRedactsToken(t *testing.T) {
	dir := t.TempDir()
	dump := dir + "/wire"
	t.Setenv("MCECLI_SOAP_DEBUG", dump)
	token := "SECRETJWT.TOKEN.VALUE"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"":null}`))
	}))
	defer srv.Close()

	_, _, _ = Retrieve(context.Background(), srv.URL, token, "SentEvent",
		[]string{"SubscriberKey"}, Opts{Filters: []Filter{{Prop: "EventDate", Op: "greaterThan", Value: "2026-01-01"}}})

	b, err := os.ReadFile(dump + ".0")
	if err != nil {
		t.Fatalf("debug dump missing: %v", err)
	}
	if strings.Contains(string(b), token) {
		t.Fatalf("debug dump contains the raw token: %s", string(b))
	}
	if !strings.Contains(string(b), "[REDACTED]") {
		t.Fatalf("dump should contain [REDACTED]: %s", string(b))
	}
}
