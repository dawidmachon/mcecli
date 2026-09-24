package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/dawidmachon/mcecli/internal/output"
	"github.com/dawidmachon/mcecli/internal/soap"
)

const usageDEField = `mcecli de field — field operations on an existing DE (gated)

  mcecli de field add <deKey> --field SPEC [--field SPEC ...] --write
               [--required NAME]   — repeatable; required = NOT nullable
               [--confirm]         — extra confirmation for schema changes

  SPEC: Name:Type(length[,scale]) — same as de create
  Adding a column uses SOAP UpdateRequest (REST PATCH silently ignores
  new fields — verified live). Schema changes apply immediately.

Existing schema: mcecli de get <deKey>
`

func cmdDEField(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "add" {
		fmt.Fprint(stderr, usageDEField)
		return exitUsage
	}
	fs := flag.NewFlagSet("de field add", flag.ContinueOnError)
	var c common
	var write, confirm bool
	var fields fieldList
	var required multiFlag
	addCommon(fs, &c)
	fs.Var(&fields, "field", "field spec Name:Type(length[,scale]) — repeatable")
	fs.Var(&required, "required", "field name that must be NOT nullable — repeatable")
	fs.BoolVar(&write, "write", false, "confirm the write")
	fs.BoolVar(&confirm, "confirm", false, "confirm schema change to an existing DE")
	if err := parseCmd(fs, args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 2 {
		fmt.Fprint(stderr, usageDEField)
		return exitUsage
	}
	deKey := fs.Arg(1)
	if !write || !confirm {
		e := output.Fail(0, "refusing de field add without --write --confirm",
			"this changes the schema of an existing DE — retry with both flags")
		_ = output.Print(e, c.pretty, stdout)
		return exitUsage
	}
	if len(fields) == 0 {
		e := output.Fail(0, "at least one --field is required", usageDEField)
		_ = output.Print(e, c.pretty, stdout)
		return exitUsage
	}

	reqSet := map[string]bool{}
	for _, r := range required.items {
		reqSet[strings.ToLower(r)] = true
	}
	deFields := make([]soap.DEField, 0, len(fields))
	for _, spec := range fields {
		fsp, err := parseFieldSpec(spec)
		if err != nil {
			e := output.Fail(0, err.Error(), usageDEField)
			_ = output.Print(e, c.pretty, stdout)
			return exitUsage
		}
		req := reqSet[strings.ToLower(fsp.Name)]
		deFields = append(deFields, soap.DEField{
			Name:       fsp.Name,
			Type:       fsp.Type,
			MaxLength:  fsp.Length,
			Scale:      fsp.Scale,
			IsRequired: req,
			Nullable:   !req,
		})
	}

	s, env, code := newSession(&c)
	if env != nil {
		_ = output.Print(env, c.pretty, stdout)
		return code
	}
	msg, err := soap.UpdateDEFields(context.Background(), s.res.SoapURL(), s.tok.AccessToken, deKey, deFields)
	if err != nil {
		e := output.Fail(0, err.Error(), "verify the DE key: mcecli de get "+deKey)
		_ = output.Print(e, c.pretty, stdout)
		return exitAPI
	}
	e := output.OK(200, map[string]any{"de": deKey, "added": fieldNamesDe(deFields), "result": msg})
	e.Hint = "verify: mcecli de get " + deKey
	_ = output.Print(e, c.pretty, stdout)
	return exitOK
}

// fieldNamesDe joins DEField names.
func fieldNamesDe(fs []soap.DEField) string {
	names := make([]string, 0, len(fs))
	for _, f := range fs {
		names = append(names, f.Name)
	}
	return strings.Join(names, ", ")
}
