package v1

import (
	"fmt"
	"os"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"weak"

	"ergo.local/prototype/runtime/rt"
)

// Fork-tree defects (docs/spec/runtime.md, G2; runtime-199 stage 4b): a
// defect stays attached to the task it happened in, with its stack, its
// chain and the context values in force there, across Wait, Stop, an
// owner's drain and a release; report alone writes it.

// boom is a defect at a known line of this file.
//
//go:noinline
func boom() int {
	var xs []int
	i := 3
	return xs[i]
}

// withSource registers this file as the program's source for the test, so
// report writes Ergo frames (ergoStack): each function of this file is a
// declaration covering the whole file, so a frame is named by its line.
func withSource(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	RegisterCalls([]struct {
		Name      string
		File      string
		Line, End int
	}{{Name: "src", File: file, Line: 1, End: 1 << 20}}, nil)
	t.Cleanup(func() { registered, decls, calls = false, nil, nil })
	return file
}

// lineOf is the line of file that holds text, which must hold it once.
func lineOf(t *testing.T, file, text string) int {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	at := 0
	for i, ln := range strings.Split(string(data), "\n") {
		if strings.Contains(ln, text) && !strings.Contains(ln, "lineOf(") {
			if at != 0 {
				t.Fatalf("%q is on two lines", text)
			}
			at = i + 1
		}
	}
	return at
}

// observe is a way the parent meets its child's exit.
type observe int

const (
	byWait observe = iota
	byStop
	byDrain
)

func (o observe) String() string { return [...]string{"Wait", "Stop", "drain"}[o] }

// forkDefect runs a root that binds a span, starts a child that binds its
// own annotation and panics in boom once released, then (before releasing
// it) opens a later binding and ends the span, and meets the child's exit
// by o. The child's report must hold its own snapshot only.
func forkDefect(root *rt.Task, o observe) error {
	return runOn(root, func(t *rt.Task) error {
		return frame(t, func(s *Scope) error {
			span := s.OpenContext("trace", "span", "ingest")
			release := make(chan struct{})
			c := Go(s, "task.Go at f.ergo:9:10", func(ct *rt.Task) (int, error) {
				return frameValue(ct, func(cs *Scope) (int, error) {
					inner := cs.OpenContext("log", "region", "inner")
					<-release
					v := boom()
					inner.End()
					return v, nil
				})
			})
			span.End()
			late := s.OpenContext("log", "late", "parent-later")
			close(release)
			defer late.End()
			switch o {
			case byWait:
				_, err := c.Wait()
				return err
			case byStop:
				c.Stop()
			}
			return nil // byDrain: the frame's end drains c
		})
	})
}

// TestDefectReportAcrossObservations: through Wait, Stop and an owner's
// drain, the defect reports the child's frames, its chain, and the context
// in force in the child at the defect: the inherited span and its own
// annotation, not the parent's later binding.
func TestDefectReportAcrossObservations(t *testing.T) {
	file := withSource(t)
	for _, o := range []observe{byWait, byStop, byDrain} {
		t.Run(o.String(), func(t *testing.T) {
			var b strings.Builder
			status := report(&b, forkDefect(rt.Root("main"), o))
			got := regexp.MustCompile(`#\d+`).ReplaceAllString(strings.ReplaceAll(b.String(), file, "F"), "#N")
			want := "panic: runtime error: index out of range [3] with length 0\n" +
				fmt.Sprintf("\tsrc at F:%d\n", lineOf(t, file, "return xs[i]")) +
				fmt.Sprintf("\tsrc at F:%d\n", lineOf(t, file, "v := boom()")) +
				"\ttask #N task.Go at f.ergo:9:10\n" +
				"\t  context trace.span=\"ingest\" log.region=\"inner\"\n" +
				"\ttask #N main\n"
			if status != 2 || got != want {
				t.Fatalf("status %d, report:\n%s\nwant:\n%s", status, got, want)
			}
		})
	}
}

// TestDefectReportFramesEndAtTheTaskBody: the Go body passed to Go, the
// lowered task.Go block's entry, is not reported as a call; the frames
// above it are, and nothing below it.
func TestDefectReportFramesEndAtTheTaskBody(t *testing.T) {
	file := withSource(t)
	err := runMain(func(t *rt.Task) error {
		return frame(t, func(s *Scope) error {
			c := Go(s, "task.Go at f.ergo:1:1", func(ct *rt.Task) (int, error) { return boom(), nil })
			_, err := c.Wait()
			return err
		})
	})
	rs := reasons(err)
	if len(rs) != 1 {
		t.Fatalf("exit %v; want one defect", err)
	}
	frames := ergoStack(rs[0].Value.(*Panic).Stack)
	// boom's line only: the closure at the Go call is the task's entry.
	if len(frames) != 1 || frames[0] != fmt.Sprintf("src at %s:%d", file, lineOf(t, file, "return xs[i]")) {
		t.Fatalf("frames %q; want boom's frame alone", frames)
	}
}

// TestDefectKeptWhenTypedFailureDropped: a task whose body fails and whose
// exit action panics exits with both; Stop and the owner's drain drop the
// abandoned typed failure and keep the defect, marked as an exit action's,
// with the task's chain.
func TestDefectKeptWhenTypedFailureDropped(t *testing.T) {
	for _, o := range []observe{byStop, byDrain} {
		t.Run(o.String(), func(t *testing.T) {
			ready := make(chan struct{})
			err := runMain(func(root *rt.Task) error {
				return frame(root, func(s *Scope) error {
					c := Go(s, "child", func(ct *rt.Task) (int, error) {
						return frameValue(ct, func(cs *Scope) (int, error) {
							OnExit(cs, func() { boom() })
							close(ready)
							return 0, bad{"abandoned"}
						})
					})
					<-ready
					if o == byStop {
						c.Stop()
					}
					return nil
				})
			})
			rs := reasons(err)
			if len(rs) != 1 || rs[0].Kind != rt.KindDefect || !rs[0].InRelease {
				t.Fatalf("exit %v; want the release defect alone", err)
			}
			if rs[0].Chain[0].Span != "child" {
				t.Fatalf("chain %+v; want the child first", rs[0].Chain)
			}
		})
	}
}

// TestDefectReportSameWhenTraced: a traced tree reports a defect as an
// untraced one does.
func TestDefectReportSameWhenTraced(t *testing.T) {
	withSource(t)
	norm := func(err error) string {
		var b strings.Builder
		report(&b, err)
		return regexp.MustCompile(`#\d+`).ReplaceAllString(b.String(), "#N")
	}
	for _, o := range []observe{byWait, byStop, byDrain} {
		untraced := norm(forkDefect(rt.Root("main"), o))
		traced := norm(forkDefect(rt.TracedRoot("main", &events{}), o))
		if traced != untraced {
			t.Fatalf("%s: traced report:\n%s\nuntraced:\n%s", o, traced, untraced)
		}
	}
}

// TestDefectAncestryBounded: a defect deep in a fork chain records a
// bounded chain, failing task first, and keeps no task alive: the chain is
// copied, so every task of the tree is collected while the exit is held.
func TestDefectAncestryBounded(t *testing.T) {
	const depth = 40
	var held []weak.Pointer[rt.Task]
	var fork func(ct *rt.Task, n int) (int, error)
	fork = func(ct *rt.Task, n int) (int, error) {
		held = append(held, weak.Make(ct))
		if n == depth {
			return boom(), nil
		}
		return frameValue(ct, func(s *Scope) (int, error) {
			c := Go(s, Site(fmt.Sprintf("task.Go at f.ergo:%d:1", n+1)), func(ct *rt.Task) (int, error) { return fork(ct, n+1) })
			return c.Wait()
		})
	}
	err := runMain(func(root *rt.Task) error {
		_, err := fork(root, 0)
		return err
	})
	rs := reasons(err)
	if len(rs) != 1 || rs[0].Kind != rt.KindDefect {
		t.Fatalf("exit %v; want one defect", err)
	}
	chain := rs[0].Chain
	if len(chain) != 32 || chain[0].Span != fmt.Sprintf("task.Go at f.ergo:%d:1", depth) {
		t.Fatalf("chain of %d, first %+v; want 32, the failing task first", len(chain), chain[0])
	}
	for i := 0; i < 10; i++ {
		runtime.GC()
		alive := 0
		for _, w := range held {
			if w.Value() != nil {
				alive++
			}
		}
		if alive == 0 {
			runtime.KeepAlive(err)
			return
		}
	}
	t.Fatalf("tasks of an ended tree are retained by the defect it reported")
}
