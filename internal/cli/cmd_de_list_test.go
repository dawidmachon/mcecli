// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"net/http"
	"strings"
	"testing"
)

// Agent-feedback round 3: $search is REQUIRED even when categoryId narrows
// results (platform 400s on categoryId alone — it is an AND filter, not an
// alternative). The client must fail fast with the TRUE contract instead of
// forwarding a doomed request.

func TestDeListWithoutSearchFailsFastEvenWithCategory(t *testing.T) {
	fakeSFMC(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/data/v1/customObjects", func(w http.ResponseWriter, r *http.Request) {
			t.Errorf("request must not reach the platform: categoryId cannot stand alone")
		})
	})
	code, out, _ := run(t, "de", "list", "--category", "123")
	if code != exitUsage {
		t.Fatalf("expected usage exit 2, got %d out=%s", code, out)
	}
	e := envelope(t, out)
	h, _ := e["hint"].(string)
	if !strings.Contains(h, "AND filter") || !strings.Contains(h, "--search") {
		t.Fatalf("hint must state the true contract ($search required, --category is AND): %q", h)
	}
}

func TestDeListBareHintStatesTrueContract(t *testing.T) {
	fakeSFMC(t, nil)
	code, out, _ := run(t, "de", "list")
	if code != exitUsage {
		t.Fatalf("expected usage exit 2, got %d out=%s", code, out)
	}
	e := envelope(t, out)
	if h, _ := e["hint"].(string); strings.Contains(h, "or --category") {
		t.Fatalf("hint must not offer --category as an OR-alternative: %q", h)
	}
}
