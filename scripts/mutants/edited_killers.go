package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type editedFileKind string

const (
	editedGo   editedFileKind = "go"
	editedErgo editedFileKind = "ergo"
)

type editedTestFile struct {
	File         string
	PkgDir       string
	Kind         editedFileKind
	WholePackage bool
	EditedTests  map[string]bool
}

type editedKillerHit struct {
	EditedFile string
	KillerID   string
}

type affectedMutant struct {
	LedgerPath string
	LedgerRel  string
	Ledger     *Ledger
	Mutant     *Mutant
	Hits       []editedKillerHit
}

type lineRange struct {
	Start int
	End   int
}

var (
	diffHunkRE          = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)
	ergoTestOrLawDeclRE = regexp.MustCompile(`(?m)^[ \t]*(?:test|law)[ \t]+[A-Za-z_]`)
	familyPartSuffixRE  = regexp.MustCompile(`^(Test.+)Part[0-9]+$`)
)

func runEditedKillers(cfg Config, base string, dryRun bool, selectRE, ergoFlag string, noNativeErgo bool, scopeArgs []string, stdout, stderr io.Writer) int {
	if base == "" {
		fmt.Fprintln(stderr, "mutants: -check-edited-killers requires -base REV")
		return 2
	}
	var sel *regexp.Regexp
	if selectRE != "" {
		var err error
		if sel, err = regexp.Compile(selectRE); err != nil {
			fmt.Fprintf(stderr, "mutants: -run: %v\n", err)
			return 2
		}
	}
	changed, err := gitChangedFiles(cfg.Root, base)
	if err != nil {
		fmt.Fprintf(stderr, "mutants: -check-edited-killers: %v\n", err)
		return 2
	}
	edited, err := classifyEditedTestFiles(cfg.Root, base, changed)
	if err != nil {
		fmt.Fprintf(stderr, "mutants: -check-edited-killers: %v\n", err)
		return 2
	}
	if len(edited) == 0 {
		fmt.Fprintf(stdout, "edited-killers: 0 affected mutant rows (0 edited test/law files since %s)\n", base)
		return 0
	}
	ledgers, err := discoverLedgers(cfg.Root, scopeArgs)
	if err != nil {
		fmt.Fprintf(stderr, "mutants: -check-edited-killers: %v\n", err)
		return 2
	}
	affected, skipped, err := findAffectedMutants(cfg.Root, ledgers, edited, sel)
	if err != nil {
		fmt.Fprintf(stderr, "mutants: -check-edited-killers: %v\n", err)
		return 2
	}
	for _, s := range skipped {
		fmt.Fprintf(stdout, "skipped\t%s\t%s\t%s\t(target file deleted or renamed)\n", s.LedgerRel, s.Mutant.Name, s.Mutant.File)
	}
	if len(affected) == 0 {
		fmt.Fprintf(stdout, "edited-killers: 0 affected mutant rows (%d edited test/law files, %d skipped)\n", len(edited), len(skipped))
		return 0
	}
	if dryRun {
		for _, a := range affected {
			for _, h := range a.Hits {
				fmt.Fprintf(stdout, "%s\t%s\t%s\tedited=%s\tkiller=%s\n", a.LedgerRel, a.Mutant.Name, a.Mutant.File, h.EditedFile, h.KillerID)
			}
		}
		fmt.Fprintf(stdout, "edited-killers: %d affected mutant rows (%d edited test/law files, %d skipped)\n", len(affected), len(edited), len(skipped))
		return 0
	}

	if cfg.Work == "" {
		var done func()
		if cfg.Work, done, err = privateWork(); err != nil {
			fmt.Fprintf(stderr, "mutants: %v\n", err)
			return 2
		}
		defer done()
	}
	cfg.Sources = ergoSources
	cfg.CoverStore = ""
	cfg.JournalDir = ""

	jobs := jobsFromAffected(affected)
	start := time.Now()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer cancel()

	ergoBin := resolveErgoBinary(cfg.Root, ergoFlag, noNativeErgo)
	var delJobs []Job
	goJudge := jobs
	if ergoBin != "" {
		delJobs, goJudge = partitionDelegatedJobs(cfg.Root, cfg, jobs)
	}
	var delResults []Result
	if len(delJobs) > 0 {
		var fallback []Job
		delResults, fallback, err = runDelegatedErgoJobs(ctx, ergoBin, cfg.Root, true, delJobs, stderr)
		if err != nil {
			fmt.Fprintf(stderr, "mutants: -check-edited-killers: %v\n", err)
			return 2
		}
		if len(fallback) > 0 {
			goJudge = append(goJudge, fallback...)
		}
	}
	var goResults []Result
	var runErr error
	if len(goJudge) > 0 {
		if cfg.Cover, cfg.Tools, cfg.Suites, err = suitesFor(cfg.Root, goJudge); err != nil {
			fmt.Fprintf(stderr, "mutants: -check-edited-killers: %v\n", err)
			return 2
		}
		goResults, _, runErr = Run(ctx, cfg, goJudge)
		if goResults == nil {
			fmt.Fprintf(stderr, "mutants: -check-edited-killers: %v\n", runErr)
			return 2
		}
	}
	results := inLedgerOrder(jobs, append(delResults, goResults...))
	code := reportEditedKillers(stdout, affected, skipped, results)
	fmt.Fprintf(stdout, "time: %s\n", time.Since(start).Round(time.Second))
	if runErr != nil {
		fmt.Fprintf(stderr, "mutants: -check-edited-killers: %v\n", runErr)
		return 2
	}
	return code
}

func jobsFromAffected(affected []affectedMutant) []Job {
	byLedger := map[string]*Job{}
	var order []string
	namesByLedger := map[string][]string{}
	for _, a := range affected {
		j, ok := byLedger[a.LedgerPath]
		if !ok {
			j = &Job{
				Path: a.LedgerPath,
				Ledger: &Ledger{
					About:    a.Ledger.About,
					Env:      slices.Clone(a.Ledger.Env),
					Commands: a.Ledger.Commands,
				},
			}
			byLedger[a.LedgerPath] = j
			order = append(order, a.LedgerPath)
		}
		j.Ledger.Mutants = append(j.Ledger.Mutants, a.Mutant)
		namesByLedger[a.LedgerPath] = append(namesByLedger[a.LedgerPath], regexp.QuoteMeta(a.Mutant.Name))
	}
	jobs := make([]Job, 0, len(order))
	for _, p := range order {
		j := byLedger[p]
		j.Select = regexp.MustCompile("^(" + strings.Join(namesByLedger[p], "|") + ")$")
		jobs = append(jobs, *j)
	}
	return jobs
}

func reportEditedKillers(w io.Writer, affected, skipped []affectedMutant, results []Result) int {
	byKey := make(map[string]Result, len(results))
	for _, r := range results {
		byKey[r.Ledger+"\x00"+r.Name] = r
	}
	killed, failed, gaps := 0, 0, 0
	var failures []string
	for _, a := range affected {
		r, ok := byKey[a.LedgerPath+"\x00"+a.Mutant.Name]
		status := r.Status
		if !ok || status == "" {
			status = Unjudged
		}
		where := a.Mutant.File
		if r.Line > 0 {
			where = fmt.Sprintf("%s:%d", a.Mutant.File, r.Line)
		}
		line := fmt.Sprintf("%-9s %s  %s", status, a.Mutant.Name, where)
		if r.Killer != "" {
			line += fmt.Sprintf("  by %s (%s)", r.Killer, r.Reason)
		}
		fmt.Fprintln(w, line)
		if status == Killed {
			killed++
			continue
		}
		if (status == Survived || status == Uncovered) && a.Mutant.Gap != "" {
			gaps++
			continue
		}
		failed++
		var editedParts []string
		for _, h := range a.Hits {
			editedParts = append(editedParts, fmt.Sprintf("%s (killer %s)", h.EditedFile, h.KillerID))
		}
		detail := ""
		if d := firstLine(r.Detail); d != "" {
			detail = "; detail: " + d
		}
		failures = append(failures, fmt.Sprintf("%s [%s] file=%s rule=%q edited=%s%s",
			a.Mutant.Name, status, a.Mutant.File, a.Mutant.Rule, strings.Join(editedParts, ", "), detail))
	}
	section(w, "EDITED-KILLER FAILURES (assertion weakened, edit stale, or mutant invalid)", failures)
	fmt.Fprintf(w, "edited-killers: %d judged, %d killed, %d failed, %d known gaps (%d skipped)\n",
		len(affected), killed, failed, gaps, len(skipped))
	if failed > 0 {
		return 1
	}
	return 0
}

func discoverLedgers(root string, scopeArgs []string) ([]string, error) {
	root = filepath.Clean(root)
	var out []string
	add := func(p string) {
		p = filepath.Clean(p)
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	walkDir := func(dir string) (int, error) {
		found := 0
		err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if p != dir && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules" || d.Name() == "tmp") {
					return filepath.SkipDir
				}
				return nil
			}
			if d.Name() == "mutants.json" && filepath.Base(filepath.Dir(p)) == "testdata" {
				add(p)
				found++
			}
			return nil
		})
		return found, err
	}
	if len(scopeArgs) == 0 {
		if _, err := walkDir(root); err != nil {
			return nil, err
		}
		slices.Sort(out)
		return out, nil
	}
	for _, arg := range scopeArgs {
		if strings.HasSuffix(arg, ".json") || arg == "check" || arg == "lower" {
			add(ledgerPath(root, arg))
			continue
		}
		candidate := arg
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(root, filepath.FromSlash(arg))
		}
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			n, err := walkDir(candidate)
			if err != nil {
				return nil, err
			}
			if n == 0 {
				add(ledgerPath(root, candidate))
			}
			continue
		}
		add(ledgerPath(root, arg))
	}
	slices.Sort(out)
	return out, nil
}

func gitChangedFiles(root, base string) ([]string, error) {
	cmd := exec.Command("git", "--no-optional-locks", "-C", root, "diff", "--no-renames", "--name-only", "-z", base, "--")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git diff %s: %w: %s", base, err, strings.TrimSpace(stderr.String()))
	}
	raw := stdout.Bytes()
	if len(raw) == 0 {
		return nil, nil
	}
	parts := bytes.Split(raw, []byte{0})
	var files []string
	for _, p := range parts {
		if len(p) == 0 {
			continue
		}
		files = append(files, filepath.ToSlash(string(p)))
	}
	slices.Sort(files)
	return files, nil
}

func gitShowFile(root, base, rel string) ([]byte, bool) {
	cmd := exec.Command("git", "--no-optional-locks", "-C", root, "show", "--no-ext-diff", base+":"+rel)
	out, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	return out, true
}

func gitDiffHunks(root, base, rel string) (oldRanges, newRanges []lineRange, err error) {
	cmd := exec.Command("git", "--no-optional-locks", "-C", root, "diff", "--no-renames", "-U0", base, "--", rel)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, nil, fmt.Errorf("git diff -U0 %s -- %s: %w: %s", base, rel, err, strings.TrimSpace(stderr.String()))
	}
	return parseDiffHunks(stdout.String())
}

func parseDiffHunks(diffText string) (oldRanges, newRanges []lineRange, err error) {
	for _, line := range strings.Split(diffText, "\n") {
		m := diffHunkRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		oldStart, _ := strconv.Atoi(m[1])
		oldCount := 1
		if m[2] != "" {
			oldCount, _ = strconv.Atoi(m[2])
		}
		newStart, _ := strconv.Atoi(m[3])
		newCount := 1
		if m[4] != "" {
			newCount, _ = strconv.Atoi(m[4])
		}
		if oldCount > 0 {
			oldRanges = append(oldRanges, lineRange{Start: oldStart, End: oldStart + oldCount - 1})
		}
		if newCount > 0 {
			newRanges = append(newRanges, lineRange{Start: newStart, End: newStart + newCount - 1})
		}
	}
	return oldRanges, newRanges, nil
}

func classifyEditedTestFiles(root, base string, changed []string) ([]editedTestFile, error) {
	var out []editedTestFile
	for _, rel := range changed {
		switch {
		case strings.HasSuffix(rel, "_test.go"):
			ef, ok, err := classifyEditedGoTest(root, base, rel)
			if err != nil {
				return nil, err
			}
			if ok {
				out = append(out, ef)
			}
		case strings.HasSuffix(rel, ".ergo"):
			if ef, ok := classifyEditedErgoTest(root, base, rel); ok {
				out = append(out, ef)
			}
		}
	}
	return out, nil
}

func slashDir(rel string) string {
	d := path.Dir(rel)
	if d == "." {
		return ""
	}
	return d
}

func classifyEditedGoTest(root, base, rel string) (editedTestFile, bool, error) {
	ef := editedTestFile{
		File:   rel,
		PkgDir: slashDir(rel),
		Kind:   editedGo,
	}
	newSrc, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			ef.WholePackage = true
			return ef, true, nil
		}
		return editedTestFile{}, false, err
	}
	oldSrc, _ := gitShowFile(root, base, rel)
	oldRanges, newRanges, err := gitDiffHunks(root, base, rel)
	if err != nil {
		return editedTestFile{}, false, err
	}
	editedTests, wholePkg := analyzeGoTestEdits(oldSrc, newSrc, oldRanges, newRanges)
	if !wholePkg && len(editedTests) == 0 {
		return editedTestFile{}, false, nil
	}
	ef.WholePackage = wholePkg
	ef.EditedTests = editedTests
	return ef, true, nil
}

type goTestSpan struct {
	Name  string
	Start int
	End   int
}

func analyzeGoTestEdits(oldSrc, newSrc []byte, oldRanges, newRanges []lineRange) (editedTests map[string]bool, wholePackage bool) {
	editedTests = map[string]bool{}
	inspect := func(src []byte, ranges []lineRange) (tests map[string]bool, ok bool) {
		if len(src) == 0 {
			return nil, true
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "test.go", src, parser.ParseComments)
		if err != nil {
			wholePackage = true
			return nil, false
		}
		tests = map[string]bool{}
		var testSpans []goTestSpan
		var ignoreSpans []lineRange
		if f.Package.IsValid() {
			pLine := fset.Position(f.Package).Line
			ignoreSpans = append(ignoreSpans, lineRange{Start: pLine, End: pLine})
		}
		for _, d := range f.Decls {
			switch decl := d.(type) {
			case *ast.FuncDecl:
				if decl.Recv == nil && isGoTestFunc(decl.Name.Name) {
					start := fset.Position(decl.Pos()).Line
					end := fset.Position(decl.End()).Line
					testSpans = append(testSpans, goTestSpan{Name: decl.Name.Name, Start: start, End: end})
					tests[decl.Name.Name] = true
				}
			case *ast.GenDecl:
				if decl.Tok == token.IMPORT {
					ignoreSpans = append(ignoreSpans, lineRange{
						Start: fset.Position(decl.Pos()).Line,
						End:   fset.Position(decl.End()).Line,
					})
				}
			}
		}
		lines := strings.Split(string(src), "\n")
		isIgnorableLine := func(lineNo int) bool {
			for _, sp := range ignoreSpans {
				if lineNo >= sp.Start && lineNo <= sp.End {
					return true
				}
			}
			if lineNo < 1 || lineNo > len(lines) {
				return true
			}
			trimmed := strings.TrimSpace(lines[lineNo-1])
			return trimmed == "" || strings.HasPrefix(trimmed, "//")
		}
		for _, r := range ranges {
			for lineNo := r.Start; lineNo <= r.End; lineNo++ {
				matchedTest := false
				for _, sp := range testSpans {
					if lineNo >= sp.Start && lineNo <= sp.End {
						editedTests[sp.Name] = true
						matchedTest = true
						break
					}
				}
				if matchedTest || isIgnorableLine(lineNo) {
					continue
				}
				wholePackage = true
			}
		}
		return tests, true
	}

	oldTests, oldOK := inspect(oldSrc, oldRanges)
	newTests, newOK := inspect(newSrc, newRanges)
	if oldOK && newOK {
		for name := range oldTests {
			if !newTests[name] {
				editedTests[name] = true
			}
		}
	}
	return editedTests, wholePackage
}

func isGoTestFunc(name string) bool {
	return strings.HasPrefix(name, "Test") && name != "TestMain"
}

func classifyEditedErgoTest(root, base, rel string) (editedTestFile, bool) {
	baseName := path.Base(rel)
	isTestName := strings.HasSuffix(baseName, "_test.ergo") ||
		strings.HasSuffix(baseName, "_tests.ergo") ||
		strings.Contains(strings.ToLower(baseName), "laws")
	if !isTestName {
		curSrc, curErr := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		oldSrc, oldOK := gitShowFile(root, base, rel)
		if (curErr != nil || !hasErgoTestOrLawDecl(curSrc)) && (!oldOK || !hasErgoTestOrLawDecl(oldSrc)) {
			return editedTestFile{}, false
		}
	}
	return editedTestFile{
		File:         rel,
		PkgDir:       slashDir(rel),
		Kind:         editedErgo,
		WholePackage: true,
	}, true
}

func hasErgoTestOrLawDecl(src []byte) bool {
	return ergoTestOrLawDeclRE.Match(src)
}

func findAffectedMutants(root string, ledgers []string, edited []editedTestFile, sel *regexp.Regexp) (affected, skipped []affectedMutant, err error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, nil, err
	}
	for _, lp := range ledgers {
		l, err := readLedger(root, lp, nil)
		if err != nil {
			return nil, nil, err
		}
		relLedger, err := filepath.Rel(absRoot, lp)
		if err != nil {
			relLedger = lp
		}
		relLedger = filepath.ToSlash(relLedger)
		for _, m := range l.Mutants {
			if sel != nil && !sel.MatchString(m.Name) {
				continue
			}
			var hits []editedKillerHit
			for _, id := range m.Killers {
				for _, h := range matchKiller(id, edited) {
					if !slices.Contains(hits, h) {
						hits = append(hits, h)
					}
				}
			}
			if len(hits) == 0 {
				continue
			}
			row := affectedMutant{
				LedgerPath: lp,
				LedgerRel:  relLedger,
				Ledger:     l,
				Mutant:     m,
				Hits:       hits,
			}
			if _, statErr := os.Stat(filepath.Join(absRoot, filepath.FromSlash(m.File))); errors.Is(statErr, os.ErrNotExist) {
				skipped = append(skipped, row)
				continue
			}
			affected = append(affected, row)
		}
	}
	return affected, skipped, nil
}

func matchKiller(killerID string, edited []editedTestFile) []editedKillerHit {
	suite, testName := splitID(killerID)
	suiteDir, _, _ := strings.Cut(suite, "[")
	var hits []editedKillerHit
	for _, ef := range edited {
		matched := false
		switch ef.Kind {
		case editedGo:
			if suiteDir == ef.PkgDir {
				matched = ef.WholePackage || matchesGoTestName(testName, ef.EditedTests)
			}
		case editedErgo:
			if suiteDir == ef.PkgDir || (suite == "examples" && exampleMatchesErgoPath(testName, ef.File)) {
				matched = true
			}
		}
		if matched {
			hits = append(hits, editedKillerHit{EditedFile: ef.File, KillerID: killerID})
		}
	}
	return hits
}

func matchesGoTestName(killerTest string, editedTests map[string]bool) bool {
	if editedTests[killerTest] {
		return true
	}
	for name := range editedTests {
		if m := familyPartSuffixRE.FindStringSubmatch(name); m != nil && m[1] == killerTest {
			return true
		}
	}
	return false
}

func exampleMatchesErgoPath(exampleTarget, relPath string) bool {
	if mod, ok := strings.CutPrefix(exampleTarget, "modules/"); ok {
		return relPath == "modules/"+mod || strings.HasPrefix(relPath, "modules/"+mod+"/")
	}
	return relPath == "examples/"+exampleTarget || strings.HasPrefix(relPath, "examples/"+exampleTarget+"/")
}
