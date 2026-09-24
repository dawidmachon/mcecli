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

// AccountUser operational reads (SOAP). VERIFIED live: ID/UserID/Name/
// Email/ActiveFlag/DefaultBusinessUnit/Delete all retrievable. No
// server-side filter — client-side --search over paged retrieves.

const usageUsers = `mcecli users — platform user reads (read-only, SOAP)

  mcecli users list           — platform users (in the parent context)
               [--search STR] — client-side filter on name/email
               [--limit N]    — max rows scanned (default 2500; 1 page)
`

func cmdUsers(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "list" || args[0] == "help" || args[0] == "-h" {
		if len(args) > 0 && (args[0] == "help" || args[0] == "-h") {
			fmt.Fprint(stdout, usageUsers)
			return exitOK
		}
		fs := flag.NewFlagSet("users list", flag.ContinueOnError)
		var c common
		var search string
		var limit int
		addCommon(fs, &c)
		fs.StringVar(&search, "search", "", "client-side filter on name+email")
		fs.IntVar(&limit, "limit", 2500, "max rows scanned (server pages 2500/request)")
		if err := parseCmd(fs, args); err != nil {
			return exitUsage
		}
		s, env, code := newSession(&c)
		if env != nil {
			_ = output.Print(env, c.pretty, stdout)
			return code
		}
		rows, status, err := soap.Retrieve(context.Background(), s.res.SoapURL(), s.tok.AccessToken,
			"AccountUser",
			[]string{"ID", "UserID", "Name", "Email", "ActiveFlag", "DefaultBusinessUnit"},
			soap.Opts{MaxRows: limit})
		if err != nil {
			e := output.Fail(0, err.Error(), "")
			_ = output.Print(e, c.pretty, stdout)
			return exitAPI
		}
		if strings.HasPrefix(status, "Error") {
			e := output.Fail(0, "SOAP OverallStatus: "+status, "")
			_ = output.Print(e, c.pretty, stdout)
			return exitAPI
		}
		out := make([]map[string]string, 0, len(rows))
		for _, r := range rows {
			if search != "" {
				needle := strings.ToLower(search)
				if !strings.Contains(strings.ToLower(r["Name"]), needle) &&
					!strings.Contains(strings.ToLower(r["Email"]), needle) {
					continue
				}
			}
			out = append(out, r)
		}
		if out == nil {
			out = []map[string]string{}
		}
		e := output.OK(200, out)
		e.Count = len(out)
		e.Hint = fmt.Sprintf("scanned %d user rows — DefaultBusinessUnit shows the user's home MID", len(rows))
		_ = output.Print(e, c.pretty, stdout)
		return exitOK
	}
	fmt.Fprintf(stderr, "unknown 'users' subcommand %q\n\n%s", args[0], usageUsers)
	return exitUsage
}
