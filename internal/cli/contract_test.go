// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

// Contract guards: these tests freeze behaviors that other components (and
// LLM agents) depend on. If one of these fails during a refactor, the change
// is a breaking change — not a refactor.

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/dawidmachon/mcecli/internal/output"
)

// TestEnvelopeGolden freezes the JSON contract of the envelope: field names,
// their order, and omitempty behavior. Any diff here is a BREAKING change.
func TestEnvelopeGolden(t *testing.T) {
	success := output.OK(200, []any{map[string]any{"a": float64(1)}, map[string]any{"a": float64(2)}})
	success.Count = 2
	got, err := json.Marshal(success)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"ok":true,"status":200,"count":2,"data":[{"a":1},{"a":2}]}`
	if string(got) != want {
		t.Fatalf("success envelope contract changed:\n got %s\nwant %s", got, want)
	}

	failure := output.Fail(404, "not found", "check the key")
	got, err = json.Marshal(failure)
	if err != nil {
		t.Fatal(err)
	}
	want = `{"ok":false,"status":404,"hint":"check the key","error":{"message":"not found"}}`
	if string(got) != want {
		t.Fatalf("failure envelope contract changed:\n got %s\nwant %s", got, want)
	}
}

// TestClassifyWriteTable locks the two-tier gate taxonomy.
func TestClassifyWriteTable(t *testing.T) {
	cases := []struct {
		method, path string
		dangerous    bool
		reasonHas    string
	}{
		{"GET", "data/v1/anything", false, ""},
		{"POST", "data/v1/customObjects", false, ""},                   // create DE
		{"POST", "data/v1/async/dataextensions/key:K/rows", false, ""}, // row upsert
		{"PUT", "data/v1/async/dataextensions/guid/rows", false, ""},   // row upsert
		{"POST", "data/v1/customobjectdata/export", false, ""},         // export job
		{"DELETE", "asset/v1/content/assets/123", true, "DELETE"},      // destruction
		{"POST", "messaging/v1/messageSends/1/send", true, "/send"},    // sends!
		{"POST", "interaction/v1/interactions/j1/stop", true, "/stop"}, // journey stop
		{"POST", "interaction/v1/interactions/j1/publishAsync", true, "/publish"},
		{"POST", "automation/v1/automations/a1/start", true, "/start"},     // run automation
		{"POST", "data/v1/customObjects/1/cleardata", true, "/cleardata"},  // wipe rows
		{"PUT", "interaction/v1/interactions", true, "overwrites"},         // journey overwrite
		{"PATCH", "interaction/v1/eventDefinitions/1", true, "overwrites"}, // definition overwrite
		{"post", "MESSAGING/v1/messageSends/1/SEND", true, "/send"},        // case-insensitive
	}
	for _, tc := range cases {
		dangerous, reason := classifyWrite(tc.method, tc.path)
		if dangerous != tc.dangerous {
			t.Errorf("classifyWrite(%s %s) = %v (%s), want %v", tc.method, tc.path, dangerous, reason, tc.dangerous)
			continue
		}
		if dangerous && tc.reasonHas != "" && !strings.Contains(reason, tc.reasonHas) {
			t.Errorf("classifyWrite(%s %s) reason %q must contain %q", tc.method, tc.path, reason, tc.reasonHas)
		}
	}
}

// TestResolveNextTable locks continuation-path normalization.
func TestResolveNextTable(t *testing.T) {
	cases := map[string]string{
		"/v1/customobjectdata/token/T/rowset?$page=2": "data/v1/customobjectdata/token/T/rowset?$page=2",
		"customobjectdata/token/T/rowset?$page=2":     "data/v1/customobjectdata/token/T/rowset?$page=2",
		"/asset/v1/content/assets?$page=2":            "asset/v1/content/assets?$page=2",
	}
	for in, want := range cases {
		if got := resolveNext(in); got != want {
			t.Errorf("resolveNext(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestLoadBodyVariants covers --body handling incl. the Windows stdin case.
func TestLoadBodyVariants(t *testing.T) {
	oldStdin, oldRead := stdin, osReadFile
	t.Cleanup(func() { stdin, osReadFile = oldStdin, oldRead })

	stdin = func() io.Reader { return strings.NewReader(`{"from":"stdin"}`) }
	osReadFile = func(name string) ([]byte, error) {
		if name == "f.json" {
			return []byte(`{"from":"file"}`), nil
		}
		return nil, errors.New("no such file")
	}

	if b, err := loadBody(""); err != nil || b != nil {
		t.Fatalf("empty body: %v %v", b, err)
	}
	for _, form := range []string{"-", "@-"} {
		b, err := loadBody(form)
		if err != nil || string(b) != `{"from":"stdin"}` {
			t.Fatalf("stdin via %q: %s %v", form, b, err)
		}
	}
	if b, err := loadBody("@f.json"); err != nil || string(b) != `{"from":"file"}` {
		t.Fatalf("@file: %s %v", b, err)
	}
	if _, err := loadBody("@missing.json"); err == nil {
		t.Fatal("missing file must error")
	}
	if b, err := loadBody(`{"a":1}`); err != nil || string(b) != `{"a":1}` {
		t.Fatalf("inline: %s %v", b, err)
	}
	if _, err := loadBody(`{invalid`); err == nil {
		t.Fatal("invalid inline JSON must error")
	}
}
