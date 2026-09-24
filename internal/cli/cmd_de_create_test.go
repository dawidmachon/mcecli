package cli

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestParseFieldSpec(t *testing.T) {
	cases := []struct {
		in            string
		name, typ     string
		length, scale int
		hasLen        bool
		wantErr       bool
	}{
		{"Email:EmailAddress(254)", "Email", "EmailAddress", 254, 0, true, false},
		{"Amount:Decimal(18,2)", "Amount", "Decimal", 18, 2, true, false},
		{"Created:Date", "Created", "Date", 0, 0, false, false},
		{"Counter:Number", "Counter", "Number", 0, 0, false, false},
		{"Bad", "", "", 0, 0, false, true},
		{"X:Text(abc)", "", "", 0, 0, false, true},
	}
	for _, c := range cases {
		f, err := parseFieldSpec(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("%q: expected error", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if f.Name != c.name || f.Type != c.typ || f.Length != c.length || f.Scale != c.scale || f.HasLen != c.hasLen {
			t.Errorf("%q: got %+v", c.in, f)
		}
	}
}

func TestDECreateBuildsCompleteFieldObjects(t *testing.T) {
	var body map[string]any
	fakeSFMC(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/data/v1/customObjects", func(w http.ResponseWriter, r *http.Request) {
			b := make([]byte, 8192)
			n, _ := r.Body.Read(b)
			_ = json.Unmarshal(b[:n], &body)
			_, _ = w.Write([]byte(`{"id":"new-1","key":"NEW_DE","name":"NEW_DE"}`))
		})
	})
	// gate: no --write refused
	if code, _, _ := run(t, "de", "create", "NEW_DE", "--field", "A:Text(10)", "--category", "1"); code != exitUsage {
		t.Fatalf("no --write must refuse")
	}
	// missing --category refused
	if code, out, _ := run(t, "de", "create", "NEW_DE", "--field", "A:Text(10)", "--write"); code != exitUsage {
		t.Fatalf("missing --category must refuse: out=%s", out)
	}
	code, out, errOut := run(t, "de", "create", "NEW_DE",
		"--field", "Email:EmailAddress(254)", "--field", "Name:Text(100)",
		"--pk", "Email", "--category", "5", "--write")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s err=%s", code, out, errOut)
	}
	fields := body["fields"].([]any)
	if len(fields) != 2 {
		t.Fatalf("2 fields expected: %v", body)
	}
	f0 := fields[0].(map[string]any)
	// wire assertions: COMPLETE field object (the endpoint dribbles one
	// missing property per 400 otherwise)
	for _, k := range []string{"name", "type", "length", "isNullable", "isPrimaryKey",
		"isHidden", "isInheritable", "isOverridable", "isReadOnly", "isTemplateField",
		"mustOverride", "ordinal", "storageType", "maskType", "description"} {
		if _, ok := f0[k]; !ok {
			t.Fatalf("field object missing %q (incomplete objects are rejected live): %v", k, f0)
		}
	}
	if f0["isPrimaryKey"] != true || f0["isNullable"] != false {
		t.Fatalf("pk field flags wrong: %v", f0)
	}
	if _, has := f0["maxLength"]; has {
		t.Fatalf("must use length, not maxLength (live: misleading error): %v", f0)
	}
}
