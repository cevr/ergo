package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"ergo.local/prototype/scripts/gate/replay"
	"ergo.local/prototype/scripts/mutants/anchor"
	"ergo.local/prototype/scripts/mutants/covfile"
	"ergo.local/prototype/scripts/mutants/suitename"
)

// The coverage store's backtest (CS-ROUND): the round tier's selection,
// for every row of every ledger whose edit is Go source and whose killers
// are Go tests of a suite the store holds at a commit, holds every named
// killer or runs every test. It reads the store and the ledgers at the
// commit and starts no test. On the workbox, where the store lives, the
// full tier runs it as the step "coverage store backtest" (`go run
// ./scripts/gate -backtest`), with the replay of the round tier over the
// tips from that commit to HEAD; it fails on a missing store, never passes
// empty.

// backtestReport is what a backtest found.
type backtestReport struct {
	Commit               string
	Rows, Killers, Stale int
	Missed               []string
	Median               float64
}

func (r backtestReport) String() string {
	return fmt.Sprintf("backtest at %s: %d rows, %d killers, %d missed, median %.0f%% of a package selected (%d rows stale at the commit)", short(r.Commit), r.Rows, r.Killers, len(r.Missed), 100*r.Median, r.Stale)
}

// backtestLedgers runs the backtest at commit over the checkout at root,
// with the closures of its test packages by "./DIR".
func backtestLedgers(store covfile.Store, root, commit string, closures map[string]*replay.Closure) (backtestReport, error) {
	report := backtestReport{Commit: commit}
	entries, err := store.At(commit)
	if err != nil {
		return report, err
	}
	files, err := gitText(root, "ls-tree", "-r", "--name-only", commit)
	if err != nil {
		return report, err
	}
	var shares []float64
	segments := map[string]*covfile.Coverage{}
	paths := strings.Split(files, "\n")
	names := suitename.WithTestDirs(func(dir string) bool {
		return slices.ContainsFunc(paths, func(file string) bool {
			return filepath.ToSlash(filepath.Dir(file)) == dir &&
				(strings.HasSuffix(file, "_test.go") || strings.HasSuffix(file, ".ergo"))
		})
	})
	for _, ledger := range paths {
		if !strings.HasSuffix(ledger, "testdata/mutants.json") {
			continue
		}
		text, err := gitText(root, "show", commit+":"+ledger)
		if err != nil {
			return report, err
		}
		var l struct {
			Mutants []struct {
				Name, File, Decl, From, To string
				Killers                    []string
			}
		}
		if err := json.Unmarshal([]byte(text), &l); err != nil {
			return report, fmt.Errorf("%s: %w", ledger, err)
		}
		for _, row := range l.Mutants {
			if !strings.HasSuffix(row.File, ".go") {
				continue
			}
			old, err := exec.Command("git", "--no-optional-locks", "-C", root, "cat-file", "blob", commit+":"+row.File).Output()
			if err != nil {
				report.Stale++
				continue
			}
			mutated, err := anchor.Apply(row.File, old, row.Decl, row.From, row.To)
			if err != nil {
				report.Stale++
				continue
			}
			change := []fileChange{{Path: row.File, Old: old, New: mutated, Was: true, Is: true}}
			counted := false
			for _, id := range row.Killers {
				resolved, err := names.Resolve(id)
				if err != nil {
					return report, fmt.Errorf("%s: %s: %w", ledger, row.Name, err)
				}
				suite, test := suitename.Split(resolved)
				i := slices.IndexFunc(entries, func(e covfile.Entry) bool {
					return e.Suite == suite && e.Module == "" && e.Tags == "" && e.Package == "./"+suite
				})
				if i < 0 {
					// Historical indexes can still use the legacy name.
					i = slices.IndexFunc(entries, func(e covfile.Entry) bool {
						return e.Module == "" && e.Tags == "" && e.Package == "./"+suite
					})
				}
				if i < 0 {
					continue
				}
				e := entries[i]
				closure := closures[e.Package]
				if closure == nil {
					continue
				}
				seg := segments[e.Key]
				if seg == nil {
					if seg, ok = store.Load(e.Key); !ok {
						return report, fmt.Errorf("the index at %s names segment %s of %s, which the store does not hold", short(commit), e.Key, suite)
					}
					segments[e.Key] = seg
				}
				names := []string{test}
				for _, st := range seg.Tests {
					if s, n := suitename.Split(st.ID); s == e.Suite && n != test {
						names = append(names, n)
					}
				}
				pick := pickRound(e, seg, closure, change, names)
				report.Killers++
				counted = true
				if _, ok := pick.run[test]; pick.all == "" && !ok {
					report.Missed = append(report.Missed, row.Name+" "+id)
				}
				share := 1.0
				if pick.all == "" {
					share = float64(len(pick.run)) / float64(len(names))
				}
				shares = append(shares, share)
			}
			if counted {
				report.Rows++
			}
		}
	}
	if len(shares) > 0 {
		slices.Sort(shares)
		report.Median = shares[len(shares)/2]
	}
	if report.Rows == 0 {
		return report, fmt.Errorf("the coverage store at %s holds no suite a ledger's killer names: no backtest (go run ./scripts/mutants -measure)", short(commit))
	}
	return report, nil
}

// runBacktest is -backtest: the ledgers' backtest at the store's commit
// nearest to HEAD, then the round tier's replay from it to HEAD. It
// returns the exit code.
func runBacktest(out io.Writer) int {
	fail := func(err error) int {
		fmt.Fprintln(out, "gate:", err)
		return 1
	}
	root, err := repositoryRoot()
	if err != nil {
		return fail(err)
	}
	store, err := covfile.Open()
	if err != nil {
		return fail(err)
	}
	commit, err := backtestCommit(store, root)
	if err != nil {
		return fail(err)
	}
	packages, _, err := testPackages(root)
	if err != nil {
		return fail(err)
	}
	closures := map[string]*replay.Closure{}
	for _, p := range packages {
		closures["./"+p.dir] = p.closure
	}
	report, err := backtestLedgers(store, root, commit, closures)
	if err != nil {
		return fail(err)
	}
	fmt.Fprintln(out, report)
	code := 0
	if len(report.Missed) > 0 {
		fmt.Fprintf(out, "gate: the selection misses %d killers:\n%s\n", len(report.Missed), strings.Join(report.Missed, "\n"))
		code = 1
	}
	if missed, err := replayAffected(root, commit+"..HEAD", "", out); err != nil {
		return fail(err)
	} else if len(missed) > 0 {
		code = 1
	}
	return code
}

// storeIndexDigest is a digest of the store's index (every entry file's
// name and content), a key part of the backtest step: the step reads the
// store, which no checkout file holds.
func storeIndexDigest() string {
	store, err := covfile.Open()
	if err != nil {
		return "no store: " + err.Error()
	}
	h := sha256.New()
	files, _ := filepath.Glob(filepath.Join(store.Dir, "index", "*", "*.json"))
	slices.Sort(files)
	for _, f := range files {
		data, _ := os.ReadFile(f)
		fmt.Fprintf(h, "%s\x00%d\x00%s\x00", f, len(data), data)
	}
	return hex.EncodeToString(h.Sum(nil))
}
