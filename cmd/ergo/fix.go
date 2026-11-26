package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"ergo.local/prototype/internal/linked/code"
	"ergo.local/prototype/internal/report"
	ckpkg "ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/fix"
	"ergo.local/prototype/internal/sketch/lint"
	"ergo.local/prototype/internal/sketchfront"
)

const fixUsage = "usage: ergo fix [--check|--dry-run] [--json] [--lint] [--comments|--len|--global-funcs|--std-types|--machines|--option] [--std DIR] [--go] PATH..."

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
  --lint     then apply the safe lint fixes (race-for-timeout, hidden-dependency,
             eta-method-value, file-name-case) of each program's lint
             configuration, until none is left; a fix that leaves a
             diagnostic refuses the run and writes nothing
  --comments     only remove, under each PATH and the --std DIR, every comment
                 lint's no-comments refuses (all but a public declaration's
                 doc comment), keeping each file's tokens; with --json it
                 lists what it removed: file, line, declaration, position, text
  --len          only rewrite, under each PATH and the --std DIR, every call
                 to the universe len(e) built-in into e.Len(), parenthesising
                 lower-precedence operands and leaving user-declared len
                 untouched; with --json it lists every rewritten site
  --global-funcs only rewrite, under each PATH and the --std DIR, calls to
                 universe/prelude global functions (hash, encode, decode,
                 embedded, MakeSlice, MakeMap, InForce, AsWritten, TestFolder,
                 TestFolderHandle, NewLiveScheduler) into their std-package
                 forms, adding missing imports in sorted order and leaving
                 user shadows untouched; with --json it lists every rewrite
  --std-types    only rewrite, under each PATH and the --std DIR, references
                 to Unit-8 prelude types (Console, Ambient, Scheduler, Random,
                 Writer, Reader, Wire*, Hashing, Structural, Task, ordered,
                 number, TestAmbient, TestDirectory, TestLoopback, Range, Seq,
                 Never, Context, CallSite, Closer, Keeping, Unsatisfied,
                 NoMatch) into their std-package forms, adding missing imports
                 in sorted order and leaving user shadows untouched; with
                 --json it lists every rewrite
  --machines     only rewrite, under each PATH and the --std DIR, references
                 to Unit-9 prelude machine types (Enter, Stay, Emit, Ignore,
                 Reject, Done, Tell, Decision, Timeout, Machine, InputMachine)
                 into their qualified machines.* forms, adding import machines
                 in sorted order and leaving user shadows untouched; with
                 --json it lists every rewrite
  --option       only rewrite, under each PATH and the --std DIR, references
                 to Unit-10 prelude option types (Option, Some, None) into
                 their qualified option.* forms, adding import option in
                 sorted order and leaving user shadows untouched; with --json
                 it lists every rewrite

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
	var check, dry, asJSON, goLits, withLint, comments, rewriteLen, globalFuncs, stdTypes, machinesFlag, optionFlag bool
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
		case "--comments":
			comments = true
		case "--len":
			rewriteLen = true
		case "--global-funcs":
			globalFuncs = true
		case "--std-types":
			stdTypes = true
		case "--machines":
			machinesFlag = true
		case "--option":
			optionFlag = true
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
	specials := 0
	if comments {
		specials++
	}
	if rewriteLen {
		specials++
	}
	if globalFuncs {
		specials++
	}
	if stdTypes {
		specials++
	}
	if machinesFlag {
		specials++
	}
	if optionFlag {
		specials++
	}
	if len(paths) == 0 && stdRoot == "" || check && dry || specials > 1 {
		return usageError{message: fixUsage}
	}
	write := !check && !dry
	if comments {
		return fixComments(respellRoots(fixFolders(paths), stdRoot), write, check, asJSON)
	}
	if rewriteLen {
		return fixLen(fixFolders(paths), stdRoot, write, check, asJSON)
	}
	if globalFuncs {
		return fixGlobalFuncs(fixFolders(paths), stdRoot, write, check, asJSON)
	}
	if stdTypes {
		return fixStdTypes(fixFolders(paths), stdRoot, write, check, asJSON)
	}
	if machinesFlag {
		return fixMachines(fixFolders(paths), stdRoot, write, check, asJSON)
	}
	if optionFlag {
		return fixOption(fixFolders(paths), stdRoot, write, check, asJSON)
	}
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
	targetPaths := expandFixTargets(paths, withLint)
	seenChanged := map[string]bool{}
	seenLinted := map[fix.Linted]bool{}
	for _, path := range targetPaths {
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
		for _, c := range res.Changed {
			key := c.File + "\x00" + c.Signature
			if !seenChanged[key] {
				seenChanged[key] = true
				rep.Changed = append(rep.Changed, c)
			}
		}
		rep.Remaining = append(rep.Remaining, res.Remaining...)
		for _, l := range res.Linted {
			if !seenLinted[l] {
				seenLinted[l] = true
				rep.Linted = append(rep.Linted, l)
			}
		}
	}
	if withLint && len(rep.Remaining) == 0 {
		fcRep, err := fix.RunFileCase(fix.FileCaseOptions{Paths: lawPaths, Write: write})
		if err != nil {
			var cfgErr *fix.ConfigError
			if errors.As(err, &cfgErr) {
				return invalidInvocation(err)
			}
			return err
		}
		for _, r := range fcRep.Renames {
			rep.Linted = append(rep.Linted, fix.Linted{Rule: "file-name-case", File: r.From, Line: 1, To: r.To})
		}
		for _, sk := range fcRep.Skipped {
			rep.Remaining = append(rep.Remaining, fix.Diagnostic{
				File: sk.From,
				Diagnostic: report.Diagnostic{
					Code:    "L172",
					Message: sk.Reason,
					Span:    report.Span{Line: 1, Column: 1},
				},
			})
		}
		if write && len(fcRep.Renames) > 0 {
			rep.Written = true
		}
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

func expandFixTargets(paths []string, withLint bool) []string {
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		key := p
		if abs, err := filepath.Abs(p); err == nil {
			key = abs
		}
		if !seen[key] {
			seen[key] = true
			out = append(out, p)
		}
	}
	for _, path := range paths {
		if fixProgram(path) {
			add(path)
			continue
		}
		if !withLint {
			continue
		}
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			continue
		}
		if targets, err := sketchfront.DiscoverPackages(path); err == nil {
			for _, tgt := range targets {
				add(tgt.Dir)
			}
		}
	}
	return out
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
		if l.To != "" {
			fmt.Printf("%s -> %s: fixed %s\n", displayPath(l.File), displayPath(l.To), l.Rule)
		} else {
			fmt.Printf("%s:%d: fixed %s\n", displayPath(l.File), l.Line, l.Rule)
		}
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
// fixComments is `ergo fix --comments`: the comment pass alone over roots
// (fix.RunComments). With asJSON it prints the comments it took out, the
// rows of the refactor worklist; with check it writes nothing and exits 1
// when a comment would go.
func fixComments(roots []string, write, check, asJSON bool) error {
	rep, err := fix.RunComments(fix.Options{Paths: roots, Write: write})
	if err != nil {
		return exitStatusError{code: 1, message: "ergo fix: " + err.Error()}
	}
	if asJSON {
		removed := rep.Removed
		if removed == nil {
			removed = []fix.Removed{}
		}
		if err := printJSON(struct {
			Written bool          `json:"written"`
			Removed []fix.Removed `json:"removed"`
			Errors  []string      `json:"errors,omitempty"`
		}{write && len(rep.Files) > 0, removed, rep.Errs}); err != nil {
			return err
		}
	} else {
		fmt.Fprint(os.Stdout, rep.Text())
	}
	switch {
	case len(rep.Errs) > 0:
		return exitStatusError{code: 1, message: "fix: comments left in files it could not change", handled: true}
	case check && len(rep.Files) > 0:
		return exitStatusError{code: 1, message: "fix: comments would be removed", handled: true}
	}
	return nil
}

// fixLen is `ergo fix --len`: rewrites universe len(e) calls to e.Len()
// (fix.RunLen). With asJSON it prints every rewritten site; with check it
// writes nothing and exits 1 when any len(e) call would be rewritten.
func fixLen(paths []string, stdRoot string, write, check, asJSON bool) error {
	rep, err := fix.RunLen(fix.Options{Paths: paths, StdRoot: stdRoot, Write: write})
	if err != nil {
		return exitStatusError{code: 1, message: "ergo fix: " + err.Error()}
	}
	if asJSON {
		rewrites := rep.Rewrites
		if rewrites == nil {
			rewrites = []fix.LenRewrite{}
		}
		if err := printJSON(struct {
			Written  bool             `json:"written"`
			Rewrites []fix.LenRewrite `json:"rewrites"`
			Errors   []string         `json:"errors,omitempty"`
		}{write && len(rep.Files) > 0, rewrites, rep.Errs}); err != nil {
			return err
		}
	} else {
		fmt.Fprint(os.Stdout, rep.Text())
	}
	switch {
	case len(rep.Errs) > 0:
		return exitStatusError{code: 1, message: "fix: len calls left in files it could not change", handled: true}
	case check && len(rep.Files) > 0:
		return exitStatusError{code: 1, message: "fix: len calls would be rewritten", handled: true}
	}
	return nil
}

// fixGlobalFuncs is `ergo fix --global-funcs`: rewrites universe/prelude
// global function calls to their std-package forms (fix.RunGlobalFuncs). With
// asJSON it prints every rewritten site; with check it writes nothing and
// exits 1 when any call would be rewritten.
func fixGlobalFuncs(paths []string, stdRoot string, write, check, asJSON bool) error {
	rep, err := fix.RunGlobalFuncs(fix.Options{Paths: paths, StdRoot: stdRoot, Write: write})
	if err != nil {
		return exitStatusError{code: 1, message: "ergo fix: " + err.Error()}
	}
	if asJSON {
		rewrites := rep.Rewrites
		if rewrites == nil {
			rewrites = []fix.GlobalFuncRewrite{}
		}
		if err := printJSON(struct {
			Written  bool                    `json:"written"`
			Rewrites []fix.GlobalFuncRewrite `json:"rewrites"`
			Errors   []string                `json:"errors,omitempty"`
		}{write && len(rep.Files) > 0, rewrites, rep.Errs}); err != nil {
			return err
		}
	} else {
		fmt.Fprint(os.Stdout, rep.Text())
	}
	switch {
	case len(rep.Errs) > 0:
		return exitStatusError{code: 1, message: "fix: global function calls left in files it could not change", handled: true}
	case check && len(rep.Files) > 0:
		return exitStatusError{code: 1, message: "fix: global function calls would be rewritten", handled: true}
	}
	return nil
}

type fixStdTypesReport struct {
	Written  bool                 `json:"written"`
	Rewrites []fix.StdTypeRewrite `json:"rewrites"`
	Errors   []string             `json:"errors,omitempty"`
}

func fixStdTypes(paths []string, stdRoot string, write, check, asJSON bool) error {
	return runTypeFix(fix.RunStdTypes, "std type", paths, stdRoot, write, check, asJSON)
}

func fixMachines(paths []string, stdRoot string, write, check, asJSON bool) error {
	return runTypeFix(fix.RunMachines, "machine type", paths, stdRoot, write, check, asJSON)
}

func fixOption(paths []string, stdRoot string, write, check, asJSON bool) error {
	return runTypeFix(fix.RunOption, "option type", paths, stdRoot, write, check, asJSON)
}

func runTypeFix(runner func(fix.Options) (fix.StdTypesReport, error), kind string, paths []string, stdRoot string, write, check, asJSON bool) error {
	rep, err := runner(fix.Options{Paths: paths, StdRoot: stdRoot, Write: write})
	if err != nil {
		return exitStatusError{code: 1, message: "ergo fix: " + err.Error()}
	}
	if asJSON {
		out := fixStdTypesReport{
			Written:  write && len(rep.Files) > 0,
			Rewrites: rep.Rewrites,
			Errors:   rep.Errs,
		}
		if out.Rewrites == nil {
			out.Rewrites = []fix.StdTypeRewrite{}
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(out); err != nil {
			return err
		}
	} else {
		fmt.Fprint(os.Stdout, rep.Text())
	}
	switch {
	case len(rep.Errs) > 0:
		return exitStatusError{code: 1, message: fmt.Sprintf("fix: %s references left in files it could not change", kind), handled: true}
	case check && len(rep.Files) > 0:
		return exitStatusError{code: 1, message: fmt.Sprintf("fix: %s references would be rewritten", kind), handled: true}
	}
	return nil
}

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
	// The parser's own fixes first (VET-CHECK-VAR): a Go local
	// declaration becomes let or let mut, applied and parsed again until
	// no error carries a fix, so the passes after it read Ergo.
	lets, err := fix.RunLets(fix.Options{Paths: roots, Write: write})
	if err != nil {
		return false, err
	}
	fmt.Fprint(out, lets.Text())
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
	for _, files := range []map[string]string{lets.Files, modes.Files, muts.Files} {
		for file := range files {
			if abs, err := filepath.Abs(file); err == nil && strings.HasPrefix(abs, std+string(filepath.Separator)) {
				return true, nil
			}
		}
	}
	return false, nil
}
