package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"ergo.local/prototype/internal/sketch/fix"
)

const fixHelp = `usage: ergo fix FILE|DIR [--check] [--json]

Writes the fails and needs clauses that the signatures of FILE, or of the
package in DIR and the local packages it imports, are missing (q137:
every function writes its clauses). It only adds clause elements and the
imports they need; it never removes an element and never adds a catch.
It prints one line per widened signature, public contracts first.

  --check  write nothing; exit 1 when a signature would change
  --json   print the result as JSON

The checker never runs fix: check, build and test leave sources as they
are. Other diagnostics are printed, and the exit status is then 1.
`

type fixReport struct {
	Written   bool             `json:"written"`
	Rounds    int              `json:"rounds"`
	Changed   []fix.Change     `json:"changed"`
	Remaining []fix.Diagnostic `json:"remaining"`
}

func fixCommand(args []string) error {
	var path string
	var check, asJSON bool
	for _, a := range args {
		switch a {
		case "--check":
			check = true
		case "--json":
			asJSON = true
		default:
			if path != "" || len(a) > 1 && a[0] == '-' {
				return usageError{message: "usage: ergo fix FILE|DIR [--check] [--json]"}
			}
			path = a
		}
	}
	if path == "" {
		return usageError{message: "usage: ergo fix FILE|DIR [--check] [--json]"}
	}
	texts, chk, err := fix.Load(path)
	if err != nil {
		return err
	}
	res, err := fix.Run(texts, chk)
	if err != nil {
		return err
	}
	if !check {
		if err := writeFixed(texts, res.Texts); err != nil {
			return err
		}
	}
	rep := fixReport{Written: !check && len(res.Changed) > 0, Rounds: res.Rounds, Changed: res.Changed, Remaining: res.Remaining}
	if rep.Changed == nil {
		rep.Changed = []fix.Change{}
	}
	if rep.Remaining == nil {
		rep.Remaining = []fix.Diagnostic{}
	}
	if asJSON {
		if err := printJSON(rep); err != nil {
			return err
		}
	} else {
		printFix(rep)
	}
	switch {
	case len(res.Remaining) > 0:
		return exitStatusError{code: 1, message: "fix: diagnostics remain", handled: true}
	case check && len(res.Changed) > 0:
		return exitStatusError{code: 1, message: "fix: signatures would change", handled: true}
	}
	return nil
}

func printFix(rep fixReport) {
	for i, c := range rep.Changed {
		if i == 0 && c.Exported {
			fmt.Println("public contract widened:")
		}
		indent := ""
		if c.Exported {
			indent = "  "
		}
		fmt.Printf("%s%s: %s\n", indent, displayPath(c.File), c.Signature)
	}
	for _, d := range rep.Remaining {
		fmt.Fprintf(os.Stderr, "%s:%d:%d: error %s: %s\n", displayPath(d.File), d.Span.Line, d.Span.Column, d.Code, d.Message)
	}
}

func displayPath(path string) string {
	if wd, err := os.Getwd(); err == nil {
		if rel, err := filepath.Rel(wd, path); err == nil && !filepath.IsAbs(rel) && rel != "" && rel[0] != '.' {
			return rel
		}
	}
	return path
}

// writeFixed writes each changed file once, by rename, after checking
// that it still holds the text the fix read.
func writeFixed(before, after map[string]string) error {
	var paths []string
	for p, text := range after {
		if text != before[p] {
			paths = append(paths, p)
		}
	}
	slices.Sort(paths)
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return err
		}
		now, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if !bytes.Equal(now, []byte(before[p])) {
			return fmt.Errorf("fix: %s changed while it was fixed; nothing written to it", p)
		}
		temp, err := os.CreateTemp(filepath.Dir(p), "."+filepath.Base(p)+".ergo-fix-*")
		if err != nil {
			return err
		}
		_, werr := temp.WriteString(after[p])
		cerr := temp.Close()
		if werr == nil {
			werr = cerr
		}
		if werr == nil {
			werr = os.Chmod(temp.Name(), info.Mode().Perm())
		}
		if werr == nil {
			werr = os.Rename(temp.Name(), p)
		}
		if werr != nil {
			_ = os.Remove(temp.Name())
			return werr
		}
	}
	return nil
}
