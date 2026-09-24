// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package httpc is a minimal HTTP helper: JSON content types, bearer auth,
// bounded retries with backoff on 429/5xx, and non-2xx responses returned as
// data (not errors) so callers can shape agent-facing error messages.
package httpc

import (
	"bytes"
	"context"
	"errors"
	"github.com/dawidmachon/mcecli/internal/config"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// authRedact keeps credentials out of debug dumps: shows only a length hint.
func authRedact(bearer string) string {
	if bearer == "" {
		return "auth: (none)"
	}
	return "auth: bearer (len " + strconv.Itoa(len(bearer)) + ")"
}

var client = &http.Client{Timeout: 90 * time.Second}

// Backoff is the sleep before retry attempt i (0-based). Overridable in tests.
var Backoff = func(i int) time.Duration {
	return time.Duration(250*(1<<i)) * time.Millisecond // 250ms, 500ms, 1s
}

const maxAttempts = 4

// Result is a completed HTTP exchange. Any HTTP status is a Result; only
// transport-level failures return an error.
type Result struct {
	Status     int
	Body       []byte
	RetryAfter int // seconds the server asked us to wait (from 429 responses)
}

// Do performs the request, retrying 429/5xx up to maxAttempts total attempts.
// Retry-After (seconds) is honored, capped at 10s (SFMC rate-limit best practice).
func Do(ctx context.Context, method, url, bearer string, body []byte, headers map[string]string) (*Result, error) {
	var lastErr error
	var res *Result
	var retryDelay time.Duration

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			d := retryDelay
			if d == 0 {
				d = Backoff(attempt - 1)
			}
			retryDelay = 0
			select {
			case <-time.After(d):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		var rdr io.Reader
		if body != nil {
			rdr = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, url, rdr)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}

		if p := config.EnvGet("MCECLI_REST_DEBUG"); p != "" {
			// add PID so concurrent processes don't overwrite each other's dump
			_ = os.WriteFile(p+"."+strconv.Itoa(os.Getpid()), []byte(method+" "+url+"\n"+authRedact(bearer)+"\n"+string(body)), 0o600)
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		b, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}
		res = &Result{Status: resp.StatusCode, Body: b}
		if resp.StatusCode == http.StatusTooManyRequests {
			res.RetryAfter = int(retryAfter(resp).Seconds())
		}

		if (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500) && attempt < maxAttempts-1 {
			if ra := retryAfter(resp); ra > 0 {
				retryDelay = ra
			}
			continue
		}
		return res, nil
	}

	if lastErr != nil {
		return nil, lastErr
	}
	if res != nil {
		return res, nil
	}
	return nil, errors.New("request failed")
}

func retryAfter(resp *http.Response) time.Duration {
	v := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0
	}
	if n > 10 {
		n = 10
	}
	return time.Duration(n) * time.Second
}
