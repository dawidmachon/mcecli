package cli

import (
	"context"
	"flag"
	"io"
	"strings"

	"github.com/dawidmachon/mcecli/internal/output"
	"github.com/dawidmachon/mcecli/internal/soap"
)

// DataFolder reads (SOAP). VERIFIED live: ContentType filter WORKS
// server-side (rare). ParentFolderID NOT retrievable on all tenants.
// Use to find categoryId values required by de create / query create.

const usageFolders = `mcecli folders — content folder reads (read-only, SOAP)

  mcecli folders                     — all folders (many content types)
  mcecli folders --type T            — one content type (server-side filter)
        e.g.: dataextension, queryactivity, ssjsactivity, publication, …
        (query create typically needs the queryactivity folder)

Gives the categoryId values required by de create / query create.
`

func cmdFolders(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("folders", flag.ContinueOnError)
	var c common
	var typeFilter string
	var limit int
	addCommon(fs, &c)
	fs.StringVar(&typeFilter, "type", "", "ContentType filter (server-side; e.g. dataextension, queryactivity)")
	fs.IntVar(&limit, "limit", 250, "max rows")
	if err := parseCmd(fs, args); err != nil {
		return exitUsage
	}

	s, env, code := newSession(&c)
	if env != nil {
		_ = output.Print(env, c.pretty, stdout)
		return code
	}
	opts := soap.Opts{MaxRows: limit}
	if typeFilter != "" {
		opts.Filters = []soap.Filter{{Prop: "ContentType", Op: "equals", Value: typeFilter}}
	}
	rows, status, err := soap.Retrieve(context.Background(), s.res.SoapURL(), s.tok.AccessToken,
		"DataFolder", []string{"ID", "Name", "ContentType"}, opts)
	if err != nil {
		e := output.Fail(0, err.Error(), "")
		_ = output.Print(e, c.pretty, stdout)
		return exitAPI
	}
	if strings.HasPrefix(status, "Error") {
		e := output.Fail(0, "SOAP OverallStatus: "+status, "check --type value (dataextension, queryactivity, …)")
		_ = output.Print(e, c.pretty, stdout)
		return exitAPI
	}
	// extra client-side type match for the no-filter case convenience
	out := make([]map[string]string, 0, len(rows))
	for _, r := range rows {
		if typeFilter != "" && !strings.EqualFold(r["ContentType"], typeFilter) {
			continue
		}
		out = append(out, r)
	}
	if out == nil {
		out = []map[string]string{}
	}
	e := output.OK(200, out)
	e.Count = len(out)
	e.Hint = "use these IDs as --category on de create / query create"
	_ = output.Print(e, c.pretty, stdout)
	return exitOK
}
