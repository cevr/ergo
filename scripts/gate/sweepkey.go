package main

import (
	"fmt"
	"go/scanner"
	"go/token"
	"io"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"ergo.local/prototype/scripts/gate/replay"
)

// The incremental closure sweep (GATE2 GT1). A full-tier sweep step replays
// its last pass while the key of everything the sweep reads for its package
// is unchanged, as a fast-tier test step does (Step.Package,
// replay.PackageKey); --no-cache, and the merge tier's uncached run,
// sweep every package again: the reference. The sweep of a package reads:
//
//   - the package's closure: the test binary's sources and embeds, its
//     testdata and its declared inputs, with the programs it builds. A pass
//     means every input its tests recorded lies there, so while none of it
//     changes the tests read the same inputs (as Go's test cache assumes).
//   - the sweeper's own closure: internal/closure, which holds the closure
//     table, the sandbox, the classifier and the std store, and the test
//     binary it runs as.
//   - which of the directories internal/closure's source names are test
//     packages: the sweep of any one package fails when a package its
//     tables name is not a test package of the module (sweepTableNames).
//
// The gate executable, the toolchain and host fingerprints, the step's
// environment and the tier are in every package key. The recorded test
// times (durations.json, test-cpu.json) are not: they split a package's
// runs and set their deadlines, and decide no verdict but a hang's.

// sweeperDir is the directory of the package the sweep runs as.
var sweeperDir = strings.TrimPrefix(sweepPackage, "./")

// keySweeps gives each closure sweep step of steps its package key's
// closure (sweepClosure) and the test-package facts of the sweeper's tables
// (sweepTableNames, over names, the string literals of the sweeper's
// source). A sweep of a directory that is no listed test package keeps no
// key and always runs.
func keySweeps(steps []*Step, tested []testPackage, problem string, names []string) {
	byDir := map[string]*replay.Closure{}
	for _, p := range tested {
		byDir[p.dir] = p.closure
	}
	sweeper, ok := byDir[sweeperDir]
	if !ok {
		// No sweeper (a module that is not this one): every sweep runs.
		return
	}
	parts := []string{"sweep tables name test packages " + strings.Join(sweepTableNames(names, byDir), " ")}
	for _, step := range steps {
		if step.Sweep == "" {
			continue
		}
		swept, ok := byDir[step.Sweep]
		if !ok {
			continue
		}
		step.Package = sweepClosure(swept, sweeper)
		step.PackageProblem = problem
		step.KeyParts = parts
	}
}

// sweepClosure is the closure of what the sweep of swept reads: swept's
// closure with the sweeper's (its sources, embeds, testdata, declared
// inputs and programs) added. Its Dir stays swept's, whose testdata it
// covers; the sweeper's testdata is a declared subtree.
func sweepClosure(swept, sweeper *replay.Closure) *replay.Closure {
	union := &replay.Closure{
		Dir:    swept.Dir,
		Deps:   append(slices.Clone(swept.Deps), sweeper.Deps...),
		Embeds: append(slices.Clone(swept.Embeds), sweeper.Embeds...),
	}
	union.Declared = append(append(slices.Clone(swept.Declared), sweeper.Declared...), replay.Allowance{Path: path.Join(sweeper.Dir, "testdata"), Grant: replay.GrantSubtree})
	if len(swept.Programs)+len(sweeper.Programs) > 0 {
		union.Programs = map[string]*replay.Closure{}
		maps.Copy(union.Programs, sweeper.Programs)
		maps.Copy(union.Programs, swept.Programs)
	}
	return union
}

// sweepTableNames are the test packages among names, sorted: each name of
// the sweeper's tables that is a test package now. A table that names a
// directory which stops being a test package changes the list, and with it
// the key of every sweep, as it fails every sweep.
func sweepTableNames(names []string, tested map[string]*replay.Closure) []string {
	var found []string
	for _, name := range names {
		if _, ok := tested[name]; ok {
			found = append(found, name)
		}
	}
	slices.Sort(found)
	return slices.Compact(found)
}

// sweeperNames are the string literals of the Go files in the sweeper's
// directory under root: the directories its tables name are among them.
func sweeperNames(root string) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(sweeperDir), "*.go"))
	if err != nil {
		return nil, err
	}
	var names []string
	for _, file := range files {
		source, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		names = append(names, stringLiterals(source)...)
	}
	return names, nil
}

// stringLiterals are the values of the string literals of a Go source.
func stringLiterals(source []byte) []string {
	var s scanner.Scanner
	files := token.NewFileSet()
	s.Init(files.AddFile("", files.Base(), len(source)), source, nil, 0)
	var literals []string
	for {
		_, tok, lit := s.Scan()
		if tok == token.EOF {
			return literals
		}
		if tok != token.STRING {
			continue
		}
		if value, err := strconv.Unquote(lit); err == nil {
			literals = append(literals, value)
		}
	}
}

// keySweepSteps keys the closure sweep steps of a run that has no whole-gate
// key, the merge tier's uncached run (gate.sweepCollect; GATE2 GT5): each
// replays a pass of an earlier merge run while its package key, made from
// the merge tier's facts at the start, is unchanged, and records its pass
// (recordPackages). The merge tier checks a clean checkout, whose sources
// are the export its sandbox runs; ignored files of the checkout in a
// sweep's closure are keyed too, which can only make the key miss. Keys
// name the merge tier (replay.WithTier), so a full-tier pass never stands
// for a merge one, nor the reverse. --no-cache, the reference, sweeps every
// package.
func (g *gate) keySweepSteps(out io.Writer) {
	facts, err := g.sweepCollect()
	if err != nil {
		fmt.Fprintf(out, "gate: no closure sweep key: %v\n", err)
		return
	}
	g.packageKeys = map[*Step]packageKey{}
	for _, step := range g.steps {
		if step.Sweep == "" || step.Package == nil {
			continue
		}
		key, problem := replay.PackageKey(facts, step.Package, packageIdentity(step)...)
		if step.PackageProblem != "" && problem == "" {
			fmt.Fprintf(out, "gate: %s: %s\n", step.Name, step.PackageProblem)
		}
		g.packageKeys[step] = packageKey{key: key, problem: problem}
	}
}

// fullSteps are the full tier's steps, each closure sweep keyed (keySweeps).
func fullSteps(root string) ([]*Step, error) {
	steps, err := gateSteps(root)
	if err != nil || !sweepSupported() {
		return steps, err
	}
	tested, problem, err := testPackages(root)
	if err != nil {
		return nil, err
	}
	names, err := sweeperNames(root)
	if err != nil {
		return nil, err
	}
	keySweeps(steps, tested, problem, names)
	return steps, nil
}
