// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package journal

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAppendAndLast(t *testing.T) {
	t.Setenv("MCECLI_HOME", t.TempDir())

	if err := Append(Entry{Profile: "dev", BU: "parent", MID: "111", Method: "PUT",
		URL: "https://x/rows", Status: 202, BodySHA256: HashBody([]byte(`{"a":1}`)),
		BodyLen: 7, RequestID: "r1"}); err != nil {
		t.Fatal(err)
	}
	if err := Append(Entry{Profile: "prod", Method: "POST", URL: "https://x/y", Status: 400,
		ServerError: "bad"}); err != nil {
		t.Fatal(err)
	}

	all, err := Last(10, "")
	if err != nil || len(all) != 2 {
		t.Fatalf("entries=%d err=%v", len(all), err)
	}
	if all[0].Profile != "prod" {
		t.Fatalf("newest first violated: %+v", all[0])
	}
	if all[1].TS == "" || all[1].BodySHA256 == "" || all[1].BodyLen != 7 {
		t.Fatalf("entry fields missing: %+v", all[1])
	}

	onlyDev, _ := Last(10, "DEV") // case-insensitive filter
	if len(onlyDev) != 1 || onlyDev[0].Profile != "dev" {
		t.Fatalf("profile filter failed: %+v", onlyDev)
	}

	if Count() != 2 {
		t.Fatalf("count=%d", Count())
	}
	// no secrets in the stored line
	b, _ := os.ReadFile(Path())
	if strings.Contains(string(b), "Bearer") || strings.Contains(string(b), "access_token") {
		t.Fatal("journal must not contain credentials")
	}
}

func TestJournalFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX perm bits are not enforced on Windows")
	}
	t.Setenv("MCECLI_HOME", t.TempDir())
	if err := Append(Entry{Profile: "p", Method: "PUT", URL: "u"}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(Path())
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("journal must be owner-only, got %v", fi.Mode().Perm())
	}
}

func TestLastEmpty(t *testing.T) {
	t.Setenv("MCECLI_HOME", filepath.Join(t.TempDir(), "missing"))
	got, err := Last(5, "")
	if err != nil || got != nil {
		t.Fatalf("missing journal must yield nil: %v %v", got, err)
	}
}

func TestSkipsCorruptLines(t *testing.T) {
	t.Setenv("MCECLI_HOME", t.TempDir())
	if err := Append(Entry{Profile: "a", Method: "PUT", URL: "u"}); err != nil {
		t.Fatal(err)
	}
	f, _ := os.OpenFile(Path(), os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString("NOT JSON\n")
	f.Close()
	got, err := Last(10, "")
	if err != nil || len(got) != 1 {
		t.Fatalf("corrupt line must be skipped: %v %v", got, err)
	}
}

func TestHashBodyEmpty(t *testing.T) {
	if HashBody(nil) != "" {
		t.Fatal("empty body must hash to empty string")
	}
}

var _ = strings.Contains
var _ = filepath.Join
