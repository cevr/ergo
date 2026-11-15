package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"ergo.local/prototype/internal/sketch/fix"
)

const fixUsage = "usage: ergo fix [--check|--dry-run] [--json] [--std DIR] [--go] PATH..."

const fixHelp = fixUsage + `

Rewrites source into the spelling the language requires. It never runs
during check, build or test: they leave sources as they are.

First it writes the declared implementations (internal/sketch/fix):
` + "`implements I`" + ` on each type that the old method-set rule used as the
operation type I, and ` + "`..I`" + ` in each operation type used as I. A PATH is a
file or a folder; a folder is walked, each module package by package and
each other .ergo file as a program of its own. --std DIR also checks every
package of the embedded standard library and edits its source in DIR when
DIR is under a PATH. --go also reads the Ergo programs that .go files hold
in string literals, each as a program of its own, and edits the literals.
It prints what it wrote and each pair it could not write, with the repair.

Then, for each PATH that is a .ergo file or a folder holding .ergo files,
it writes the fails, needs and takes clauses that the signatures of the
file, or of the package in the folder and the local packages it imports,
are missing (q137: every function writes its clauses). It only adds clause
elements and the imports they need; it never removes an element and never
adds a catch. It prints one line per widened signature, public contracts
first. Other diagnostics are printed, and the exit status is then 1.

  --dry-run  write nothing
  --check    write nothing; exit 1 when a signature would change
  --json     print the clause results as JSON

Exit codes: 0 done; 1 a file could not be read or written, a diagnostic
remains, or --check found a change; 2 invalid invocation.
`

type fixReport struct {
	Written   bool             `json:"written"`
	Rounds    int              `json:"rounds"`
	Changed   []fix.Change     `json:"changed"`
	Remaining []fix.Diagnostic `json:"remaining"`
}

// fixCommand is `ergo fix`: the implements pass over every PATH, then the
// clause pass over each PATH that is a program.
func fixCommand(args []string) error {
	var paths []string
	var check, dry, asJSON, goLits bool
	stdRoot := ""
	for i := 0; i < len(args); i++ {
		switch a := args[i]; a {
		case "--check":
			check = true
		case "--dry-run":
			dry = true
		case "--json":
			asJSON = true
		case "--go":
			goLits = true
		case "--std":
			if i+1 == len(args) {
				return usageError{message: fixUsage}
			}
			i++
			stdRoot = args[i]
		default:
			if len(a) > 1 && a[0] == '-' {
				return usageError{message: fixUsage}
			}
			paths = append(paths, a)
		}
	}
	if len(paths) == 0 || check && dry {
		return usageError{message: fixUsage}
	}
	write := !check && !dry
	impl, err := fix.Run(fix.Options{Paths: paths, StdRoot: stdRoot, Write: write, Go: goLits})
	if err != nil {
		return exitStatusError{code: 1, message: "ergo fix: " + err.Error()}
	}
	if len(impl.Written) > 0 || len(impl.Unwritten) > 0 {
		out := os.Stdout
		if asJSON {
			out = os.Stderr // stdout holds only the JSON
		}
		fmt.Fprint(out, impl.Text())
	}
	rep := fixReport{Changed: []fix.Change{}, Remaining: []fix.Diagnostic{}}
	for _, path := range paths {
		if !fixProgram(path) {
			continue
		}
		texts, chk, err := fix.Load(path)
		if err != nil {
			return err
		}
		res, err := fix.Rows(texts, chk)
		if err != nil {
			return err
		}
		if write {
			if err := writeFixed(texts, res.Texts); err != nil {
				return err
			}
		}
		rep.Written = rep.Written || write && len(res.Changed) > 0
		rep.Rounds = max(rep.Rounds, res.Rounds)
		rep.Changed = append(rep.Changed, res.Changed...)
		rep.Remaining = append(rep.Remaining, res.Remaining...)
	}
	if asJSON {
		if err := printJSON(rep); err != nil {
			return err
		}
	} else {
		printFix(rep)
	}
	switch {
	case len(rep.Remaining) > 0:
		return exitStatusError{code: 1, message: "fix: diagnostics remain", handled: true}
	case check && (len(rep.Changed) > 0 || len(impl.Written) > 0):
		return exitStatusError{code: 1, message: "fix: signatures would change", handled: true}
	}
	return nil
}

// fixProgram reports whether path is a program the clause pass reads: a
// .ergo file, or a folder that holds one.
func fixProgram(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if !info.IsDir() {
		return filepath.Ext(path) == ".ergo"
	}
	matches, _ := filepath.Glob(filepath.Join(path, "*.ergo"))
	return len(matches) > 0
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
