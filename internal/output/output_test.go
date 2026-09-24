// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package output

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestProjectObject(t *testing.T) {
	in := map[string]any{"name": "DE1", "customerKey": "K1", "secret": "x"}
	out := Project(in, []string{"name", "customerKey"})
	m := out.(map[string]any)
	if len(m) != 2 || m["name"] != "DE1" {
		t.Fatalf("projection failed: %v", m)
	}
	if _, has := m["secret"]; has {
		t.Fatal("unselected keys must be dropped")
	}
}

func TestProjectArrayAndDotPath(t *testing.T) {
	in := []any{
		map[string]any{"name": "a", "columns": map[string]any{"name": "Email", "type": "EmailAddress"}},
		map[string]any{"name": "b"},
	}
	out := Project(in, []string{"name", "columns.name"}).([]any)
	first := out[0].(map[string]any)
	if first["name"] != "a" || first["columns.name"] != "Email" {
		t.Fatalf("dot path projection failed: %v", first)
	}
	second := out[1].(map[string]any)
	if _, has := second["columns.name"]; has {
		t.Fatal("missing dot path must be omitted, not nil")
	}
}

func TestProjectPassthrough(t *testing.T) {
	if v := Project("plain", []string{"a"}); v != "plain" {
		t.Fatalf("non-object passthrough failed: %v", v)
	}
	if v := Project(map[string]any{"a": 1}, nil); len(v.(map[string]any)) != 1 {
		t.Fatal("empty fields must pass through untouched")
	}
}

func TestEnvelopeShapes(t *testing.T) {
	okEnv := OK(200, []any{1, 2})
	var buf bytes.Buffer
	if err := Print(okEnv, false, &buf); err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["ok"] != true || parsed["count"] != float64(2) {
		t.Fatalf("ok envelope: %v", parsed)
	}
	if _, has := parsed["error"]; has {
		t.Fatal("success envelope must not carry error key")
	}

	failEnv := Fail(404, "not found", "check the key")
	buf.Reset()
	_ = Print(failEnv, false, &buf)
	_ = json.Unmarshal(buf.Bytes(), &parsed)
	if parsed["ok"] != false || parsed["status"] != float64(404) {
		t.Fatalf("fail envelope: %v", parsed)
	}
	errObj := parsed["error"].(map[string]any)
	if errObj["message"] != "not found" {
		t.Fatalf("error message: %v", errObj)
	}
}
