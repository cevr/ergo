package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/testwait"
)

// Every exit path of a command stops the process group the runner started
// for it: a pass, a failure, a timeout, the cap, a build that fails after it
// started a child, a failing setup.sh, a failing test leg and a recorded
// original. Each row's command starts a background ticker; after the run the
// ticker must not grow.
func TestEveryExitPathStopsTheGroup(t *testing.T) {
	t.Parallel()
	tick := ticker(`"$TICKS"`)
	rows := []struct {
		name string
		// make writes the fixture and returns the runner's arguments and the
		// ticks file of the ticker it starts.
		make func(f *fixture, ticks string) []string
		code int
		want string // a line of the output starts with this
	}{
		{"pass", func(f *fixture, ticks string) []string {
			f.program("p", "#!/bin/sh\n"+tick+"echo done\n", "slow\n", map[string]map[string]string{"basic": {"env": "TICKS=" + ticks + "\n", "expected.out": "done\n"}}, nil)
			return []string{"-timeout", caseTimeout(f.t), "p"}
		}, 0, "ok p/basic ("},
		{"fail", func(f *fixture, ticks string) []string {
			f.program("p", "#!/bin/sh\n"+tick+"echo done\n", "slow\n", map[string]map[string]string{"basic": {"env": "TICKS=" + ticks + "\n", "expected.out": "other\n"}}, nil)
			return []string{"-timeout", caseTimeout(f.t), "p"}
		}, 1, "FAIL p/basic: stdout differs"},
		{"timeout", func(f *fixture, ticks string) []string {
			f.program("p", "#!/bin/sh\n"+tick+caseSleep, "slow\n", map[string]map[string]string{"basic": {"env": "TICKS=" + ticks + "\n", "expected.out": ""}}, nil)
			return []string{"-timeout", "2s", "p"}
		}, 1, "FAIL p/basic: timed out after 2s"},
		{"cap", func(f *fixture, ticks string) []string {
			f.program("p", "#!/bin/sh\n"+tick+fmt.Sprintf("head -c %d /dev/zero\n", outputCap+1)+caseSleep, "slow\n", map[string]map[string]string{"basic": {"env": "TICKS=" + ticks + "\n", "expected.out": ""}}, nil)
			return []string{"-timeout", caseTimeout(f.t), "p"}
		}, 1, fmt.Sprintf("FAIL p/basic: stdout over %d bytes", outputCap)},
		{"build", func(f *fixture, ticks string) []string {
			f.program("p", "#!/bin/sh\n# BUILDTICK\necho hi\n", "default\n", map[string]map[string]string{"basic": {"expected.out": "hi\n"}}, nil)
			return []string{"p"}
		}, 1, "FAIL p: build"},
		{"setup", func(f *fixture, ticks string) []string {
			f.program("p", "#!/bin/sh\necho hi\n", "default\n", map[string]map[string]string{"basic": {"env": "TICKS=" + ticks + "\n", "expected.out": "hi\n", "setup.sh": tick + "exit 3\n"}}, nil)
			return []string{"p"}
		}, 1, "FAIL p/basic: setup.sh exit 3"},
		{"test leg", func(f *fixture, ticks string) []string {
			f.program("p", "#!/bin/sh\necho hi\nexit 0\n# TESTTICK\ntest Fixture {\n}\n", "default\n", map[string]map[string]string{"basic": {"expected.out": "hi\n"}}, nil)
			return []string{"p"}
		}, 1, "FAIL p: test"},
		{"record", func(f *fixture, ticks string) []string {
			f.program("p", "#!/bin/sh\necho port\n", "default\n", map[string]map[string]string{"basic": {"env": "TICKS=" + ticks + "\n", "expected.out": "stale\n"}},
				map[string]string{"record.sh": tick + "echo orig\n", "origin": "a fixture original\n"})
			return []string{"-record", "p"}
		}, 0, "recorded p/basic"},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			ticks := filepath.Join(t.TempDir(), "ticks")
			switch row.name {
			case "build":
				ticks = filepath.Join(filepath.Dir(f.ergo), "ticks-build")
			case "test leg":
				ticks = filepath.Join(filepath.Dir(f.ergo), "ticks-test")
			}
			code, out := f.run(row.make(f, ticks)...)
			if code != row.code || !strings.Contains("\n"+out, "\n"+row.want) {
				t.Fatalf("want exit %d and a line %q; exit %d\n%.2000s", row.code, row.want, code, out)
			}
			if grows(t, ticks) {
				t.Fatal("the command's background child still runs after the run")
			}
		})
	}
}

// A panic on the runner's goroutine while a case runs reaches run's caller,
// and the deferred stop in execute stops the case's group as it unwinds.
// Not parallel: it sets testHookStarted, which every execute reads.
func TestAPanicStopsTheGroup(t *testing.T) {
	f := newFixture(t)
	ticks := filepath.Join(t.TempDir(), "ticks")
	f.program("panicky", "#!/bin/sh\n"+ticker(`"$TICKS"`)+caseSleep, "slow\n",
		map[string]map[string]string{"basic": {"env": "TICKS=" + ticks + "\n", "expected.out": ""}}, nil)
	started := false
	testHookStarted = func(argv []string) {
		if filepath.Base(argv[0]) != "panicky" {
			return
		}
		started = true
		testwait.Until(t, func() bool { info, err := os.Stat(ticks); return err == nil && info.Size() > 0 })
		panic("injected")
	}
	defer func() { testHookStarted = nil }()
	recovered := func() (r any) {
		defer func() { r = recover() }()
		f.run("-timeout", caseTimeout(t), "panicky")
		return nil
	}()
	if !started || recovered != "injected" {
		t.Fatalf("want the injected panic at run's caller; hook saw the case %t, recovered %v", started, recovered)
	}
	if grows(t, ticks) {
		t.Fatal("after a panic in the runner the case's background child still runs")
	}
}
