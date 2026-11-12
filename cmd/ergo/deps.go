package main

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"ergo.local/prototype/internal/sketch/api"
	"ergo.local/prototype/internal/sketchfront"
)

const inspectDepsHelp = `usage: ergo inspect deps DIR [--json]

Lists each required module the program in DIR reads (grammar.md, Source
files 1, Required modules and Trusted Go): its path and selected version,
whether the root's ergo.mod trusts its Go, and for each of its packages
the program reads, the capabilities its exported functions need and its
uses of Go (import go, extern go). Read it before writing a trust go line.
A package that was not checked (the program has diagnostics) shows no
capabilities. --json prints the same as JSON.

Exit status: 0 when the program checks, 1 when it does not (the list is
printed first), 2 on an invalid invocation.
`

// depReport is one required module of ergo inspect deps.
type depReport struct {
	Module   string       `json:"module"`
	Trusted  bool         `json:"trusted"`
	Packages []depPackage `json:"packages"`
}

type depPackage struct {
	Path    string   `json:"path"`
	Checked bool     `json:"checked"`
	Needs   []string `json:"needs"`
	Go      []string `json:"go"`
}

// inspectDepsCommand is `ergo inspect deps DIR [--json]` (MD3).
func inspectDepsCommand(args []string) error {
	json := false
	var paths []string
	for _, arg := range args {
		switch {
		case arg == "--json":
			json = true
		case arg == "--help" || arg == "-h":
			_, err := os.Stdout.WriteString(inspectDepsHelp)
			return err
		case strings.HasPrefix(arg, "-"):
			return invalidInvocation(fmt.Errorf("inspect deps: unknown option %s", arg))
		default:
			paths = append(paths, arg)
		}
	}
	if len(paths) != 1 || !isPackageFolder(paths[0]) {
		return invalidInvocation(fmt.Errorf("inspect deps: supply one package folder, DIR"))
	}
	l, err := load(paths[0], "go")
	if err != nil {
		return err
	}
	reports := depReports(l.analysis.Packages())
	if json {
		if err := printJSON(reports); err != nil {
			return err
		}
	} else if _, err := os.Stdout.WriteString(depText(reports)); err != nil {
		return err
	}
	if n := len(l.analysis.Diagnostics); n > 0 {
		return exitStatusError{code: 1, message: fmt.Sprintf("ergo inspect deps: the program has %d diagnostics; ergo diagnostics %s lists them", n, paths[0])}
	}
	return nil
}

// depReports groups the program's packages of required modules by module,
// in the order the loader read them.
func depReports(pkgs []sketchfront.CheckedPackage) []depReport {
	reports := []depReport{}
	for _, p := range pkgs {
		if p.Module == "" {
			continue
		}
		at := slices.IndexFunc(reports, func(r depReport) bool { return r.Module == p.Module })
		if at < 0 {
			reports = append(reports, depReport{Module: p.Module, Trusted: p.Trusted, Packages: []depPackage{}})
			at = len(reports) - 1
		}
		d := depPackage{Path: p.Path, Checked: p.Package != nil, Needs: []string{}, Go: slices.Clone(p.Go)}
		if d.Go == nil {
			d.Go = []string{}
		}
		if p.Package != nil {
			for _, f := range api.Functions(p.Package) {
				for _, need := range strings.Split(f.Needs, " | ") {
					if need != "" && !slices.Contains(d.Needs, need) {
						d.Needs = append(d.Needs, need)
					}
				}
			}
			slices.Sort(d.Needs)
		}
		reports[at].Packages = append(reports[at].Packages, d)
	}
	return reports
}

func depText(reports []depReport) string {
	if len(reports) == 0 {
		return "no required module\n"
	}
	var b strings.Builder
	for _, r := range reports {
		trust := "Go not trusted"
		if r.Trusted {
			trust = "Go trusted"
		}
		fmt.Fprintf(&b, "%s (%s)\n", r.Module, trust)
		for _, p := range r.Packages {
			needs := "needs nothing"
			switch {
			case !p.Checked:
				needs = "not checked"
			case len(p.Needs) > 0:
				needs = "needs " + strings.Join(p.Needs, " | ")
			}
			fmt.Fprintf(&b, "  %s: %s\n", p.Path, needs)
			for _, use := range p.Go {
				fmt.Fprintf(&b, "    %s\n", use)
			}
		}
	}
	return b.String()
}
