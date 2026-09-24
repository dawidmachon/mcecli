// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package soap is a minimal, dependency-free client for the SFMC SOAP API.
//
// It implements exactly one verb — Retrieve — generically: any ObjectType,
// any Properties list, results returned as plain property maps. Auth reuses
// the OAuth v2 token (passed in the SOAP security header as oAuthToken), so
// there is no second credential flow.
//
// The envelope/response shapes follow the official docs but are marked
// UNVERIFIED in docs/dev/endpoint-notes.md until probed live; adjust here and
// in the httptest fakes together.
package soap

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/dawidmachon/mcecli/internal/config"
	"github.com/dawidmachon/mcecli/internal/httpc"
)

const envelopeTpl = `<?xml version="1.0" encoding="UTF-8"?>
<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/" xmlns:tns="http://exacttarget.com/wsdl/partnerAPI" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xmlns:xsd="http://www.w3.org/2001/XMLSchema">
  <soap:Header>
    <fueloauth>%s</fueloauth>
  </soap:Header>
  <soap:Body>
    <tns:RetrieveRequestMsg>
      <tns:RetrieveRequest>
        <tns:ObjectType>%s</tns:ObjectType>
%s
      </tns:RetrieveRequest>
    </tns:RetrieveRequestMsg>
  </soap:Body>
</soap:Envelope>`

// Opts carry optional behavior for SOAP retrieves. Zero value = defaults.
type Opts struct {
	// QueryAllAccounts: include all accounts of the enterprise in the
	// results (only useful with an enterprise-scoped token).
	QueryAllAccounts bool
	// Refresher, when set, is called once if SFMC faults with
	// "Token Expired" mid-run; it must return a FRESH token (or an error).
	Refresher func() (string, error)
	// Filters are server-side conditions ANDed into one SimpleFilterPart or
	// a nested ComplexFilterPart (2+). Ops: equals, greaterThan,
	// greaterThanOrEqual, lessThan, lessThanOrEqual. VERIFIED live with
	// greaterThan/equals on event objects and Send (2026-09-16).
	Filters []Filter
	// MaxRows stops the ContinueRequest loop once at least this many rows
	// are collected (client-side cap; the retrieve is bounded server-side
	// by filters — 2500 rows per round trip). 0 = retrieve all pages.
	MaxRows int
}

// Filter is one server-side retrieve condition.
type Filter struct {
	Prop  string
	Op    string // equals | greaterThan | greaterThanOrEqual | lessThan | lessThanOrEqual
	Value string
}

// Retrieve performs a SOAP RetrieveRequest and returns the merged results
// as property maps (namespace-agnostic local names) plus the final
// OverallStatus. MoreDataAvailable responses are transparently continued
// via ContinueRequest until the retrieve completes.
func Retrieve(ctx context.Context, soapURL, token, objectType string, properties []string, opts ...Opts) ([]map[string]string, string, error) {
	var o Opts
	if len(opts) > 0 {
		o = opts[0]
	}
	return retrieve(ctx, soapURL, token, objectType, properties, o)
}

// RetrieveBusinessUnits retrieves ALL business units of the enterprise
// (ObjectType=BusinessUnit + QueryAllAccounts). VERIFIED live: requires a
// token minted for the ENTERPRISE MID; from a BU/home context it returns
// only the calling unit.
func RetrieveBusinessUnits(ctx context.Context, soapURL, token string, opts ...Opts) ([]map[string]string, string, error) {
	o := Opts{QueryAllAccounts: true}
	if len(opts) > 0 {
		o = opts[0]
	}
	return retrieve(ctx, soapURL, token, "BusinessUnit",
		[]string{"ID", "Name", "AccountType", "ParentID", "ParentName", "IsActive"}, o)
}

// retrieve loops RetrieveRequest/ContinueRequest until OverallStatus is
// final, refreshing the token once on a "Token Expired" fault.
func retrieve(ctx context.Context, soapURL, token, objectType string, properties []string, o Opts) ([]map[string]string, string, error) {
	current := token
	var all []map[string]string
	status := ""
	requestID := ""

	filterXML := buildFilterXML(o.Filters)

	for attempt := 0; ; attempt++ {
		var props strings.Builder
		for _, p := range properties {
			props.WriteString("        <tns:Properties>" + xmlEscape(p) + "</tns:Properties>\n")
		}
		if o.QueryAllAccounts {
			props.WriteString("        <tns:QueryAllAccounts>true</tns:QueryAllAccounts>\n")
		}
		if requestID != "" {
			props.WriteString("        <tns:ContinueRequest><tns:RequestID>" + xmlEscape(requestID) + "</tns:RequestID></tns:ContinueRequest>\n")
		} else {
			props.WriteString(filterXML)
		}
		body := fmt.Sprintf(envelopeTpl, xmlEscape(current), xmlEscape(objectType), strings.TrimRight(props.String(), "\n"))
		if p := config.EnvGet("MCECLI_SOAP_DEBUG"); p != "" {
			// redact the token: debug dumps must never carry credentials
			// (REST debug already redacts via authRedact — parity here)
			_ = os.WriteFile(fmt.Sprintf("%s.%d", p, attempt),
				[]byte(strings.ReplaceAll(body, current, "[REDACTED]")), 0o600)
		}

		res, err := httpc.Do(ctx, http.MethodPost, soapURL+"/Service.asmx", "", []byte(body),
			map[string]string{"SOAPAction": "Retrieve", "Content-Type": "text/xml; charset=UTF-8", "fueloauth": current})
		if err != nil {
			return nil, "", fmt.Errorf("soap request failed: %w", err)
		}
		pRows, pStatus, pRequestID, perr := parse(res.Body)
		if perr != nil {
			// sfmc-sdk parity: a "Token Expired" fault is retried once with a
			// fresh token (long agent runs can outlive a 20-minute token)
			if strings.Contains(perr.Error(), "Token Expired") && o.Refresher != nil && attempt == 0 {
				if nt, rerr := o.Refresher(); rerr == nil {
					current = nt
					continue
				}
			}
			return nil, "", fmt.Errorf("soap %s: HTTP %d: %v", objectType, res.Status, perr)
		}
		all = append(all, pRows...)
		status = pStatus
		requestID = pRequestID
		if o.MaxRows > 0 && len(all) >= o.MaxRows {
			// client-side cap reached — skip remaining pages
			if status == "MoreDataAvailable" {
				status = "OK"
			}
			break
		}
		if status != "MoreDataAvailable" || requestID == "" {
			break
		}
	}
	return all, status, nil
}

// buildFilterXML renders Opts.Filters as one SimpleFilterPart (1 condition)
// or a ComplexFilterPart with ANDed conditions (2+). Returns "" when no
// filters are set. Same wire shape RetrieveFilteredRows has used for DE rows
// (verified live); also verified directly on event objects and Send.
func buildFilterXML(filters []Filter) string {
	if len(filters) == 0 {
		return ""
	}
	cond := func(f Filter) string {
		return "<tns:Property>" + xmlEscape(f.Prop) + "</tns:Property>" +
			"<tns:SimpleOperator>" + xmlEscape(f.Op) + "</tns:SimpleOperator>" +
			"<tns:Value>" + xmlEscape(f.Value) + "</tns:Value>"
	}
	if len(filters) == 1 {
		return "        <tns:Filter xsi:type=\"tns:SimpleFilterPart\">" + cond(filters[0]) + "</tns:Filter>\n"
	}
	// 2+ conditions: ComplexFilterPart with LeftOperand/LogicalOperator/
	// RightOperand. Operands are ABSTRACT FilterPart references — each MUST
	// declare xsi:type=SimpleFilterPart or the server answers "Unknown filter
	// of type FilterPart encountered" / "Incorrect syntax near keyword 'AND'"
	// (both observed live 2026-09-16 before adding operand xsi:type).
	inner := "<tns:LeftOperand xsi:type=\"tns:SimpleFilterPart\">" + cond(filters[0]) + "</tns:LeftOperand>"
	for _, f := range filters[1:] {
		inner = inner +
			"<tns:LogicalOperator>AND</tns:LogicalOperator>" +
			"<tns:RightOperand xsi:type=\"tns:SimpleFilterPart\">" + cond(f) + "</tns:RightOperand>"
	}
	return "        <tns:Filter xsi:type=\"tns:ComplexFilterPart\">" + inner + "</tns:Filter>\n"
}

// parse walks the SOAP response with a streaming decoder: collects direct
// children of every <Results> block (namespaces stripped), the
// OverallStatus text, the RequestID, and turns <Fault> into an error.
func parse(body []byte) ([]map[string]string, string, string, error) {
	var rows []map[string]string
	var statusB, requestIDB, faultText strings.Builder

	dec := xml.NewDecoder(bytes.NewReader(body))
	var (
		inResults bool
		inFault   bool
		inOverall bool
		inReqID   bool
		cur       map[string]string
		prop      string
		propText  strings.Builder
		depth     int
		// PartnerProperties flattening: SentEvent rows carry some requested
		// values (e.g. SubscriberID) inside PartnerProperties/Name+Value pairs
		// instead of direct children — flatten them into the row (found live
		// 2026-09-16: SubscriberID was silently dropped before).
		inPP    bool
		ppWhich string // "Name" or "Value" while inside a pair
		ppText  strings.Builder
		ppKey   string
	)

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, "", "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch {
			case t.Name.Local == "Fault" && !inResults:
				inFault = true
			case t.Name.Local == "Results" && !inResults:
				inResults, cur, depth = true, map[string]string{}, 0
			case inResults:
				depth++
				if depth == 1 {
					prop = t.Name.Local
					propText.Reset()
					inPP = prop == "PartnerProperties"
				} else if inPP && depth == 2 {
					ppWhich = t.Name.Local
					ppText.Reset()
				}
			case t.Name.Local == "OverallStatus":
				inOverall = true
			case t.Name.Local == "RequestID":
				inReqID = true
			}
		case xml.CharData:
			switch {
			case inPP && depth == 2:
				ppText.Write(t)
			case inResults && depth == 1:
				propText.Write(t)
			case inFault:
				faultText.Write(t)
			case inOverall:
				statusB.Write(t)
			case inReqID:
				requestIDB.Write(t)
			}
		case xml.EndElement:
			switch {
			case t.Name.Local == "Fault" && inFault:
				return nil, "", "", fmt.Errorf("SOAP fault: %s", strings.TrimSpace(faultText.String()))
			case t.Name.Local == "Results" && inResults:
				rows = append(rows, cur)
				inResults = false
				inPP = false
			case inPP && depth == 2:
				switch ppWhich {
				case "Name":
					ppKey = strings.TrimSpace(ppText.String())
				case "Value":
					if cur != nil && ppKey != "" {
						cur[ppKey] = strings.TrimSpace(ppText.String())
					}
				}
				ppWhich = ""
				depth--
			case inResults && depth == 1:
				if prop != "PartnerProperties" && cur != nil {
					cur[prop] = strings.TrimSpace(propText.String())
				}
				depth--
			case inResults:
				depth--
			case t.Name.Local == "OverallStatus":
				inOverall = false
			case t.Name.Local == "RequestID":
				inReqID = false
			}
		}
	}
	if fault := strings.TrimSpace(faultText.String()); fault != "" {
		return nil, "", "", fmt.Errorf("SOAP fault: %s", fault)
	}
	return rows, strings.TrimSpace(statusB.String()), strings.TrimSpace(requestIDB.String()), nil
}

// xmlEscape returns s with XML special chars replaced by entity references.
func xmlEscape(s string) string {
	var b bytes.Buffer
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

// filterPart builds a SimpleFilterPart element content (property + operator + value).
// Does NOT include the outer <tns:Filter> wrapper — callers wrap it.
func filterPart(property, value string) string {
	return "<tns:Property>" + xmlEscape(property) + "</tns:Property>" +
		"<tns:SimpleOperator>equals</tns:SimpleOperator>" +
		"<tns:Value>" + xmlEscape(value) + "</tns:Value>"
}

// buildPKFilter returns the inner filter content for one or more ANDed
// exact-match conditions. 1 condition → bare SimpleFilterPart content;
// 2+ → ComplexFilterPart with typed LeftOperand/RightOperand (each operand
// IS the typed SimpleFilterPart wrapper, not double-wrapped — verified live
// with SentEvent 2-filter AND; same shape as buildFilterXML).
// Does NOT include the outer Filter wrapper — callers provide it.
func buildPKFilter(pk map[string]string) string {
	keys := make([]string, 0, len(pk))
	for k := range pk {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) == 1 {
		return filterPart(keys[0], pk[keys[0]])
	}
	// Multi-PK: ComplexFilterPart with typed operands.
	// LeftOperand IS the typed SimpleFilterPart (not a wrapper around one).
	inner := "<tns:LeftOperand xsi:type=\"tns:SimpleFilterPart\">" +
		filterPart(keys[0], pk[keys[0]]) +
		"</tns:LeftOperand>"
	for _, k := range keys[1:] {
		inner += "<tns:LogicalOperator>AND</tns:LogicalOperator>" +
			"<tns:RightOperand xsi:type=\"tns:SimpleFilterPart\">" +
			filterPart(k, pk[k]) +
			"</tns:RightOperand>"
	}
	return inner
}

// parseDERows walks a DataExtensionObject retrieve response. Each <Results>
// block carries repeated <Properties><Name>..</Name><Value>..</Value></Properties>
// pairs, flattened here into {name: value} rows.
func parseDERows(body []byte) ([]map[string]string, error) {
	var rows []map[string]string
	dec := xml.NewDecoder(bytes.NewReader(body))
	var (
		inResults, inProps, inName, inValue bool
		cur                                 map[string]string
		name                                string
		text                                strings.Builder
	)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "Results":
				inResults, cur = true, map[string]string{}
			case "Properties":
				if inResults {
					inProps = true
				}
			case "Name":
				if inProps {
					inName, text = true, strings.Builder{}
				}
			case "Value":
				if inProps {
					inValue, text = true, strings.Builder{}
				}
			}
		case xml.CharData:
			switch {
			case inName:
				text.Write(t)
			case inValue:
				text.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "Name":
				if inName {
					name = text.String()
					inName = false
				}
			case "Value":
				if inValue {
					if cur != nil {
						cur[name] = text.String()
					}
					inValue = false
				}
			case "Properties":
				inProps = false
			case "Results":
				if inResults {
					rows = append(rows, cur)
					inResults = false
				}
			}
		}
	}
	return rows, nil
}

// RetrieveDERows fetches rows of a data extension matching exact PK values
// (server-side filter — one cheap call even on huge DEs). The DE key is
// embedded in the ObjectType as DataExtensionObject[DE_KEY] (mcdev/sfmc-sdk
// pattern). Returns flattened {column: value} rows.
func RetrieveDERows(ctx context.Context, soapURL, token, deKey string, pk map[string]string) ([]map[string]string, error) {
	return RetrieveFilteredRows(ctx, soapURL, token, deKey, nil, pk)
}

// RetrieveFilteredRows fetches rows from a data extension using SOAP
// server-side filtering. The DE key is embedded in the ObjectType as
// DataExtensionObject[DE_KEY] (mcdev/sfmc-sdk pattern). Filters are exact-match
// (equals) on field names; multiple are ANDed. columns limits returned fields.
func RetrieveFilteredRows(ctx context.Context, soapURL, token, deKey string, columns []string, filters map[string]string) ([]map[string]string, error) {
	var colProps strings.Builder
	for _, c := range columns {
		colProps.WriteString("        <tns:Properties>" + xmlEscape(c) + "</tns:Properties>\n")
	}
	if len(columns) == 0 {
		colProps.WriteString("        <tns:Properties>*</tns:Properties>\n")
	}
	filterStr := ""
	if len(filters) > 0 {
		filterStr = "        <tns:Filter xsi:type=\"tns:SimpleFilterPart\">" + buildPKFilter(filters) + "</tns:Filter>\n"
	}
	objectType := "DataExtensionObject[" + deKey + "]"
	body := fmt.Sprintf(envelopeTpl, xmlEscape(token), objectType, colProps.String()+filterStr)
	if p := config.EnvGet("MCECLI_SOAP_DEBUG"); p != "" {
		_ = os.WriteFile(p+".derows",
			[]byte(strings.ReplaceAll(body, token, "[REDACTED]")), 0o600)
	}
	res, err := httpc.Do(ctx, http.MethodPost, soapURL+"/Service.asmx", "", []byte(body),
		map[string]string{"SOAPAction": "Retrieve", "Content-Type": "text/xml; charset=UTF-8", "fueloauth": token})
	if err != nil {
		return nil, fmt.Errorf("soap request failed: %w", err)
	}
	rows, perr := parseDERows(res.Body)
	if perr != nil {
		return nil, fmt.Errorf("soap DE rows: HTTP %d: %v", res.Status, perr)
	}
	return rows, nil
}

// DEField describes one column for DataExtension field addition.
type DEField struct {
	Name       string
	Type       string // Text, Number, Date, Boolean, Decimal, EmailAddress…
	MaxLength  int
	Scale      int
	IsRequired bool
	Nullable   bool
}

// UpdateDEFields adds columns to an existing data extension via SOAP
// UpdateRequest with Objects xsi:type="DataExtension" + Fields/Field.
// VERIFIED live 2026-09-17: customer key + Name/MaxLength/IsRequired/
// IsNullable → "Data Extension updated." (field appears in subsequent
// DataExtensionField retrieves).
func UpdateDEFields(ctx context.Context, soapURL, token, deKey string, fields []DEField) (string, error) {
	var fb strings.Builder
	for _, f := range fields {
		fb.WriteString("        <tns:Field>\n")
		fb.WriteString("          <tns:Name>" + xmlEscape(f.Name) + "</tns:Name>\n")
		if f.MaxLength > 0 {
			fb.WriteString("          <tns:MaxLength>" + strconv.Itoa(f.MaxLength) + "</tns:MaxLength>\n")
		}
		if f.Scale > 0 {
			fb.WriteString("          <tns:Scale>" + strconv.Itoa(f.Scale) + "</tns:Scale>\n")
		}
		fb.WriteString("          <tns:IsRequired>" + strconv.FormatBool(f.IsRequired) + "</tns:IsRequired>\n")
		fb.WriteString("          <tns:IsNullable>" + strconv.FormatBool(f.Nullable) + "</tns:IsNullable>\n")
		fb.WriteString("          <tns:IsPrimaryKey>false</tns:IsPrimaryKey>\n")
		fb.WriteString("        </tns:Field>\n")
	}
	soap := `<?xml version="1.0" encoding="UTF-8"?>
<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/" xmlns:tns="http://exacttarget.com/wsdl/partnerAPI" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xmlns:xsd="http://www.w3.org/2001/XMLSchema">
  <soap:Header><fueloauth>` + xmlEscape(token) + `</fueloauth></soap:Header>
  <soap:Body>
    <tns:UpdateRequest>
      <tns:Options></tns:Options>
      <tns:Objects xsi:type="tns:DataExtension">
        <tns:CustomerKey>` + xmlEscape(deKey) + `</tns:CustomerKey>
        <tns:Fields>
` + fb.String() + `        </tns:Fields>
      </tns:Objects>
    </tns:UpdateRequest>
  </soap:Body>
</soap:Envelope>`
	if p := config.EnvGet("MCECLI_SOAP_DEBUG"); p != "" {
		_ = os.WriteFile(p+".defield", []byte(strings.ReplaceAll(soap, token, "[REDACTED]")), 0o600)
	}
	res, err := httpc.Do(ctx, http.MethodPost, soapURL+"/Service.asmx", "", []byte(soap),
		map[string]string{"SOAPAction": "Update", "Content-Type": "text/xml; charset=UTF-8", "fueloauth": token})
	if err != nil {
		return "", fmt.Errorf("soap request failed: %w", err)
	}
	s := string(res.Body)
	st := ""
	if i := strings.Index(s, "<StatusCode>"); i >= 0 {
		st = s[i+12 : min(len(s), i+40)]
		if j := strings.Index(st, "<"); j >= 0 {
			st = st[:j]
		}
	}
	msg := ""
	if i := strings.Index(s, "<StatusMessage>"); i >= 0 {
		msg = s[i+15 : min(len(s), i+150)]
		if j := strings.Index(msg, "<"); j >= 0 {
			msg = msg[:j]
		}
	}
	if strings.EqualFold(st, "Error") || strings.Contains(s, "<faultcode>") {
		return st, fmt.Errorf("SOAP Update error: %s", msg)
	}
	return msg, nil
}
