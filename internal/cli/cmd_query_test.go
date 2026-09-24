// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
)

// Query live bugs these tests lock (found & fixed 2026-09-16):
//  1. resolveQueryQID read only page 1 — queries beyond the server's 25-item
//     page cap were "not found" (context had 292 queries).
//  2. query start was POSTed a JSON body — live SFMC silently rejects that;
//     the endpoint wants an EMPTY body (wire assertion below).
//  3. a failed start (non-2xx) fell through into polling — fail fast instead.
//  4. polling read "status" off the definition endpoint, which carries no
//     status at all — poll /actions/isrunning instead.

type queryFake struct {
	startStatus int   // HTTP status the fake start endpoint returns
	polls       int32 // isrunning calls before isRunning goes false
	pollCount   atomic.Int32
	startBodies []string // captured start request bodies (wire assertion)
}

func (q *queryFake) routes(mux *http.ServeMux) {
	mux.HandleFunc("/automation/v1/queries", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("$page") == "2" {
			_, _ = w.Write([]byte(`{"count":26,"page":2,"pageSize":25,"items":[{"key":"MCECLI_BIG","name":"MCECLI_BIG","queryDefinitionId":"qid-2","targetKey":"MCECLI_TARGET"}]}`))
			return
		}
		// page 1: 25 filler queries, target NOT among them
		items := `[`
		for i := 0; i < 25; i++ {
			if i > 0 {
				items += `,`
			}
			items += fmt.Sprintf(`{"key":"filler-%02d","queryDefinitionId":"qid-f%d"}`, i, i)
		}
		items += `]`
		_, _ = w.Write([]byte(`{"count":26,"page":1,"pageSize":25,"items":` + items + `}`))
	})
	mux.HandleFunc("/automation/v1/queries/qid-2", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"key":"MCECLI_BIG","name":"MCECLI_BIG","targetKey":"MCECLI_TARGET"}`))
	})
	mux.HandleFunc("/automation/v1/queries/qid-2/actions/start", func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 64)
		n, _ := r.Body.Read(buf)
		q.startBodies = append(q.startBodies, string(buf[:n]))
		if q.startStatus != http.StatusOK && q.startStatus != http.StatusAccepted {
			w.WriteHeader(q.startStatus)
			_, _ = w.Write([]byte(`{"message":"boom from fake"}`))
			return
		}
		if n > 0 {
			// live behavior: non-empty start body → server error
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`Internal Server Error`))
			return
		}
		_, _ = w.Write([]byte(`"OK"`))
	})
	mux.HandleFunc("/automation/v1/queries/qid-2/actions/isrunning", func(w http.ResponseWriter, r *http.Request) {
		if q.pollCount.Add(1) <= q.polls {
			_, _ = w.Write([]byte(`{"queryDefinitionId":"qid-2","isRunning":true}`))
			return
		}
		_, _ = w.Write([]byte(`{"queryDefinitionId":"qid-2","isRunning":false}`))
	})
	mux.HandleFunc("/automation/v1/queries/qid-2/log", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"count":0,"items":[]}`))
	})
}

func TestQueryRunPagesPastItem25(t *testing.T) {
	qf := &queryFake{startStatus: http.StatusOK, polls: 1}
	fakeSFMC(t, qf.routes)
	code, out, errOut := run(t, "query", "run", "MCECLI_BIG", "--write", "--confirm", "--poll", "1")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s err=%s", code, out, errOut)
	}
	e := envelope(t, out)
	d := e["data"].(map[string]any)
	if d["status"] != "Complete" || d["target"] != "MCECLI_TARGET" {
		t.Fatalf("bad run result: %v", e)
	}
	if len(qf.startBodies) != 1 || qf.startBodies[0] != "" {
		t.Fatalf("start must carry an EMPTY body (live SFMC rejects JSON): %q", qf.startBodies)
	}
}

func TestQueryRunFailedStartFailsFast(t *testing.T) {
	qf := &queryFake{startStatus: http.StatusBadRequest}
	fakeSFMC(t, qf.routes)
	code, out, _ := run(t, "query", "run", "MCECLI_BIG", "--write", "--confirm", "--poll", "1")
	if code != exitAPI {
		t.Fatalf("failed start must fail fast: exit=%d out=%s", code, out)
	}
	e := envelope(t, out)
	if e["ok"] != false {
		t.Fatalf("error envelope expected: %v", e)
	}
	if qf.pollCount.Load() != 0 {
		t.Fatalf("must not poll after a failed start (polled %d times)", qf.pollCount.Load())
	}
}

func TestQueryStatusUsesIsrunning(t *testing.T) {
	qf := &queryFake{startStatus: http.StatusOK, polls: 0}
	fakeSFMC(t, qf.routes)
	code, out, _ := run(t, "query", "status", "MCECLI_BIG")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	d := dataMap(t, out)
	if d["status"] != "not running (never started or finished)" {
		t.Fatalf("status must come from /actions/isrunning: %v", d)
	}
}

func TestQueryRunTargetFlagRefused(t *testing.T) {
	// The start endpoint takes no body — a --target override would silently
	// run against the SAVED target. Must refuse, not lie.
	qf := &queryFake{startStatus: http.StatusOK, polls: 0}
	fakeSFMC(t, qf.routes)
	code, out, _ := run(t, "query", "run", "MCECLI_BIG", "--write", "--confirm", "--target", "OTHER_DE")
	if code != exitUsage {
		t.Fatalf("--target must refuse: exit=%d out=%s", code, out)
	}
	e := envelope(t, out)
	if msg, _ := e["error"].(map[string]any); msg == nil {
		t.Fatalf("error envelope expected: %v", e)
	}
	if qf.pollCount.Load() != 0 {
		t.Fatalf("refusal must not start anything")
	}
}

func TestQueryValidate(t *testing.T) {
	fakeSFMC(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/automation/v1/queries/actions/validate", func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			// wire assertion: field is "Text" (NOT queryText) + targetKey
			if body["Text"] == "" || body["targetKey"] != "MCECLI_TARGET" {
				t.Errorf("validate body wrong: %v", body)
			}
			if body["queryText"] != nil {
				t.Errorf("validate must use Text, not queryText: %v", body)
			}
			_, _ = w.Write([]byte(`{"queryValid":false,"errors":[{"message":"bad view"}],"warnings":[]}`))
		})
	})
	// gate REMOVED: validate is side-effect-free (agent feedback 2026-09-18)
	code, out, _ := run(t, "query", "validate", "--text", "SELECT 1", "--target", "MCECLI_TARGET")
	if code != exitOK {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	e := envelope(t, out)
	d := e["data"].(map[string]any)
	if d["queryValid"] != false || d["errors"] == nil {
		t.Fatalf("validate result wrong: %v", e)
	}
}
