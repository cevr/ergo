package main

// Guard packages run whole in every tier (GATE-REACH). A guard is a test
// package that reads the checkout to hold a written rule: internal/residue
// (every tracked file), internal/sketch/stdlib (every std source). Until
// GATE-REACH the fast tier cut such a package's tests by size and by the
// coverage selection, and a landing that ran only focused tests never ran
// them: 15 tests were red on main 5cce5f8c6. "Whole" is every test the
// package runs under -short: no size cut and no selection. -short stays,
// so a test that builds Go still skips (needsBuild), and a guard never
// builds a binary in the fast tier.
//
// The rule, never a list in the gate: the closure table grants the package
// the whole checkout (replay.GrantSubtree "."), or a test file of the
// package declares itself one with a line `//gate:whole <reason>`. A
// declaration must give its reason, must sit in a package none of whose
// tests builds Go (needsBuild first), and must stay within
// wholeTestLimit Test functions, so the marker is no way for a heavy
// package into every gate.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strings"

	"ergo.local/prototype/scripts/gate/replay"
)

// wholeMarker opens the line that declares a guard package.
const wholeMarker = "//gate:whole"

// wholeNote starts the note of a step that runs its package whole.
const wholeNote = "whole: "

// wholeTestLimit is the most Test functions a declared guard package may
// have: the counted bound of its whole run (internal/sketch/stdlib had 55
// on 2026-11-22). A guard by grant is held by its grant's review instead.
const wholeTestLimit = 80

// wholeOf is why the fast tier runs p whole, or "" when it does not: the
// grant of the checkout, or the reason p's own marker gives. A table the
// gate cannot read grants nothing (testPackage.unread).
func wholeOf(root string, p testPackage) (string, error) {
	if p.closure != nil && !p.unread && slices.Contains(p.closure.Declared, replay.Allowance{Path: ".", Grant: replay.GrantSubtree}) {
		return "the closure table grants it the whole checkout", nil
	}
	dir := filepath.Join(root, filepath.FromSlash(p.dir))
	files, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
	if err != nil {
		return "", err
	}
	reason, tests, building := "", 0, ""
	fset := token.NewFileSet()
	for _, file := range files {
		f, err := parser.ParseFile(fset, file, nil, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			return "", err
		}
		for _, group := range f.Comments {
			for _, c := range group.List {
				rest, ok := strings.CutPrefix(c.Text, wholeMarker)
				if !ok || (rest != "" && rest[0] != ' ') {
					continue
				}
				if strings.TrimSpace(rest) == "" {
					return "", fmt.Errorf("%s: %s gives no reason: say what the package reads that every gate must check", fset.Position(c.Pos()), wholeMarker)
				}
				reason = strings.TrimSpace(rest)
			}
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "Test") || fn.Name.Name == "TestMain" {
				continue
			}
			tests++
			if fn.Body != nil && len(fn.Body.List) > 0 && callsNeedsBuild(fn.Body.List[0]) && building == "" {
				building = fn.Name.Name
			}
		}
	}
	if reason == "" {
		return "", nil
	}
	if building != "" {
		return "", fmt.Errorf("%s declares %s, but %s builds Go (needsBuild): a guard package reads sources and builds nothing", p.dir, wholeMarker, building)
	}
	if tests > wholeTestLimit {
		return "", fmt.Errorf("%s declares %s with %d Test functions, over wholeTestLimit (%d): split the guard, or leave it to the size rule", p.dir, wholeMarker, tests, wholeTestLimit)
	}
	return reason, nil
}

// guardsOnly is -guards: the fast tier keeps only the guard packages'
// steps and what they need, the check a landing runs when it runs no tier
// (an integrator's focused legs).
var guardsOnly bool

// guardsTier names a -guards run: its own tier in the whole-gate key and
// in the reds records, so it never replays or stands for a fast tier.
const guardsTier = "fast guards"

// guardSteps are the steps of steps that run a guard package whole, with
// every step they need, in their order.
func guardSteps(steps []*Step) []*Step {
	byName := map[string]*Step{}
	for _, step := range steps {
		byName[step.Name] = step
	}
	keep := map[string]bool{}
	var mark func(name string)
	mark = func(name string) {
		if keep[name] || byName[name] == nil {
			return
		}
		keep[name] = true
		for _, need := range byName[name].Needs {
			mark(need)
		}
	}
	for _, step := range steps {
		if strings.HasPrefix(step.Note, "whole: ") {
			mark(step.Name)
		}
	}
	return slices.DeleteFunc(slices.Clone(steps), func(step *Step) bool { return !keep[step.Name] })
}
