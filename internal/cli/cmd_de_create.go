package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/dawidmachon/mcecli/internal/output"
)

// de create — curated DE creation. The raw endpoint rejects incomplete
// field objects one property at a time (verified: type → isNullable →
// ordinal → isTemplateField → isHidden → isReadOnly, then rejects
// maxLength in favor of length with a misleading error). This command
// builds the complete field object from a compact spec.

const usageDeCreate = `mcecli de create <name> — create a data extension (gated write)

  mcecli de create <name> --field SPEC [--field SPEC ...] --category ID --write
               [--key KEY]      custom key (defaults to name)
               [--description D]
               [--pk NAME]      primary key field (repeatable)

  SPEC: Name:Type(length[,scale])  — types: Text, Number, Date, Boolean,
        Decimal, EmailAddress, Phone. Examples:
          Email:EmailAddress(254)   Name:Text(100)   Amount:Decimal(18,2)
          Created:Date              IsOptIn:Boolean   Counter:Number

categoryId is required (folder id) — find folders with:
  mcecli folders --type dataextension
(or reuse the categoryId shown by: mcecli de get <existingKey> --json)
`

// fieldSpec is one parsed --field argument.
// fieldList collects repeated flags.
type fieldList []string

func (m *fieldList) String() string { return strings.Join(*m, ", ") }
func (m *fieldList) Set(v string) error {
	*m = append(*m, v)
	return nil
}

type fieldSpec struct {
	Name   string
	Type   string
	Length int
	Scale  int
	HasLen bool
}

// parseFieldSpec parses Name:Type(length[,scale]).
func parseFieldSpec(spec string) (fieldSpec, error) {
	var fs fieldSpec
	parts := strings.SplitN(spec, ":", 2)
	if len(parts) != 2 || parts[0] == "" {
		return fs, fmt.Errorf("field spec must be Name:Type — got %q", spec)
	}
	fs.Name = strings.TrimSpace(parts[0])
	rest := strings.TrimSpace(parts[1])
	if i := strings.IndexAny(rest, "("); i >= 0 {
		if !strings.HasSuffix(rest, ")") {
			return fs, fmt.Errorf("field spec %q: unbalanced parentheses", spec)
		}
		fs.Type = rest[:i]
		dims := strings.TrimSuffix(rest[i+1:], ")")
		d := strings.Split(dims, ",")
		l, err := strconv.Atoi(strings.TrimSpace(d[0]))
		if err != nil {
			return fs, fmt.Errorf("field spec %q: bad length %q", spec, d[0])
		}
		fs.Length, fs.HasLen = l, true
		if len(d) == 2 {
			sc, err := strconv.Atoi(strings.TrimSpace(d[1]))
			if err != nil {
				return fs, fmt.Errorf("field spec %q: bad scale %q", spec, d[1])
			}
			fs.Scale = sc
		}
		if len(d) > 2 {
			return fs, fmt.Errorf("field spec %q: too many dimensions", spec)
		}
	} else {
		fs.Type = rest
	}
	return fs, nil
}

// fieldObject builds the COMPLETE field object the create endpoint demands
// (verified live: missing booleans are rejected one at a time; the length
// property is "length", NOT "maxLength").
func fieldObject(fs fieldSpec, ordinal int, pk bool) map[string]any {
	o := map[string]any{
		"name":            fs.Name,
		"type":            fs.Type,
		"isNullable":      !pk,
		"isPrimaryKey":    pk,
		"isHidden":        false,
		"isInheritable":   true,
		"isOverridable":   true,
		"isReadOnly":      false,
		"isTemplateField": false,
		"mustOverride":    false,
		"ordinal":         ordinal,
		"storageType":     "Plain",
		"maskType":        "None",
		"description":     "",
	}
	if fs.HasLen {
		o["length"] = fs.Length
	}
	if fs.Scale != 0 {
		o["scale"] = fs.Scale
	}
	return o
}

func cmdDECreate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("de create", flag.ContinueOnError)
	var c common
	var write bool
	var key, desc, category string
	var fields fieldList
	var pks fieldList
	addCommon(fs, &c)
	fs.Var(&fields, "field", "field spec Name:Type(length[,scale]) — repeatable")
	fs.Var(&pks, "pk", "primary key field name — repeatable")
	fs.StringVar(&key, "key", "", "custom key (defaults to name)")
	fs.StringVar(&desc, "description", "", "DE description")
	fs.StringVar(&category, "category", "", "folder id (required)")
	fs.BoolVar(&write, "write", false, "confirm the write")
	if err := parseCmd(fs, args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprint(stderr, usageDeCreate)
		return exitUsage
	}
	name := fs.Arg(0)
	if !write {
		e := output.Fail(0, "refusing de create without --write",
			"creating a data extension changes the org — retry with --write")
		_ = output.Print(e, c.pretty, stdout)
		return exitUsage
	}
	if len(fields) == 0 {
		e := output.Fail(0, "at least one --field is required", usageDeCreate)
		_ = output.Print(e, c.pretty, stdout)
		return exitUsage
	}
	if category == "" {
		e := output.Fail(0, "--category (folder id) is required by the platform",
			"mcecli folders --type dataextension — or reuse the categoryId from an existing DE")
		_ = output.Print(e, c.pretty, stdout)
		return exitUsage
	}

	specs := make([]fieldSpec, 0, len(fields))
	seen := map[string]bool{}
	for _, spec := range fields {
		fsp, err := parseFieldSpec(spec)
		if err != nil {
			e := output.Fail(0, err.Error(), usageDeCreate)
			_ = output.Print(e, c.pretty, stdout)
			return exitUsage
		}
		if seen[strings.ToLower(fsp.Name)] {
			e := output.Fail(0, fmt.Sprintf("duplicate field name %q", fsp.Name), "")
			_ = output.Print(e, c.pretty, stdout)
			return exitUsage
		}
		seen[strings.ToLower(fsp.Name)] = true
		specs = append(specs, fsp)
	}
	pkSet := map[string]bool{}
	for _, pk := range pks {
		if !seen[strings.ToLower(pk)] {
			e := output.Fail(0, fmt.Sprintf("--pk %q is not among the declared fields", pk), fieldNames(specs))
			_ = output.Print(e, c.pretty, stdout)
			return exitUsage
		}
		pkSet[strings.ToLower(pk)] = true
	}

	objs := make([]map[string]any, 0, len(specs))
	for i, fsp := range specs {
		objs = append(objs, fieldObject(fsp, i, pkSet[strings.ToLower(fsp.Name)]))
	}

	if key == "" {
		key = name
	}
	body := map[string]any{
		"name":        name,
		"key":         key,
		"description": desc,
		"categoryId":  category,
		"fields":      objs,
	}
	bodyBytes, _ := json.Marshal(body)

	s, env, code := newSession(&c)
	if env != nil {
		_ = output.Print(env, c.pretty, stdout)
		return code
	}
	st, resp, env2, _ := s.call(http.MethodPost, "data/v1/customObjects", bodyBytes, nil)
	if env2 != nil {
		_ = output.Print(env2, c.pretty, stdout)
		return exitAPI
	}
	if st < 200 || st > 299 {
		msg := strings.TrimSpace(string(resp))
		if m, ok := parseJSON(resp).(map[string]any); ok {
			msg = str(m, "message")
		}
		e := output.Fail(st, "de create failed: "+msg,
			"field objects must be complete — this command builds them; check --category exists")
		_ = output.Print(e, c.pretty, stdout)
		return exitAPI
	}
	var created map[string]any
	_ = json.Unmarshal(resp, &created)
	if created == nil {
		created = map[string]any{}
	}
	e := output.OK(st, created)
	e.Hint = "verify schema: mcecli de get " + key + " — load rows: mcecli de add " + key + " --data @rows.ndjson --write"
	_ = output.Print(e, c.pretty, stdout)
	return exitOK
}

func fieldNames(specs []fieldSpec) string {
	names := make([]string, 0, len(specs))
	for _, fsp := range specs {
		names = append(names, fsp.Name)
	}
	return strings.Join(names, ", ")
}
