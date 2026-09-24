// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dawidmachon/mcecli/internal/config"
)

// fakeSFMC starts a fake auth+REST host, points a fresh MCECLI_HOME profile at
// it and returns the fake server URL. The token route rejects client_id
// "bad" with invalid_client (models a credential without BU access).
func fakeSFMC(t *testing.T, extraRoutes func(mux *http.ServeMux)) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("MCECLI_HOME", dir)

	mux := http.NewServeMux()
	mux.HandleFunc("/v2/token", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		switch body["client_id"] {
		case "bad":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid_client","error_description":"Client ID bad not accessible for this account"}`))
			return
		case "narrow":
			_, _ = w.Write([]byte(`{"access_token":"NARROWTOKEN","token_type":"Bearer","expires_in":1200,"scope":"email_read"}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"TESTTOKEN","token_type":"Bearer","expires_in":1200,"scope":"data_extensions_read data_extensions_write","rest_instance_url":"http://rest.example/","soap_instance_url":"http://soap.example/"}`))
	})
	if extraRoutes != nil {
		extraRoutes(mux)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cfg := map[string]any{
		"profiles": map[string]any{
			"test": map[string]any{
				"subdomain":     "fake",
				"client_id":     "cid",
				"client_secret": "sec",
				"auth_base":     srv.URL,
				"rest_base":     srv.URL,
				"soap_base":     srv.URL,
				"bus":           map[string]any{"parent": "111", "region": "222"},
			},
		},
	}
	b, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	return srv.URL
}

func run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := Run(args, &out, &errb, "test", "SKILL")
	return code, out.String(), errb.String()
}

func envelope(t *testing.T, out string) map[string]any {
	t.Helper()
	var e map[string]any
	if err := json.Unmarshal([]byte(out), &e); err != nil {
		t.Fatalf("stdout is not a JSON envelope: %q (%v)", out, err)
	}
	return e
}

func dataMap(t *testing.T, out string) map[string]any {
	t.Helper()
	e := envelope(t, out)
	d, ok := e["data"].(map[string]any)
	if !ok {
		t.Fatalf("envelope data is not an object: %v", e)
	}
	return d
}

func TestRestGetPassthrough(t *testing.T) {
	fakeSFMC(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/data/v1/echo", func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer TESTTOKEN" {
				t.Errorf("missing bearer token: %q", r.Header.Get("Authorization"))
			}
			_, _ = w.Write([]byte(`{"foo":"bar"}`))
		})
	})
	code, out, _ := run(t, "rest", "GET", "data/v1/echo")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s stderr=%s", code, out, out)
	}
	e := envelope(t, out)
	if e["ok"] != true || e["status"] != float64(200) {
		t.Fatalf("bad envelope: %v", e)
	}
	if d := e["data"].(map[string]any); d["foo"] != "bar" {
		t.Fatalf("bad data: %v", d)
	}
}

func TestRestWriteGate(t *testing.T) {
	fakeSFMC(t, nil) // gate must fire before any network call
	code, out, _ := run(t, "rest", "POST", "data/v1/whatever", "--body", `{"a":1}`)
	if code != exitUsage {
		t.Fatalf("write without --write must exit %d, got %d (%s)", exitUsage, code, out)
	}
	e := envelope(t, out)
	if e["ok"] != false {
		t.Fatalf("expected error envelope: %v", e)
	}

	// DELETE with --write but without --confirm must also refuse
	code, out, _ = run(t, "rest", "DELETE", "data/v1/whatever", "--write")
	if code != exitUsage {
		t.Fatalf("DELETE without --confirm must exit %d, got %d", exitUsage, code)
	}
	_ = envelope(t, out)
}

func TestRestPostWithWrite(t *testing.T) {
	fakeSFMC(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/data/v1/create", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Errorf("method=%s", r.Method)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("body: %v", err)
			}
			if body["a"] != float64(1) {
				t.Errorf("bad body: %v", body)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":1}`))
		})
	})
	code, out, _ := run(t, "rest", "POST", "data/v1/create", "--write", "--body", `{"a":1}`)
	if code != exitOK {
		t.Fatalf("exit=%d out=%s stderr=%s", code, out, out)
	}
	e := envelope(t, out)
	if e["ok"] != true || e["status"] != float64(201) {
		t.Fatalf("bad envelope: %v", e)
	}
}

func TestRestCrossHostRefused(t *testing.T) {
	fakeSFMC(t, nil)
	code, out, _ := run(t, "rest", "GET", "https://evil.example.com/data/v1/x")
	if code != exitUsage {
		t.Fatalf("cross-host URL must be refused, got %d (%s)", code, out)
	}
}

func TestDeRowsItemsShape(t *testing.T) {
	fakeSFMC(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/data/v1/customobjectdata/key/DE1/rowset", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"links":{"self":"/v1/customobjectdata/token/T1/rowset?$page=1","next":"/v1/customobjectdata/token/T1/rowset?$page=2"},"requestToken":"T1","count":4,"page":1,"pageSize":2,"items":[{"keys":{"id":"1"},"values":{"email":"a@b.c","status":"Active"}},{"keys":{"id":"2"},"values":{"email":"x@y.z","status":"Bounced"}}]}`))
		})
	})
	code, out, _ := run(t, "de", "rows", "DE1", "--page", "1", "--size", "2")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	e := envelope(t, out)
	if e["count"] != float64(4) {
		t.Fatalf("count must come from SFMC 'count' field: %v", e)
	}
	if e["next"] != "data/v1/customobjectdata/token/T1/rowset?$page=2" {
		t.Fatalf("next must be a usable continuation path: %v", e["next"])
	}
	items := e["data"].([]any)
	if len(items) != 2 {
		t.Fatalf("data must be flattened rows: %v", items)
	}
	row := items[0].(map[string]any)
	if row["email"] != "a@b.c" || row["(key) id"] != "1" {
		t.Fatalf("keys+values must merge into one row: %v", row)
	}
}

func TestDeListSearch(t *testing.T) {
	fakeSFMC(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/data/v1/customObjects", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("$search") == "" {
				t.Errorf("$search required")
			}
			_, _ = w.Write([]byte(`{"count":1,"page":1,"pageSize":25,"links":{},"items":[{"name":"Preference_Center","key":"PC1","rowCount":10}]}`))
		})
	})
	code, out, _ := run(t, "de", "list", "--search", "pref")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	e := envelope(t, out)
	if e["count"] != float64(1) {
		t.Fatalf("--search must map to $search: %s", out)
	}

	// neither --search nor --category must be a usage error with a hint
	code, out, _ = run(t, "de", "list")
	if code != exitUsage {
		t.Fatalf("de list without search must exit %d", exitUsage)
	}
	_ = envelope(t, out)
}

func TestDeGet(t *testing.T) {
	fakeSFMC(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/data/v1/customObjects", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"count":1,"items":[{"id":"guid-1","key":"PC1","name":"PC1"}]}`))
		})
		mux.HandleFunc("/data/v1/customObjects/guid-1", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"id":"guid-1","key":"PC1","fieldCount":1}`))
		})
		mux.HandleFunc("/data/v1/customObjects/guid-1/fields", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"id":"guid-1","fields":[{"name":"Email","type":"Text","isPrimaryKey":true}]}`))
		})
	})
	code, out, _ := run(t, "de", "get", "PC1")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	e := envelope(t, out)
	if e["count"] != float64(1) {
		t.Fatalf("count = field count: %v", e)
	}
	d := e["data"].(map[string]any)
	if d["fields"] == nil || d["definition"] == nil {
		t.Fatalf("get must return definition + fields: %v", d)
	}
}

func TestApiIndex(t *testing.T) {
	fakeSFMC(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/messaging/v1/rest", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"kind":"discovery#restDescription","methods":{"discovery":{"path":"rest","httpMethod":"get"},"getMessageSendsCollection":{"path":"messageSends","httpMethod":"get","description":"Returns all message sends","parameters":{"$page":{"location":"query","required":false}}},"postEmailSendsSend":{"path":"emailSends/{id}/send","httpMethod":"post"}}}`))
		})
	})
	code, out, _ := run(t, "api", "messaging")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	e := envelope(t, out)
	if e["count"] != float64(2) { // "discovery" entry is skipped
		t.Fatalf("index must list methods minus discovery: %v", e)
	}

	code, out, _ = run(t, "api", "messaging", "getMessageSendsCollection")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	e = envelope(t, out)
	d := e["data"].(map[string]any)
	if d["path"] != "/messaging/v1/messageSends" || d["example"] == nil {
		t.Fatalf("detail must include resolved path + example: %v", d)
	}
}

func TestDeRowsFieldProjection(t *testing.T) {
	fakeSFMC(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/data/v1/customobjectdata/key/DE1/rowset", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`[{"email":"a@b.c","status":"Active","secret":"x"},{"email":"b@b.c","status":"Held","secret":"y"}]`))
		})
	})
	code, out, _ := run(t, "de", "rows", "DE1", "--fields", "email")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s stderr=%s", code, out, out)
	}
	if bytes.Contains([]byte(out), []byte("secret")) {
		t.Fatalf("--fields must drop unselected keys: %s", out)
	}
	if !bytes.Contains([]byte(out), []byte("a@b.c")) {
		t.Fatalf("projected value missing: %s", out)
	}
}

func TestStatusAndUse(t *testing.T) {
	fakeSFMC(t, nil)
	code, out, _ := run(t, "status")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s stderr=%s", code, out, out)
	}
	d := dataMap(t, out)
	if d["profile"] != "test" {
		t.Fatalf("single-profile fallback failed: %v", d)
	}

	if code, _, _ := run(t, "use", "test", "parent"); code != exitOK {
		t.Fatalf("use failed")
	}
	_, out, _ = run(t, "status")
	d = dataMap(t, out)
	if d["bu"] != "parent" || d["mid"] != "111" {
		t.Fatalf("BU not resolved: %v", d)
	}

	if code, _, _ := run(t, "use", "test", "nope"); code != exitCfg {
		t.Fatalf("unknown BU must exit %d", exitCfg)
	}
}

func TestAuthTestAllBus(t *testing.T) {
	baseURL := fakeSFMC(t, nil)
	// rewrite config: region gets its own credential the fake rejects
	dir := os.Getenv("MCECLI_HOME")
	cfg := `{"profiles":{"test":{"subdomain":"fake","client_id":"cid","client_secret":"sec",
		"auth_base":"` + baseURL + `","rest_base":"` + baseURL + `",
		"bus":{"parent":"111","region":{"mid":"222","client_id":"bad","client_secret":"badsec"}}}}}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	code, out, _ := run(t, "auth", "test", "--all-bus", "--refresh")
	if code != exitAPI {
		t.Fatalf("one failing pairing must exit %d, got %d (%s)", exitAPI, code, out)
	}
	e := envelope(t, out)
	if e["ok"] != false || e["count"] != float64(3) {
		t.Fatalf("bad envelope: %v", e)
	}
	rows := e["data"].([]any)
	if rows[0].(map[string]any)["bu"] != "(account)" || rows[0].(map[string]any)["ok"] != true {
		t.Fatalf("account row must pass: %v", rows[0])
	}
	region := rows[2].(map[string]any)
	if region["ok"] != false || !strings.Contains(region["error"].(string), "invalid_client") {
		t.Fatalf("region row must fail with invalid_client: %v", region)
	}

	// and the single-BU path must fail with a per-BU-aware hint
	code, out, _ = run(t, "auth", "test", "--bu", "region", "--refresh")
	if code != exitAPI {
		t.Fatalf("exit=%d out=%s stderr=%s", code, out, out)
	}
	e = envelope(t, out)
	if !strings.Contains(e["hint"].(string), "per-BU") {
		t.Fatalf("hint must point at the per-BU entry: %v", e)
	}
}

func TestBUDiscover(t *testing.T) {
	soapResp := `<?xml version="1.0" encoding="utf-8"?>
<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/" xmlns:tns="http://exacttarget.com/wsdl/partnerAPI">
  <soap:Body>
    <RetrieveResponseMsg xmlns="http://exacttarget.com/wsdl/partnerAPI">
      <Results><tns:ID>111111112</tns:ID><tns:Name>_ParentBU_</tns:Name><tns:AccountType>PRO_ACCOUNT</tns:AccountType></Results>
      <Results><tns:ID>100033344</tns:ID><tns:Name>East EU</tns:Name><tns:AccountType>PRO_BUSINESS_UNIT</tns:AccountType></Results>
      <OverallStatus>OK</OverallStatus>
    </RetrieveResponseMsg>
  </soap:Body>
</soap:Envelope>`
	fakeSFMC(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/Service.asmx", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/xml; charset=utf-8")
			_, _ = w.Write([]byte(soapResp))
		})
	})

	code, out, errb := run(t, "bu", "discover")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s stderr=%s", code, out, errb)
	}
	e := envelope(t, out)
	if e["count"] != float64(2) {
		t.Fatalf("expected 2 accounts: %v", e)
	}
	hint := e["hint"].(string)
	if !strings.Contains(hint, "--add") {
		t.Fatalf("multi-result hint must mention --add: %s", hint)
	}

	// --add imports new BUs, keeps existing entries intact
	code, out, _ = run(t, "bu", "discover", "--add")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s stderr=%s", code, out, errb)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	bus := cfg.Profiles["test"].BUs
	if bus["east_eu"] == nil || bus["east_eu"].MID != "100033344" {
		t.Fatalf("east_eu must be imported: %v", bus)
	}
	if bus["parent"].MID != "111" || bus["region"].MID != "222" {
		t.Fatalf("existing entries must be untouched: %v", bus)
	}
	if bus["_parentbu_"] == nil || bus["_parentbu_"].MID != "111111112" {
		t.Fatalf("parent account must be imported under sanitized name: %v", bus)
	}
}

func TestBUDiscoverScopedPackage(t *testing.T) {
	// A BU-scoped package sees only itself: EXPECTED, and the envelope must
	// say so explicitly so agents do not misread it as the full estate.
	soapResp := `<?xml version="1.0" encoding="utf-8"?>
<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/">
  <soap:Body>
    <RetrieveResponseMsg xmlns="http://exacttarget.com/wsdl/partnerAPI">
      <Results><ID>111111116</ID><Name>Only Me</Name><AccountType>PRO_BUSINESS_UNIT</AccountType></Results>
      <OverallStatus>OK</OverallStatus>
    </RetrieveResponseMsg>
  </soap:Body>
</soap:Envelope>`
	fakeSFMC(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/Service.asmx", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(soapResp))
		})
	})

	code, out, _ := run(t, "bu", "discover")
	if code != exitOK {
		t.Fatalf("scoped discovery is not an error; exit=%d out=%s", code, out)
	}
	e := envelope(t, out)
	hint := e["hint"].(string)
	if !strings.Contains(hint, "EXPECTED") || !strings.Contains(hint, "parent") {
		t.Fatalf("hint must explain BU-scoped behavior: %s", hint)
	}
}

func TestSanitizeBUName(t *testing.T) {
	cases := map[string]string{
		"PreProd_Parent":  "preprod_parent",
		"BU - PL/East":    "bu_-_pl_east",
		"  Spaces  In  ":  "spaces__in",    // consecutive spaces each become _
		"Ünïcode Bonkers": "ncode_bonkers", // non-ascii dropped
		"":                "bu",
	}
	for in, want := range cases {
		if got := sanitizeBUName(in); got != want {
			t.Errorf("sanitizeBUName(%q)=%q want %q", in, got, want)
		}
	}
}

func TestApiCache(t *testing.T) {
	hits := 0
	fakeSFMC(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/messaging/v1/rest", func(w http.ResponseWriter, r *http.Request) {
			hits++
			_, _ = w.Write([]byte(`{"methods":{"getMessageSendsCollection":{"path":"messageSends","httpMethod":"get","description":"all sends"}}}`))
		})
	})
	code, out, _ := run(t, "api", "messaging")
	if code != exitOK || hits != 1 {
		t.Fatalf("first call fetches live: code=%d hits=%d out=%s", code, hits, out)
	}
	if !strings.Contains(out, "cached to disk") {
		t.Fatalf("first call must say it cached: %s", out)
	}

	// second call: served from cache, zero network
	code, out, _ = run(t, "api", "messaging")
	if code != exitOK || hits != 1 {
		t.Fatalf("second call must be a cache hit: code=%d hits=%d", code, hits)
	}
	if !strings.Contains(out, "disk cache") {
		t.Fatalf("second call must say cache: %s", out)
	}

	// --refresh forces the network
	if code, _, _ = run(t, "api", "messaging", "--refresh"); code != exitOK || hits != 2 {
		t.Fatalf("refresh must re-fetch: code=%d hits=%d", code, hits)
	}
}

func TestDangerousGate(t *testing.T) {
	fakeSFMC(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/messaging/v1/messageSends/abc/send", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"ok":true}`))
		})
		mux.HandleFunc("/interaction/v1/interactions/j1/stop", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"ok":true}`))
		})
		mux.HandleFunc("/gone", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{}`))
		})
		mux.HandleFunc("/data/v1/async/dataExtensions/guid/rows", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"requestId":"r1"}`))
		})
	})
	// POST .../send is tier 2: --write alone must NOT be enough
	code, out, _ := run(t, "rest", "POST", "messaging/v1/messageSends/abc/send", "--write", "--body", `{}`)
	if code != exitUsage {
		t.Fatalf("send without --confirm must be refused: %d %s", code, out)
	}
	e := envelope(t, out)
	if !strings.Contains(e["error"].(map[string]any)["message"].(string), "DANGEROUS") {
		t.Fatalf("refusal must say DANGEROUS: %v", e)
	}
	// ... and with --confirm it passes
	code, _, _ = run(t, "rest", "POST", "messaging/v1/messageSends/abc/send", "--write", "--confirm", "--body", `{}`)
	if code != exitOK {
		t.Fatalf("send with --write --confirm must pass: %d", code)
	}
	// journey stop flagged
	code, out, _ = run(t, "rest", "POST", "interaction/v1/interactions/j1/stop", "--write", "--body", `{}`)
	if code != exitUsage {
		t.Fatalf("journey stop must be refused: %d", code)
	}
	_ = envelope(t, out)
	// PUT overwrite of journey definitions flagged
	code, _, _ = run(t, "rest", "PUT", "interaction/v1/interactions", "--write", "--body", `{}`)
	if code != exitUsage {
		t.Fatalf("journey overwrite must be refused: %d", code)
	}
	// DELETE needs --confirm (tier 2)
	code, _, _ = run(t, "rest", "DELETE", "gone", "--write")
	if code != exitUsage {
		t.Fatalf("DELETE without --confirm must be refused: %d", code)
	}
	code, _, _ = run(t, "rest", "DELETE", "gone", "--write", "--confirm")
	if code != exitOK {
		t.Fatalf("DELETE with --write --confirm must pass: %d", code)
	}
	// tier-1 write still passes with just --write (rows upsert path)
	code, out, _ = run(t, "rest", "PUT", "data/v1/async/dataExtensions/guid/rows", "--write", "--body", `{"items":[]}`)
	if code != exitOK {
		t.Fatalf("plain rows upsert must stay tier 1: %d %s", code, out)
	}
}

func TestDeAdd(t *testing.T) {
	fakeSFMC(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/data/v1/customObjects", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"count":1,"items":[{"id":"guid-9","key":"PC1","name":"PC1"}]}`))
		})
		mux.HandleFunc("/data/v1/async/dataextensions/key:PC1/rows", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPut {
				t.Errorf("method=%s", r.Method)
			}
			var body struct {
				Items []map[string]any `json:"items"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if len(body.Items) != 1 || body.Items[0]["RowId"] != "row-9" {
				t.Errorf("flat row expected: %v", body)
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"requestId":"req-1"}`))
		})
	})
	// gate: no --write refused
	if code, _, _ := run(t, "de", "add", "PC1", "--data", `{"RowId":"row-9"}`); code != exitUsage {
		t.Fatalf("de add without --write must refuse")
	}
	code, out, _ := run(t, "de", "add", "PC1", "--data", `{"RowId":"row-9","Note":"hi"}`, "--write")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	e := envelope(t, out)
	if e["ok"] != true || e["status"] != float64(202) {
		t.Fatalf("bad envelope: %v", e)
	}
}

func TestDeRowsNameFallback(t *testing.T) {
	fakeSFMC(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/data/v1/customobjectdata/key/MyDE/rowset", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"custom object data cannot be retrieved for key: MyDE"}`))
		})
		mux.HandleFunc("/data/v1/customObjects", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"count":1,"items":[{"id":"guid-7","key":"03699-GUID","name":"MyDE"}]}`))
		})
		mux.HandleFunc("/data/v1/customobjectdata/key/03699-GUID/rowset", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"count":1,"items":[{"keys":{"k":"1"},"values":{"v":"x"}}]}`))
		})
	})
	code, out, _ := run(t, "de", "rows", "MyDE")
	if code != exitOK {
		t.Fatalf("name fallback must succeed: %d %s", code, out)
	}
	e := envelope(t, out)
	if e["count"] != float64(1) {
		t.Fatalf("fallback rows missing: %v", e)
	}
}

func TestUnknownCommand(t *testing.T) {
	fakeSFMC(t, nil)
	if code, _, _ := run(t, "definitely-not-a-command"); code != exitUsage {
		t.Fatalf("unknown command must exit %d", exitUsage)
	}
}

func TestRawOutput(t *testing.T) {
	fakeSFMC(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/data/v1/plain", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"x":1}`))
		})
	})
	code, out, _ := run(t, "rest", "GET", "data/v1/plain", "--raw")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s stderr=%s", code, out, out)
	}
	if got := strings.TrimSpace(out); got != `{"x":1}` {
		t.Fatalf("--raw must print the body verbatim, got: %s", got)
	}
}

func TestAmbiguousDERefused(t *testing.T) {
	fakeSFMC(t, func(mux *http.ServeMux) {
		// 'dup' is the NAME of DE-A and the KEY of DE-B -> must refuse
		mux.HandleFunc("/data/v1/customObjects", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"count":2,"items":[{"id":"a","key":"key-a","name":"dup"},{"id":"b","key":"dup","name":"Other"}]}`))
		})
	})
	code, out, _ := run(t, "de", "add", "dup", "--data", `{"k":"1"}`, "--write")
	if code != exitAPI {
		t.Fatalf("ambiguous arg must be refused: %d %s", code, out)
	}
	e := envelope(t, out)
	msg := e["error"].(map[string]any)["message"].(string)
	if !strings.Contains(msg, "ambiguous") {
		t.Fatalf("must say ambiguous: %v", e)
	}
}

// --- wire-echo guards: assert the REQUEST that reaches the wire, not just
// a plausible response. These exist because a silently-dropped request
// parameter once shipped (QueryAllAccounts).

func TestDeListCategoryParamReachesWire(t *testing.T) {
	fakeSFMC(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/data/v1/customObjects", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("categoryId") == "" {
				t.Errorf("--category must map to categoryId param, got %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"count":0,"items":[]}`))
		})
	})
	if code, out, _ := run(t, "de", "list", "--category", "9387"); code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
}

func TestDeRowsNextUsesPathVerbatim(t *testing.T) {
	fakeSFMC(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/data/v1/customobjectdata/token/T9/rowset", func(w http.ResponseWriter, r *http.Request) {
			if !strings.Contains(r.URL.RawQuery, "page=2") {
				t.Errorf("continuation query lost: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"count":0,"items":[]}`))
		})
	})
	code, out, _ := run(t, "de", "rows", "--next", "data/v1/customobjectdata/token/T9/rowset?$page=2")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
}

func TestRestHeaderForwarded(t *testing.T) {
	fakeSFMC(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/data/v1/hdr", func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Probe") != "1" {
				t.Errorf("custom header missing")
			}
			_, _ = w.Write([]byte(`{}`))
		})
	})
	code, _, _ := run(t, "rest", "GET", "data/v1/hdr", "--header", "X-Probe: 1")
	if code != exitOK {
		t.Fatalf("exit code %d", code)
	}
}

func TestDeAddScopeAdvisory(t *testing.T) {
	baseURL := fakeSFMC(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/data/v1/customObjects", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"count":1,"items":[{"id":"g","key":"K","name":"K"}]}`))
		})
		mux.HandleFunc("/data/v1/async/dataextensions/key:K/rows", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"requestId":"r"}`))
		})
	})
	// narrow the profile's credential so the fake mints a scope-less token
	dir := os.Getenv("MCECLI_HOME")
	cfg := `{"profiles":{"test":{"subdomain":"fake","client_id":"narrow","client_secret":"sec",
		"auth_base":"` + baseURL + `","rest_base":"` + baseURL + `"}}}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, _ := run(t, "de", "add", "K", "--data", `{"RowId":"1"}`, "--write")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	e := envelope(t, out)
	hint, _ := e["hint"].(string)
	if !strings.Contains(hint, "data_extensions_write") {
		t.Fatalf("missing-scope advisory expected: %v", e)
	}
}

func TestDeFindAcrossBUs(t *testing.T) {
	fakeSFMC(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/data/v1/customObjects", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("$search") == "" {
				t.Errorf("search required")
			}
			_, _ = w.Write([]byte(`{"count":1,"items":[{"id":"g1","key":"MyDE","name":"MyDE","rowCount":5}]}`))
		})
	})
	code, out, _ := run(t, "de", "find", "MyDE")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	e := envelope(t, out)
	// targets: (account) + parent + region = 3 contexts, all match
	if e["count"] != float64(3) {
		t.Fatalf("expected 3 matches across contexts: %v", e)
	}
	first := e["data"].([]any)[0].(map[string]any)
	if first["match"] != "exact" || first["bu"] == "" {
		t.Fatalf("match metadata missing: %v", first)
	}
}

func TestDeFindUnknownCommandHint(t *testing.T) {
	fakeSFMC(t, nil)
	_, _, errb := run(t, "test") // "test" is a configured profile name
	if !strings.Contains(errb, "looks like a profile") {
		t.Fatalf("profile-like unknown command must be hinted: %s", errb)
	}
}

func TestDumpMaxAge(t *testing.T) {
	hits := 0
	fakeSFMC(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/data/v1/customObjects", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"count":1,"items":[{"id":"g9","key":"DE9","name":"DE9"}]}`))
		})
		mux.HandleFunc("/data/v1/customobjectdata/key/DE9/rowset", func(w http.ResponseWriter, r *http.Request) {
			hits++
			_, _ = w.Write([]byte(`{"links":{},"count":1,"items":[{"keys":{"id":"1"},"values":{"v":"x"}}]}`))
		})
	})
	// first dump: live pull
	if code, _, _ := run(t, "de", "dump", "DE9"); code != exitOK || hits != 1 {
		t.Fatalf("first dump must pull: hits=%d", hits)
	}
	// default: cache served
	if code, _, _ := run(t, "de", "dump", "DE9"); code != exitOK || hits != 1 {
		t.Fatalf("second dump must hit cache: hits=%d", hits)
	}
	// --max-age 0s: older-than-instant cache forces re-pull
	time.Sleep(10 * time.Millisecond)
	if code, _, _ := run(t, "de", "dump", "DE9", "--max-age", "0s"); code != exitOK || hits != 2 {
		t.Fatalf("max-age expiry must re-pull: hits=%d", hits)
	}
	// and the default again serves the NEW cache
	if code, _, _ := run(t, "de", "dump", "DE9"); code != exitOK || hits != 2 {
		t.Fatalf("cache refreshed: hits=%d", hits)
	}
}

func TestAssetPullBinaryFile(t *testing.T) {
	cdnBytes := []byte{0xFF, 0xD8, 0xFF, 0xE0} // fake JPEG magic
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(cdnBytes)
	}))
	defer cdn.Close()

	fakeSFMC(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/asset/v1/content/assets/99", func(w http.ResponseWriter, r *http.Request) {
			// publishedURL present -> priority 1: fetch from CDN
			_, _ = w.Write([]byte(`{"id":99,"name":"pic","assetType":{"name":"jpg"},"fileProperties":{"extension":"jpg"},"publishedURL":"` + cdn.URL + `/pic.jpg"}`))
		})
		mux.HandleFunc("/asset/v1/content/assets/98", func(w http.ResponseWriter, r *http.Request) {
			// no publishedURL, no content -> priority 3: /file base64-in-JSON
			_, _ = w.Write([]byte(`{"id":98,"name":"doc","assetType":{"name":"pdf"},"fileProperties":{"extension":"pdf"}}`))
		})
		mux.HandleFunc("/asset/v1/content/assets/98/file", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`"JVBERi0="`)) // base64("%PDF-")
		})
	})
	home := os.Getenv("MCECLI_HOME")

	// priority 1: publishedURL fetch
	if code, _, _ := run(t, "asset", "pull", "99"); code != exitOK {
		t.Fatalf("pull 99 failed")
	}
	raw, err := os.ReadFile(filepath.Join(home, "work", "test", "asset", "99", "file.jpg"))
	if err != nil || string(raw) != string(cdnBytes) {
		t.Fatalf("CDN fetch must be decoded to file.jpg: %v %d bytes", err, len(raw))
	}

	// priority 3: /file base64-in-JSON decode
	if code, _, _ := run(t, "asset", "pull", "98"); code != exitOK {
		t.Fatalf("pull 98 failed")
	}
	raw2, err := os.ReadFile(filepath.Join(home, "work", "test", "asset", "98", "file.pdf"))
	if err != nil || string(raw2) != "%PDF-" {
		t.Fatalf("base64 JSON must decode: %v %q", err, raw2)
	}
}

func TestDeAddBatchedNDJSON(t *testing.T) {
	chunks := []int{}
	fakeSFMC(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/data/v1/customObjects", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"count":1,"items":[{"id":"g","key":"K","name":"K"}]}`))
		})
		mux.HandleFunc("/data/v1/async/dataextensions/key:K/rows", func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				Items []map[string]any `json:"items"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			chunks = append(chunks, len(body.Items))
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"requestId":"r"}`))
		})
	})
	// 3 NDJSON rows via stdin, batch size 2 -> two chunks (2+1)
	stdin = func() io.Reader { return strings.NewReader("{\"RowId\":\"1\"}\n{\"RowId\":\"2\"}\n{\"RowId\":\"3\"}") }
	code, out, _ := run(t, "de", "add", "K", "--data", "@-", "--batch-size", "2", "--write")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	e := envelope(t, out)
	dd := e["data"].(map[string]any)
	if dd["rows"] != float64(3) || dd["chunks"] != float64(2) {
		t.Fatalf("expected 3 rows in 2 chunks: %v", e)
	}
	if len(chunks) != 2 || chunks[0] != 2 || chunks[1] != 1 {
		t.Fatalf("chunks wrong: %v", chunks)
	}
}

func TestMDPullJourneys(t *testing.T) {
	fakeSFMC(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/interaction/v1/interactions", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("$pageSize") == "" {
				t.Errorf("pageSize param missing")
			}
			_, _ = w.Write([]byte(`{"count":2,"items":[{"id":"j1","key":"K1","name":"J One","stats":{}},{"id":"j2","key":"K2","name":"J Two"}]}`))
		})
	})
	home := os.Getenv("MCECLI_HOME")
	code, out, _ := run(t, "md", "pull", "journeys")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	e := envelope(t, out)
	dd := e["data"].(map[string]any)
	if dd["count"] != float64(2) {
		t.Fatalf("count mismatch: %v", e)
	}
	dir := filepath.Join(home, "work", "test", "md", "journeys")
	for _, want := range []string{"index.json", "k1.json", "k2.json"} {
		if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
			t.Fatalf("%s missing: %v", want, err)
		}
	}
	idx, _ := os.ReadFile(filepath.Join(dir, "index.json"))
	if !strings.Contains(string(idx), `"key": "K1"`) {
		t.Fatalf("index malformed: %s", idx)
	}
	// fields projection survives to disk
	if code, out, _ = run(t, "md", "pull", "journeys", "--refresh", "--fields", "name"); code != exitOK {
		t.Fatalf("exit=%d", code)
	}
	j1, _ := os.ReadFile(filepath.Join(dir, "j1.json"))
	if strings.Contains(string(j1), `"id"`) {
		t.Fatalf("--fields must project stored items: %s", j1)
	}
}

func TestMDSearchRequired(t *testing.T) {
	fakeSFMC(t, nil)
	code, out, _ := run(t, "md", "pull", "des")
	if code != exitUsage {
		t.Fatalf("des without --search must exit %d", exitUsage)
	}
	e := envelope(t, out)
	if e["ok"] != false {
		t.Fatalf("expected error envelope: %v", e)
	}
}

func TestJournalCommandAndWriteRecording(t *testing.T) {
	calls := 0
	fakeSFMC(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/data/v1/customObjects", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"count":1,"items":[{"id":"g","key":"K","name":"K"}]}`))
		})
		mux.HandleFunc("/data/v1/async/dataextensions/key:K/rows", func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"requestId":"rr"}`))
		})
	})
	// gated write -> journaled
	code, _, _ := run(t, "de", "add", "K", "--data", `{"RowId":"1"}`, "--write")
	if code != exitOK {
		t.Fatalf("write failed")
	}
	if calls != 1 {
		t.Fatalf("write must hit the API once")
	}
	// journal reads it back
	code, out, _ := run(t, "journal", "--json")
	if code != exitOK {
		t.Fatalf("journal exit=%d", code)
	}
	if !strings.Contains(out, `"method":"PUT"`) || !strings.Contains(out, `"request_id":"rr"`) {
		t.Fatalf("journal must show the write: %s", out)
	}
	if strings.Contains(out, "access_token") {
		t.Fatal("journal must never contain tokens")
	}
	// --profile filter works (no entries for "other")
	code, out, _ = run(t, "journal", "--profile", "other")
	e := envelope(t, out)
	if dd, ok := e["data"].([]any); ok && len(dd) != 0 {
		t.Fatalf("profile filter must exclude entries: %v", e)
	}
}

func TestDoctorOffline(t *testing.T) {
	fakeSFMC(t, nil)
	code, out, _ := run(t, "doctor", "--offline")
	if code != exitOK {
		t.Fatalf("doctor --offline must be healthy: %s", out)
	}
	e := envelope(t, out)
	dd := e["data"].([]any)
	if len(dd) < 4 {
		t.Fatalf("expected config/state/credentials/cache checks: %v", dd)
	}
}

func TestUndoSnapshotOnDelete(t *testing.T) {
	fakeSFMC(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/data/v1/customObjects", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				_, _ = w.Write([]byte(`{"count":1,"items":[{"id":"gone-1","key":"OLD","name":"Old DE"}]}`))
				return
			}
			_, _ = w.Write([]byte(`{}`))
		})
		mux.HandleFunc("/data/v1/customObjects/gone-1", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				_, _ = w.Write([]byte(`{"id":"gone-1","name":"Old DE","key":"OLD"}`))
				return
			}
			if r.Method == http.MethodDelete {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{}`))
				return
			}
			_, _ = w.Write([]byte(`{}`))
		})
	})
	code, out, errb := run(t, "rest", "DELETE", "data/v1/customObjects/gone-1", "--write", "--confirm")
	if code != exitOK {
		t.Fatalf("delete exit=%d out=%s", code, out)
	}
	// snapshot note goes to stderr; stdout stays a single parseable envelope
	if !strings.Contains(errb, "before-image captured") {
		t.Fatalf("snapshot note expected on stderr: %s", errb)
	}
	e := envelope(t, out)
	// undo list shows the snapshot; request.json/response.json exist
	if code, out, _ = run(t, "undo", "list", "--profile", "test"); code != exitOK {
		t.Fatalf("undo list exit=%d", code)
	}
	e = envelope(t, out)
	arr, _ := e["data"].([]any)
	found := false
	for _, it := range arr {
		m := it.(map[string]any)
		if strings.Contains(m["saved"].(string), "gone-1") {
			found = true
		}
	}
	if !found {
		t.Fatalf("snapshot for gone-1 not listed: %v", e)
	}
}
