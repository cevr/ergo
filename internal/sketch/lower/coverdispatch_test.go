package lower

import (
	"bytes"
	"os"
	"os/exec"
	"strings"

	"ergo.local/prototype/internal/sketch/build"
)

// coverGo is the build package's CoverVariable in the mutant runner's
// coverage pass (GATE2 GT7b), or "": TestMain reads it, and with it every
// unit is lowered with cover marks (coverMarksAll).
var coverGo string

// coverDispatcherSource is the dispatcher's file of a coverage link: it
// imports the coverage copy's covdir package, whose init gives a program
// started with no GOCOVERDIR its nearest ancestor's (the running test's),
// and unsets what covdir set, so the program sees the environment it was
// given (internal/sketch/build, cover.go).
const coverDispatcherSource = `package main

import (
	"os"

	"ergo.local/program/covdir"
)

func init() {
	if covdir.Set {
		os.Unsetenv("GOCOVERDIR")
	}
}
`

// coverDispatch is buildDispatch's link in the coverage pass: the
// dispatcher of m's coverage copy (build.CoverCopy, under coverGo), linked
// with -cover over the programs' packages, so their counters land in the
// running test's GOCOVERDIR and the runner maps each block, through the
// copy's //line directives and cover marks, to the .ergo lines it came
// from. It builds Go because buildDispatch links the programs a test runs.
func coverDispatch(m *goModuleDir, dispatcher string) bool {
	m.t.Helper()
	m.write("cmd/dispatch/ergo_cover.go", []byte(coverDispatcherSource))
	dir, importPath, err := build.CoverCopy(coverGo, m.root)
	if err != nil {
		m.t.Errorf("coverage copy: %v", err)
		return false
	}
	// The main package is covered too: only an instrumented main package
	// installs the cover runtime's exit hook, so a binary whose main is not
	// covered writes no counters at all.
	args := append([]string{"build", "-trimpath"}, strings.Fields(build.CoverFlags)...)
	args = append(args, "-coverpkg="+importPath+"/p/...,"+importPath+"/cmd/dispatch", "-o", dispatcher, "./cmd/dispatch")
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		m.report("build", out.String())
		return false
	}
	return true
}
