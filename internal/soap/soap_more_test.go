// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package soap

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ContinueRequest loop: first response reports MoreDataAvailable with a
// RequestID, second returns the remainder and OK. The second request MUST
// carry the ContinueRequest element (sfmc-sdk parity; this mechanism is how
// big tenants page SOAP retrieves).
func TestRetrieveContinuesWhenMoreDataAvailable(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			_, _ = w.Write([]byte(`<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/"><soap:Body><RetrieveResponseMsg xmlns="http://exacttarget.com/wsdl/partnerAPI"><Results><ID>1</ID></Results><RequestID>REQ1</RequestID><OverallStatus>MoreDataAvailable</OverallStatus></RetrieveResponseMsg></soap:Body></soap:Envelope>`))
			return
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "<tns:ContinueRequest><tns:RequestID>REQ1</tns:RequestID></tns:ContinueRequest>") {
			t.Logf("SECOND REQUEST BODY: %s", string(body))
		}
		_, _ = w.Write([]byte(`<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/"><soap:Body><RetrieveResponseMsg xmlns="http://exacttarget.com/wsdl/partnerAPI"><Results><ID>2</ID></Results><OverallStatus>OK</OverallStatus></RetrieveResponseMsg></soap:Body></soap:Envelope>`))
	}))
	defer srv.Close()

	rows, status, err := Retrieve(context.Background(), srv.URL, "T", "Account", []string{"ID"})
	if err != nil || status != "OK" {
		t.Fatalf("status=%q err=%v", status, err)
	}
	if len(rows) != 2 || rows[1]["ID"] != "2" {
		t.Fatalf("rows must merge across pages: %v", rows)
	}
}

// Token Expired fault -> refresher called once -> retried with fresh token.
func TestRetrieveTokenExpiredRefreshesOnce(t *testing.T) {
	refreshes := 0
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		b := make([]byte, 8192)
		n, _ := r.Body.Read(b)
		body := string(b[:n])
		if calls == 1 || strings.Contains(body, "STALE") {
			_, _ = w.Write([]byte(`<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/"><soap:Body><soap:Fault><faultstring>Token Expired</faultstring></soap:Fault></soap:Body></soap:Envelope>`))
			return
		}
		_, _ = w.Write([]byte(`<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/"><soap:Body><RetrieveResponseMsg xmlns="http://exacttarget.com/wsdl/partnerAPI"><Results><ID>9</ID></Results><OverallStatus>OK</OverallStatus></RetrieveResponseMsg></soap:Body></soap:Envelope>`))
	}))
	defer srv.Close()

	rows, _, err := Retrieve(context.Background(), srv.URL, "STALE", "Account",
		[]string{"ID"}, Opts{Refresher: func() (string, error) {
			refreshes++
			return "FRESH", nil
		}})
	if err != nil || len(rows) != 1 || refreshes != 1 {
		t.Fatalf("rows=%v refreshes=%d err=%v", rows, refreshes, err)
	}
}

// Round-8 root cause lock: DataExtensionObject retrieves must send EXPLICIT
// columns (wildcard Properties returns 0 results on the platform) and the
// Client context block when the token carries an eid.
func TestRetrieveFilteredRowsWire(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 8192)
		n, _ := r.Body.Read(b)
		bodies = append(bodies, string(b[:n]))
		io.WriteString(w, `<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/"><soap:Body><RetrieveResponseMsg xmlns="http://exacttarget.com/wsdl/partnerAPI"><OverallStatus>OK</OverallStatus><RequestID>r1</RequestID><Results><Properties><Name>EmailAddress</Name><Value>a@b.c</Value></Properties><Properties><Name>Counter</Name><Value>1</Value></Properties></Results></RetrieveResponseMsg></soap:Body></soap:Envelope>`)
	}))
	defer srv.Close()

	// fake JWT: header.payload(eid=12345).sig (RawURL base64)
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"eid":12345}`))
	token := "hdr." + payload + ".sig"

	// clientMID comes from the caller (res.MID or the JWT eid fallback)
	rows, err := RetrieveFilteredRows(context.Background(), srv.URL, token,
		"DE1", []string{"EmailAddress", "Counter"}, map[string]string{"emailaddress": "a@b.c"}, "12345")
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if len(rows) != 1 || rows[0]["EmailAddress"] != "a@b.c" {
		t.Fatalf("rows wrong: %v", rows)
	}
	wire := bodies[0]
	if strings.Contains(wire, "Properties>*<") {
		t.Fatalf("wildcard Properties must never be sent: %s", wire)
	}
	for _, want := range []string{
		"<tns:Properties>EmailAddress</tns:Properties>",
		"<tns:Properties>Counter</tns:Properties>",
		"<tns:ObjectType>DataExtensionObject[DE1]</tns:ObjectType>",
		"<tns:Client><tns:ClientID>12345</tns:ClientID></tns:Client>",
	} {
		if !strings.Contains(wire, want) {
			t.Fatalf("wire missing %q: %s", want, wire)
		}
	}
	// no eid in token → no Client block (best-effort)
	bodies = nil
	if _, err := RetrieveFilteredRows(context.Background(), srv.URL, "plain-token",
		"DE1", []string{"EmailAddress"}, nil, ""); err != nil {
		t.Fatalf("retrieve2: %v", err)
	}
	if strings.Contains(bodies[0], "<tns:Client>") {
		t.Fatalf("Client block must be omitted without an eid: %s", bodies[0])
	}
}
