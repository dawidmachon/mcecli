// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package httpc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func fastBackoff() (restore func()) {
	old := Backoff
	Backoff = func(int) time.Duration { return time.Millisecond }
	return func() { Backoff = old }
}

func TestRetriesOn500(t *testing.T) {
	defer fastBackoff()()
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	res, err := Do(context.Background(), http.MethodGet, srv.URL, "", nil, nil)
	if err != nil || res.Status != http.StatusOK || n != 3 {
		t.Fatalf("res=%+v err=%v n=%d", res, err, n)
	}
}

func TestRetriesOn429(t *testing.T) {
	defer fastBackoff()()
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	res, err := Do(context.Background(), http.MethodGet, srv.URL, "", nil, nil)
	if err != nil || res.Status != http.StatusOK || n != 2 {
		t.Fatalf("res=%+v err=%v n=%d", res, err, n)
	}
}

func TestGivesUpAfterMaxAttempts(t *testing.T) {
	defer fastBackoff()()
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	res, err := Do(context.Background(), http.MethodGet, srv.URL, "", nil, nil)
	if err != nil {
		t.Fatalf("HTTP errors are data, not errors: %v", err)
	}
	if res.Status != http.StatusInternalServerError {
		t.Fatalf("status=%d", res.Status)
	}
	if n != maxAttempts {
		t.Fatalf("expected %d attempts, got %d", maxAttempts, n)
	}
}

func TestTransportErrorIsError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close() // closed -> connection refused

	if _, err := Do(context.Background(), http.MethodGet, srv.URL, "", nil, nil); err == nil {
		t.Fatal("transport failure must return an error")
	}
}

func TestBearerAndHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer XYZ" {
			t.Errorf("auth=%q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("X-Test") != "1" {
			t.Errorf("custom header missing")
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	_, err := Do(context.Background(), http.MethodGet, srv.URL, "XYZ", nil, map[string]string{"X-Test": "1"})
	if err != nil {
		t.Fatal(err)
	}
}
