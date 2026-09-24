// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"strings"
	"testing"
)

func TestDescribeObjectShowsVerifiedAndDocsProps(t *testing.T) {
	code, out, _ := run(t, "describe", "SentEvent")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	e := envelope(t, out)
	d := e["data"].(map[string]any)
	if _, ok := d["props_verified"]; !ok {
		t.Fatalf("props_verified expected: %v", e)
	}
	if _, ok := d["quirks"]; !ok {
		t.Fatalf("quirks expected for SentEvent: %v", e)
	}
}

func TestDescribeUnknownRefuses(t *testing.T) {
	code, out, _ := run(t, "describe", "BogusObject")
	if code != exitUsage {
		t.Fatalf("unknown object must refuse: exit=%d out=%s", code, out)
	}
	if !strings.Contains(out, "mcecli describe") {
		t.Fatalf("refusal must teach: %s", out)
	}
}

func TestDescribeListAll(t *testing.T) {
	code, out, _ := run(t, "describe")
	if code != exitOK {
		t.Fatalf("exit=%d", code)
	}
	e := envelope(t, out)
	if e["count"].(float64) < 10 {
		t.Fatalf("catalog should have 10+ objects: %v", e["count"])
	}
}
