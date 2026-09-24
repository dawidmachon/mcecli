// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package output renders the agent-facing JSON envelope and implements field
// projection (--fields) used to keep responses small.
package output

import (
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"

	"github.com/dawidmachon/mcecli/internal/config"
)

// ErrObj is the structured error inside an envelope.
type ErrObj struct {
	Message   string `json:"message"`
	ErrorCode string `json:"errorcode,omitempty"`
}

// Envelope is the stable response shape for every mcecli command.
// Agents learn this once: ok / status / count / next / hint / error / data.
// Changing field names is a breaking (MAJOR) change.
type Envelope struct {
	OK      bool    `json:"ok"`
	Status  int     `json:"status,omitempty"`
	Count   int     `json:"count,omitempty"`
	Next    string  `json:"next,omitempty"` // continuation cursor (usually next page number)
	Hint    string  `json:"hint,omitempty"` // actionable fix for agents on failure
	Error   *ErrObj `json:"error,omitempty"`
	Data    any     `json:"data,omitempty"`
	Context any     `json:"context,omitempty"` // profile/mid/bu/session/prod — self-describing calls
	Warning string  `json:"warning,omitempty"` // non-blocking risk flag (PROD-named profile)
}

// OK builds a success envelope; array data auto-populates count.
// Handles []any and any concrete slice type (e.g. []map[string]string)
// so callers always get a correct count regardless of element type.
func OK(status int, data any) *Envelope {
	e := &Envelope{OK: true, Status: status, Data: data}
	if data != nil {
		rv := reflect.ValueOf(data)
		if rv.Kind() == reflect.Slice {
			e.Count = rv.Len()
		}
	}
	return e
}

// Fail builds an error envelope.
func Fail(status int, msg, hint string) *Envelope {
	return &Envelope{OK: false, Status: status, Error: &ErrObj{Message: msg}, Hint: hint}
}

// JSONLMode: when true, Print emits only the data items as newline-delimited
// JSON (one object per line) instead of an envelope — piping mode for
// hosts without jq. Errors still print as envelopes on stderr? No: errors
// keep their envelope on stdout with ok:false; JSONL applies to success
// payloads with slice data only.
var JSONLMode bool

// Print writes the envelope: compact JSON by default, pretty with --pretty.
func Print(e *Envelope, pretty bool, w io.Writer) error {
	ctx := config.CurrentContext()
	e.Context = ctx
	if ctx.Prod {
		e.Warning = "production-named profile in use — writes stay gated"
	}
	if JSONLMode && e.OK {
		// slice-ish data → one JSON object per line (piping without jq)
		switch items := e.Data.(type) {
		case []any:
			for _, it := range items {
				line, merr := json.Marshal(it)
				if merr != nil {
					return merr
				}
				if _, werr := fmt.Fprintln(w, string(line)); werr != nil {
					return werr
				}
			}
			return nil
		case []map[string]any:
			for _, it := range items {
				line, merr := json.Marshal(it)
				if merr != nil {
					return merr
				}
				if _, werr := fmt.Fprintln(w, string(line)); werr != nil {
					return werr
				}
			}
			return nil
		case []map[string]string:
			for _, it := range items {
				line, merr := json.Marshal(it)
				if merr != nil {
					return merr
				}
				if _, werr := fmt.Fprintln(w, string(line)); werr != nil {
					return werr
				}
			}
			return nil
		}
		// non-slice data falls through to the envelope
	}
	var b []byte
	var err error
	if pretty {
		b, err = json.MarshalIndent(e, "", "  ")
	} else {
		b, err = json.Marshal(e)
	}
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(b))
	return err
}

// Project keeps only the given fields. Paths may be dotted ("columns.name");
// dotted results are flattened ("columns.name" -> key "columns.name").
// Works on objects, arrays of objects; anything else passes through.
func Project(v any, fields []string) any {
	if len(fields) == 0 {
		return v
	}
	switch t := v.(type) {
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = Project(item, fields)
		}
		return out
	case map[string]any:
		out := map[string]any{}
		for _, f := range fields {
			if got, ok := lookup(t, f); ok {
				out[f] = got
			}
		}
		return out
	default:
		return v
	}
}

func lookup(m map[string]any, path string) (any, bool) {
	cur := any(m)
	parts := strings.Split(path, ".")
	for i, p := range parts {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		v, ok := mm[p]
		if !ok {
			return nil, false
		}
		if i == len(parts)-1 {
			return v, true
		}
		cur = v
	}
	return nil, false
}
