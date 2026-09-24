// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package cli contains shared utilities used across multiple command files.
// These are kept here to avoid duplication and ensure consistent behavior.
package cli

import (
	"encoding/json"
	"strings"
)

// parseJSON parses JSON, returning nil on failure.
// Shared by: cmd_de.go, cmd_deliver.go.
func parseJSON(body []byte) any {
	if len(body) == 0 {
		return nil
	}
	var v any
	if json.Unmarshal(body, &v) == nil {
		return v
	}
	return nil
}

// mergeRowItems flattens SFMC rowset items [{keys,values}] into single maps.
// Shared by: cmd_de.go, cmd_deliver.go.
func mergeRowItems(items []any) []map[string]any {
	if items == nil {
		return nil
	}
	rows := make([]map[string]any, 0, len(items))
	for _, it := range items {
		im, ok := it.(map[string]any)
		if !ok {
			continue
		}
		row := make(map[string]any)
		if keys, ok := im["keys"].(map[string]any); ok {
			for k, v := range keys {
				row["(key) "+k] = v
			}
		}
		if vals, ok := im["values"].(map[string]any); ok {
			for k, v := range vals {
				row[k] = v
			}
		}
		if len(row) > 0 {
			rows = append(rows, row)
		}
	}
	return rows
}

// resolveNext converts a section-relative links.next ("/v1/...") into a
// passthrough path usable by mcecli ("data/v1/...").
// Shared by: cmd_de.go, cmd_deliver.go.
func resolveNext(nl string) string {
	switch {
	case strings.HasPrefix(nl, "/v1/"):
		return "data" + nl
	case !strings.Contains(nl, "/v1/"):
		return "data/v1/" + strings.TrimPrefix(nl, "/")
	default:
		return strings.TrimPrefix(nl, "/")
	}
}

// countItemsIn reports an SFMC collection size from count or items length.
// Shared by: cmd_deliver.go (also inlined in deDump).
func countItemsIn(body []byte) int {
	if m, ok := parseJSON(body).(map[string]any); ok {
		if n, ok := m["count"].(float64); ok {
			return int(n)
		}
		if items, ok := m["items"].([]any); ok {
			return len(items)
		}
	}
	return 0
}

// splitFields parses "a,b.c" -> ["a","b.c"].
func splitFields(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
