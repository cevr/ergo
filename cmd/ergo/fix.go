package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"ergo.local/prototype/internal/linked/code"
	ckpkg "ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/fix"
	"ergo.local/prototype/internal/sketch/lint"
)

const fixUsage = "usage: ergo fix [--check|--dry-run] [--json] [--lint] [--std DIR] [--go] PATH..."

const fixHelp = fixUsage + `

Rewrites source into the spelling the language requires. It never runs
during check, build or test: they leave sources as they are.

First it respells the old modes: takes and escaping become own, and the
borrow *T written outside a Go binding becomes mut T (with --go, in the
Go string literals too), under each PATH and in the --std DIR. The
respell reads no checked fact, so it rewrites a file that does not
check, std's too.

Then it writes the declared implementations (internal/sketch/fix):
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
` + fixLawsHelp + `
  --dry-run  write nothing
  --check    write nothing; exit 1 when a signature would change
  --json     print the clause results as JSON
  --lint     then apply the safe lint fixes (race-for-timeout) of each
             program's lint configuration, until none is left; a fix
             that leaves a diagnostic refuses the run and writes nothing

Exit codes: 0 done; 1 a file could not be read or written, a diagnostic
remains, or --check found a change; 2 invalid invocation.
`

type fixReport struct {
	Written   bool             `json:"written"`
	Rounds    int              `json:"rounds"`
	Changed   []fix.Change     `json:"changed"`
	Remaining []fix.Diagnostic `json:"remaining"`
	Linted    []fix.Linted     `json:"linted,omitempty"`
}

// fixCommand is `ergo fix`: the implements pass over every PATH, then the
// clause pass over each PATH that is a program.
func fixCommand(args []string) error {
	var paths []string
	var check, dry, asJSON, goLits, withLint bool
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
		case "--lint":
			withLint = true
		case "--std":
			if i+1 == len(args) {
				return usageError{message: fixUsage}
			}
			i++
			stdRoot = args[i]
		default:
			if len(a) > 1 && a[0] == '-' {
				return unknownOption("", a)
			}
			paths = append(paths, a)
		}
	}
	if len(paths) == 0 && stdRoot == "" || check && dry {
		return usageError{message: fixUsage}
	}
	write := !check && !dry
	// The respells come first: they are syntactic, so they read a file
	// that does not check, and every checked pass below reads the new
	// spelling (RESPELL-SYNTAX). A std folder, or a package, written in
	// the old spelling checks only once it is respelled.
	stdRespelled, err := respell(respellRoots(fixFolders(paths), stdRoot), stdRoot, write, goLits, asJSON)
	if err != nil {
		return exitStatusError{code: 1, message: "ergo fix: " + err.Error()}
	}
	refused := func(err error) error {
		msg := "ergo fix: " + err.Error()
		if stdRespelled {
			// This ergo embeds the std it was built from, which still
			// spells the old modes: the checked passes need an ergo
			// built from the respelled std.
			msg += "\nergo fix: the respell of " + stdRoot + " is written; build ergo from it, then run ergo fix again for the checked passes"
		}
		return exitStatusError{code: 1, message: msg}
	}
	if len(paths) == 0 {
		// --std DIR alone: the respell above and the law pass over the
		// standard library.
		text, err := fixLaws(nil, stdRoot, write)
		if err != nil {
			return refused(err)
		}
		fmt.Fprint(os.Stdout, text)
		return nil
	}
	lawPaths, paths := paths, fixFolders(paths)
	impl, err := fix.Run(fix.Options{Paths: paths, StdRoot: stdRoot, Write: write, Go: goLits})
	if err != nil {
		return refused(err)
	}
	if len(impl.Written) > 0 || len(impl.Unwritten) > 0 {
		out := os.Stdout
		if asJSON {
			out = os.Stderr // stdout holds only the JSON
		}
		fmt.Fprint(out, impl.Text())
	}
	// A body marked [T own] above holds its T to a resource's rules, so a
	// generic callee it binds T to needs the mark too (MODES-5): the pass
	// again over the rewritten files, until it writes no mark.
	for round := 0; write && round < 8 && impl.WroteOwn(); round++ {
		if stdRoot != "" {
			ckpkg.ResetStdForTest() // std is checked again from the rewritten sources
		}
		if impl, err = fix.Run(fix.Options{Paths: paths, StdRoot: stdRoot, Write: true, Go: goLits}); err != nil {
			return exitStatusError{code: 1, message: "ergo fix: " + err.Error()}
		}
		if len(impl.Written) > 0 || len(impl.Unwritten) > 0 {
			out := os.Stdout
			if asJSON {
				out = os.Stderr
			}
			fmt.Fprint(out, impl.Text())
		}
	}
	// A sub-union re-listed and rebuilt arm by arm becomes the union
	// listed and widened (DECL-VALUES §6 unit 2; types.md, Unions 1),
	// kept per package only when its check verdict is unchanged.
	flat, err := fix.RunUnionFlat(fix.Options{Paths: paths, Write: write})
	if err != nil {
		return exitStatusError{code: 1, message: "ergo fix: " + err.Error()}
	}
	if text := flat.Text(); text != "" {
		out := os.Stdout
		if asJSON {
			out = os.Stderr // stdout holds only the JSON
		}
		fmt.Fprint(out, text)
	}
	// The law pass (LAWLOC); --check skips it while S115 is off.
	if !check {
		text, err := fixLaws(lawPaths, stdRoot, write)
		if err != nil {
			return exitStatusError{code: 1, message: "ergo fix: " + err.Error()}
		}
		if text != "" {
			out := os.Stdout
			if asJSON {
				out = os.Stderr // stdout holds only the JSON
			}
			fmt.Fprint(out, text)
		}
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
		var linter fix.Linter
		if withLint {
			doc, err := lintConfig(path)
			if err != nil {
				return invalidInvocation(err)
			}
			// fix applies built-in fixes only and never starts a rule pack.
			conf, err := lint.Builtin().Configure(doc.Builtins(), nil)
			if err != nil {
				return invalidInvocation(fmt.Errorf("%s: %w", lint.ConfigFile, err))
			}
			if linter, err = fix.LintOf(path, conf); err != nil {
				return err
			}
		}
		res, err := fix.RunLint(texts, chk, linter)
		if err != nil {
			return err
		}
		if write {
			if err := writeFixed(texts, res.Texts); err != nil {
				return err
			}
		}
		rep.Written = rep.Written || write && (len(res.Changed) > 0 || len(res.Linted) > 0)
		rep.Rounds = max(rep.Rounds, res.Rounds)
		rep.Changed = append(rep.Changed, res.Changed...)
		rep.Remaining = append(rep.Remaining, res.Remaining...)
		rep.Linted = append(rep.Linted, res.Linted...)
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
	case check && len(flat.Changes) > 0:
		return exitStatusError{code: 1, message: "fix: unions would be listed", handled: true}
	case check && len(rep.Linted) > 0:
		return exitStatusError{code: 1, message: "fix: lint fixes would apply", handled: true}
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
	for _, l := range rep.Linted {
		fmt.Printf("%s:%d: fixed %s\n", displayPath(l.File), l.Line, l.Rule)
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

// writeFixed writes the files whose text changes from before to after
// (keyed by path) as one plan, by std code's apply protocol
// (code.Rewrite, SEMTOOLS SEM4c): each is exchanged with its new text in
// one host call, under the workspace's lock, after a check that every
// file still holds the text the fix read; a file changed meanwhile is
// refused Stale and no file is written. Each file keeps its mode. The
// workspace folder is the folder that holds every path; the lock and the
// journal are in fixState's folder, so the user's folder gains nothing.
func writeFixed(before, after map[string]string) error {
	var paths []string
	for p, text := range after {
		if text != before[p] {
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		return nil
	}
	slices.Sort(paths)
	root := filepath.Dir(paths[0])
	for _, p := range paths[1:] {
		for root != filepath.Dir(root) && !strings.HasPrefix(p, root+string(filepath.Separator)) {
			root = filepath.Dir(root)
		}
	}
	was, now := []codeSource{}, []codeSource{}
	for _, p := range paths {
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		was = append(was, codeSource{File: filepath.ToSlash(rel), Text: before[p]})
		now = append(now, codeSource{File: filepath.ToSlash(rel), Text: after[p]})
	}
	b, err := json.Marshal(was)
	if err != nil {
		return err
	}
	a, err := json.Marshal(now)
	if err != nil {
		return err
	}
	state, err := fixState(root)
	if err != nil {
		return err
	}
	out, err := code.Rewrite(context.Background(), root, state, string(b), string(a))
	if err != nil {
		return fmt.Errorf("fix: %w", err)
	}
	if len(out) < 3 || out[0] != "0" {
		return fmt.Errorf("%s", strings.TrimSpace(strings.Join(out[min(len(out), 2):], "")))
	}
	return nil
}

// fixState is the folder of ergo fix's lock and journal for the workspace
// folder root: in the user's cache folder, named by root's path, so two
// fixes of one folder take one lock, a fix that died is undone by the
// next one, and root gains no file.
func fixState(root string) (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("fix: no cache folder for the apply lock: %w", err)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(abs))
	return filepath.Join(cache, "ergo", "fix", hex.EncodeToString(sum[:8])), nil
}

// respellRoots is the folders and files the respells walk: the paths, and
// the std folder when no path holds it already.
func respellRoots(paths []string, stdRoot string) []string {
	roots := slices.Clone(paths)
	if stdRoot == "" {
		return roots
	}
	std, err := filepath.Abs(stdRoot)
	if err != nil {
		return append(roots, stdRoot)
	}
	for _, p := range paths {
		if abs, err := filepath.Abs(p); err == nil && (std == abs || strings.HasPrefix(std, abs+string(filepath.Separator))) {
			return roots
		}
	}
	return append(roots, stdRoot)
}

// respell applies the syntactic respells to roots: the keep mode's old
// spellings (takes, escaping) become own (EXM1), and the borrow *T outside
// a Go binding becomes mut T (MODES-1; q169), bindings included and, with
// goLits, the Ergo programs in Go strings. It reads no checked fact, so a
// file that does not check is respelled too. stdWritten reports a file
// under stdRoot that it rewrote.
func respell(roots []string, stdRoot string, write, goLits, asJSON bool) (stdWritten bool, err error) {
	out := os.Stdout
	if asJSON {
		out = os.Stderr // stdout holds only the JSON
	}
	modes, err := fix.RunModes(fix.Options{Paths: roots, Write: write, Go: goLits})
	if err != nil {
		return false, err
	}
	fmt.Fprint(out, modes.Text())
	muts, err := fix.RunMut(fix.Options{Paths: roots, Write: write, Go: goLits})
	if err != nil {
		return false, err
	}
	fmt.Fprint(out, muts.Text())
	if !write || stdRoot == "" {
		return false, nil
	}
	std, err := filepath.Abs(stdRoot)
	if err != nil {
		return false, nil
	}
	for _, files := range []map[string]string{modes.Files, muts.Files} {
		for file := range files {
			if abs, err := filepath.Abs(file); err == nil && strings.HasPrefix(abs, std+string(filepath.Separator)) {
				return true, nil
			}
		}
	}
	return false, nil
}
