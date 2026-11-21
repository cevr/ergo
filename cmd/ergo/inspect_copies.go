package main

import (
	"fmt"
	"os"
	"strings"

	"ergo.local/prototype/internal/sketchfront"
)

const inspectCopiesHelp = `usage: ergo inspect copies FILE|DIR [--json]

Shows where the run build copies a value and where it hands one over
(types.md, Bindings 2; REUSE RU0). First each function of the root package
with owned parameters: a caller whose argument no other binding holds
hands it over, and the function writes it in place. Then one line per
site, in source order, with the function that holds it: each Append and
Concat site's form (in place, one extender, or copy), every other copy the
lowering writes with its kind (slice, Map, or deep: a generated function
that copies each part that holds a slice or Map), each call of an owned
body with each owned argument handed over or copied, and why. --json
prints the report as JSON. It lowers in process and runs no Go tool.

Exit status: 0 when the package checks, 1 when it does not, 2 on an
invalid invocation.
`

// inspectCopiesCommand prints the copy report of the run build
// (sketchfront.CopyReport), grouped as inspectCopiesHelp says.
func inspectCopiesCommand(args []string) error {
	json := false
	var paths []string
	for _, arg := range args {
		switch {
		case arg == "--json":
			json = true
		case arg == "--help" || arg == "-h":
			_, err := os.Stdout.WriteString(inspectCopiesHelp)
			return err
		case strings.HasPrefix(arg, "-"):
			return invalidInvocation(fmt.Errorf("inspect copies: unknown option %s", arg))
		default:
			paths = append(paths, arg)
		}
	}
	if len(paths) != 1 {
		return invalidInvocation(fmt.Errorf("inspect copies: supply FILE|DIR"))
	}
	opts, err := parseFileOptions("inspect copies", paths)
	if err != nil {
		return invalidInvocation(err)
	}
	l, err := load(opts.file, opts.target)
	if err != nil {
		return err
	}
	if !l.result.Checked {
		return refuseUnchecked(l, opts)
	}
	report, err := sketchfront.CopyReport(l.analysis, l.source.Admitted())
	if err != nil {
		return err
	}
	if json {
		return printJSON(report)
	}
	var b strings.Builder
	for _, f := range report.Owned {
		fmt.Fprintf(&b, "OWNS %s:%d:%d %s: %s\n", f.File, f.Line, f.Column, f.Func, strings.Join(f.Params, ", "))
	}
	for _, s := range report.Sites {
		form := s.Form
		if s.Kind != "" {
			form += " " + s.Kind
		}
		fn := s.Func
		if fn == "" {
			fn = "_"
		}
		fmt.Fprintf(&b, "SITE %s:%d:%d %s: %s: %s\n", s.File, s.Line, s.Column, fn, form, s.Why)
	}
	_, err = os.Stdout.WriteString(b.String())
	return err
}
