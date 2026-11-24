package irspike

import (
	_ "embed"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/lower"
)

//go:embed testdata/owned_result_convert.ergo
var ownedResultRepro string

// TestIROwnedResultConversionAgrees compares the exact early-release repro
// across the evaluator and both native paths. It builds Go because Go
// executes the deferred releases; type checking cannot observe their order.
func TestIROwnedResultConversionAgrees(t *testing.T) {
	src, ir := needsBuild(t)
	dir := t.TempDir()
	u := loadText(t, dir, ownedResultRepro)
	rep, err := eval.Run(u.Package, eval.DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	var passed []string
	for _, r := range rep.Results {
		if r.Status != eval.Passed {
			t.Errorf("evaluator: %+v\n%s", r, ownedResultRepro)
		} else {
			passed = append(passed, r.Name)
		}
	}
	for _, name := range []string{"DirectReturnStaysOpen", "NamedReturnStaysOpen"} {
		if !slices.Contains(passed, name) {
			t.Errorf("evaluator did not pass %s", name)
		}
	}
	a := ownedResultRun(t, u, src, Source, "", filepath.Join(dir, "source"), nil)
	b := ownedResultRun(t, u, ir, IR, "", filepath.Join(dir, "ir"), nil, "direct", "named")
	if a != b || a.exit != 0 || !strings.Contains(a.stdout, "ok DirectReturnStaysOpen\nok NamedReturnStaysOpen\n") {
		t.Fatalf("source: %+v\nIR: %+v\n%s", a, b, ownedResultRepro)
	}
}

// TestIROwnedResultShapesAgree checks all sources of a converted result
// (literal, local and call) in plain and fallible returns. The caller's
// scope, not the conversion or the callee's defer, releases the result.
// It builds Go because the static and list frames must actually unwind.
//
// Mutant: converted result keeps the callee's static release.
func TestIROwnedResultShapesAgree(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 4)
	for seed := first; seed < first+count; seed++ {
		t.Run(itoa(seed), func(t *testing.T) {
			dir := t.TempDir()
			text, lowered := ownedResultProgram(seed)
			drop, dropBodies := ownedDropProgram(seed)
			text += drop
			lowered = append(lowered, dropBodies...)
			t.Cleanup(func() {
				if t.Failed() {
					t.Logf("replay: ERGO_IRSPIKE_SEED=%d ERGO_IRSPIKE_SEEDS=1 go test ./internal/experimental/irspike -run '^TestIROwnedResultShapesAgree$'\n%s", seed, text)
				}
			})
			u := loadText(t, dir, text)
			a := ownedResultRun(t, u, ir, IR, "scope", filepath.Join(dir, "list"), dropBodies, lowered...)
			b := ownedResultRun(t, u, ir, IR, "", filepath.Join(dir, "static"), dropBodies, lowered...)
			if a != b || a.exit != 0 {
				t.Fatalf("list: %+v\nstatic: %+v", a, b)
			}
			for _, name := range lowered {
				if !strings.Contains(a.stdout, "ok Check"+name+"\n") {
					t.Errorf("no passing ownership check for %s: %+v", name, a)
				}
			}
			// The frozen emitter releases the older FallibleCatchTail form
			// early. Compare only the new generated drops to that emitter;
			// the whole program still agrees under both IR owner strategies.
			ownedResultRunSelected(t, u, src, Source, "scope", filepath.Join(dir, "source"), dropBodies, nil)
		})
	}
}

func ownedResultRun(t *testing.T, u lower.Unit, tc Toolchain, path Path, releaseMode, dir string, listOnly []string, wantIR ...string) outcome {
	t.Helper()
	return ownedResultRunSelected(t, u, tc, path, releaseMode, dir, nil, listOnly, wantIR...)
}

func ownedResultRunSelected(t *testing.T, u lower.Unit, tc Toolchain, path Path, releaseMode, dir string, selected, listOnly []string, wantIR ...string) outcome {
	t.Helper()
	mod := filepath.Join(dir, "mod")
	prev := lower.SetReleaseLoweringForTest(releaseMode)
	defer lower.SetReleaseLoweringForTest(prev)
	if path == IR {
		files, rep, err := lower.LowerIRTests(u)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range wantIR {
			at := slices.IndexFunc(rep.IR, func(body string) bool {
				return body == name || strings.HasSuffix(body, "."+name) // a method's receiver qualifies it
			})
			if at < 0 {
				t.Fatalf("mode %q: no IR body for %s (IR %v, fallback %v)", releaseMode, name, rep.IR, rep.Fallback)
			}
			helper := `"name":"` + lower.HelperSpelling("", strings.ReplaceAll(rep.IR[at], ".", "_")+"Release") + `"`
			static := slices.ContainsFunc(files, func(f lower.File) bool {
				return f.Path == lower.IRFileName && strings.Contains(string(f.Data), helper)
			})
			if static != (releaseMode == "" && !slices.Contains(listOnly, name)) {
				t.Errorf("mode %q: %s static cleanup = %v", releaseMode, name, static)
			}
		}
		if err := writeModule(mod, files); err != nil {
			t.Fatal(err)
		}
	} else if err := emitTests(u, path, mod); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "test")
	if _, err := Build(tc, path, mod, bin, BuildOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(selected) > 0 {
		for _, name := range selected {
			got := runCase(t, bin, caseRun{args: []string{"--source", dir, "--run", "Check" + name}})
			if got.exit != 0 || !strings.Contains(got.stdout, "ok Check"+name+"\n") {
				t.Fatalf("source list, Check%s: %+v", name, got)
			}
		}
		return outcome{}
	}
	return runCase(t, bin, caseRun{args: []string{"--source", dir}})
}

func ownedResultProgram(seed uint64) (string, []string) {
	r := rand.New(rand.NewPCG(seed, seed^0x56b32d170da8cbf1))
	prelude, _, _ := strings.Cut(ownedResultRepro, "func direct(")
	var src strings.Builder
	src.WriteString("import queue\nimport scope\nimport task\n")
	src.WriteString(prelude)
	src.WriteString(`type Bad {}

type guardTicket { releases sync.Ref[int] } implements Closer

func Close(g guardTicket) {
	let n = g.releases.Get()
	g.releases.Set(n + 1)
	if n < 0 { Unreachable("guard cleanup") }
}

func makeResult(releases sync.Ref[int]) wrapped {
	wrapped{item: ticket{releases: releases}}
}

func makeFallible(releases sync.Ref[int], reject bool) wrapped fails Bad {
	if reject { return Bad }
	makeResult(releases)
}

func firstUse(v own View) int { v.Reads() }

func main() {}
`)
	forms := []struct{ name, setup, result string }{
		{"Literal", "let item = ticket{releases: releases}\n", "wrapped{item: item}"},
		{"Local", "let item = makeResult(releases)\n", "item"},
		{"Call", "", "makeResult(releases)"},
	}
	type shape struct {
		name, setup, result string
		fallible            bool
	}
	var shapes []shape
	for _, f := range forms {
		shapes = append(shapes, shape{"Plain" + f.name, f.setup, f.result, false},
			shape{"Fallible" + f.name, f.setup, f.result, true})
	}
	shapes = append(shapes,
		shape{"FallibleCallTail", "", "makeFallible(releases, reject)", true},
		shape{"FallibleTryTail", "", "try makeFallible(releases, reject)", true},
		shape{"FallibleCatchTail", "", "makeFallible(releases, reject) catch {\ncase Bad: return Bad\n}", true})
	r.Shuffle(len(shapes), func(i, j int) { shapes[i], shapes[j] = shapes[j], shapes[i] })
	var lowered []string
	for _, s := range shapes {
		lowered = append(lowered, s.name)
		row, extra, recover, rejected := "", "", "", ""
		if s.fallible {
			row, extra = " fails Bad", ", reject bool"
			recover = " catch { case Bad: Unreachable(\"unexpected failure\") }"
			rejected = "false"
		}
		fmt.Fprintf(&src, "\nfunc %s(releases sync.Ref[int], guard sync.Ref[int]%s) View%s {\n", s.name, extra, row)
		src.WriteString("let cleanup = guardTicket{releases: guard}\n")
		if s.fallible && !strings.Contains(s.result, "makeFallible(") {
			src.WriteString("if reject { return Bad }\n")
		}
		src.WriteString(s.setup)
		if r.IntN(2) == 0 {
			src.WriteString("return ")
		}
		src.WriteString(s.result + "\n}\n")
		if s.fallible {
			rejected = ", " + rejected
		}
		start := 10 + r.IntN(1000)
		fmt.Fprintf(&src, "\ntest Check%s {\nlet releases = sync.NewRef(%d)\nlet guard = sync.NewRef(0)\n", s.name, start)
		fmt.Fprintf(&src, "let v = %s(releases, guard%s)%s\n", s.name, rejected, recover)
		fmt.Fprintf(&src, "assert guard.Get() == 1\nassert firstUse(v) == %d\nassert releases.Get() == %d\n", start, start+1)
		// A defect while leaving the producer must release the value that
		// it was about to return; ownership has not reached the caller.
		src.WriteString("guard.Set(-1)\nlet child = task.Go() {\n")
		try := ""
		if s.fallible {
			try = "try "
		}
		fmt.Fprintf(&src, "let childValue = %s%s(releases, guard%s)\nfirstUse(childValue)\n}\n", try, s.name, rejected)
		src.WriteString("let crashed = match task.ExitOf(child) {\ncase task.Defected: true\ncase _: false\n}\n")
		fmt.Fprintf(&src, "assert crashed\nassert guard.Get() == 0\nassert releases.Get() == %d\n", start+2)
		if s.fallible {
			fmt.Fprintf(&src, "let unexpected = %s(releases, guard, true) catch {\ncase Bad:\n", s.name)
			fmt.Fprintf(&src, "assert guard.Get() == 1\nassert releases.Get() == %d\nreturn\n}\n", start+2)
			src.WriteString("let _ = firstUse(unexpected)\nassert false\n")
		}
		src.WriteString("}\n")
	}
	return src.String(), lowered
}

// ownedDropProgram adds generated releases to the same seeded agreement
// program. Each producer observes that its live members have not closed,
// including before a typed rejection or a defect. The caller independently
// asserts the release order and detects missing or repeated releases.
func ownedDropProgram(seed uint64) (string, []string) {
	var src strings.Builder
	src.WriteString(`
type dropTicket { id int, log sync.Ref[int], crash bool } implements Closer

func Close(t dropTicket) {
	t.log.Set(t.log.Get()*10 + t.id)
	if t.crash { Unreachable("release defect") }
}

type dropPair { left dropTicket, right dropTicket }
type DropVariant = DropFull | DropEmpty
type DropFull { pair dropPair }
type DropEmpty {}
type dropNode { item dropTicket, more []dropNode }
`)
	forms := []struct{ name, body, live, released string }{
		{"Record", `let pair = dropPair{left: l, right: r}`, "0", "11*id+10"},
		{"InPlace", `let mut pair = dropPair{left: l, right: r}
		pair.left.id = id+2`, "0", "11*id+12"},
		{"Tuple", `let pair = (l, r)`, "0", "11*id+10"},
		{"Slice", `let pair = []dropTicket{l, r}`, "0", "11*id+10"},
		{"Array", `let pair = [2]dropTicket{l, r}`, "0", "11*id+10"},
		{"Map", `let pair = Map[string, dropTicket]{"l": l, "r": r}`, "0", "11*id+10 || log.Get() == 11*id+1"},
		{"Union", `let pair DropVariant = DropFull{pair: dropPair{left: l, right: r}}`, "0", "11*id+10"},
		{"Recursive", `let pair = dropNode{item: l, more: []dropNode{dropNode{item: r, more: []dropNode{}}}}`, "0", "11*id+10"},
		{"Drain", `let pair = queue.New[dropTicket](2)
		pair.Push(l) catch { case queue.Closed: Unreachable("open queue") }
		pair.Push(r) catch { case queue.Closed: Unreachable("open queue") }`, "0", "11*id+10"},
		{"Taken", `let pair = dropPair{left: l, right: r}
		pair.left.Close()`, "id", "11*id+1"},
	}
	var names []string
	for _, f := range forms {
		name := "Drop" + f.name
		names = append(names, name)
		fmt.Fprintf(&src, `
func %[1]s(log sync.Ref[int], id int, reject bool, defect bool) bool fails Bad {
	let l = dropTicket{id: id, log: log, crash: false}
	let r = dropTicket{id: id+1, log: log, crash: defect}
	%[2]s
	if log.Get() != %[3]s { Unreachable("early release") }
	if reject { return Bad }
	false
}

test Check%[1]s {
	let log = sync.NewRef(0)
	let id = %[4]d
	%[1]s(log, id, false, false) catch { case Bad: Unreachable("unexpected failure") }
	assert log.Get() == %[5]s
	log.Set(0)
	let rejected = %[1]s(log, id, true, false) catch { case Bad: true }
	assert rejected
	assert log.Get() == %[5]s
	log.Set(0)
	let child = task.Go() { try %[1]s(log, id, false, true) }
	let crashed = match task.ExitOf(child) {
	case task.Defected: true
	case _: false
	}
	assert crashed
	assert log.Get() == %[5]s
}
`, name, f.body, f.live, 1+seed%7, f.released)
	}
	names = append(names, "DropCallback")
	fmt.Fprintf(&src, `
func DropCallback(log sync.Ref[int], id int, reject bool, defect bool) bool fails Bad {
	let receive = func(t own dropTicket) bool fails Bad {
		if t.log.Get() != 0 { Unreachable("early release") }
		if defect { Unreachable("body defect") }
		if reject { return Bad }
		false
	}
	try receive(dropTicket{id: id, log: log, crash: false})
}

test CheckDropCallback {
	let log = sync.NewRef(0)
	let id = %d
	DropCallback(log, id, false, false) catch { case Bad: Unreachable("unexpected failure") }
	assert log.Get() == id
	log.Set(0)
	let rejected = DropCallback(log, id, true, false) catch { case Bad: true }
	assert rejected
	assert log.Get() == id
	log.Set(0)
	let child = task.Go() { try DropCallback(log, id, false, true) }
	let crashed = match task.ExitOf(child) {
	case task.Defected: true
	case _: false
	}
	assert crashed
	assert log.Get() == id
}
`, 1+seed%7)
	names = append(names, "DropReplace")
	fmt.Fprintf(&src, `
func replacement(log sync.Ref[int], id int, reject bool) dropTicket fails Bad {
	if reject { return Bad }
	dropTicket{id: id, log: log, crash: false}
}

func DropReplace(log sync.Ref[int], id int, reject bool, defect bool) int fails Bad {
	if id > 0 {
		let mut owner = dropTicket{id: id, log: log, crash: defect}
		let sibling = dropTicket{id: id+1, log: log, crash: false}
		owner = try replacement(log, id+2, reject)
		if owner.log.Get() != id || sibling.log.Get() != id { Unreachable("released out of order") }
	}
	log.Get()
}

test CheckDropReplace {
	let log = sync.NewRef(0)
	let id = %d
	let observed = DropReplace(log, id, false, false) catch { case Bad: 0 }
	assert observed == 111*id+21
	assert log.Get() == observed
	log.Set(0)
	let rejected = DropReplace(log, id, true, false) catch { case Bad: -1 }
	assert rejected == -1
	assert log.Get() == 11*id+10
	log.Set(0)
	let child = task.Go() { try DropReplace(log, id, false, true) }
	let crashed = match task.ExitOf(child) {
	case task.Defected: true
	case _: false
	}
	assert crashed
	assert log.Get() == 111*id+21
}
`, 1+seed%7)
	names = append(names, "DropReplacePlace")
	fmt.Fprintf(&src, `
func DropReplacePlace(log sync.Ref[int], id int) {
	let mut pair = dropPair{left: dropTicket{id: id, log: log, crash: false}, right: dropTicket{id: id+1, log: log, crash: false}}
	pair.left.Close()
	pair = dropPair{left: dropTicket{id: id+2, log: log, crash: false}, right: dropTicket{id: id+3, log: log, crash: false}}
	if pair.right.log.Get() != 11*id+1 { Unreachable("replacement not live") }
	pair.right.id = id+4
}

test CheckDropReplacePlace {
	let log = sync.NewRef(0)
	let id = %d
	DropReplacePlace(log, id)
	assert log.Get() == 1111*id+142
}
`, 1+seed%5)
	names = append(names, "DropTask", "DropPattern", "DropPatternChoice")
	fmt.Fprintf(&src, `
type pendingOwnedTasks { jobs Map[int, Task[bool]] }

func DropTask(p mut pendingOwnedTasks, id int, reject bool, defect bool) bool fails Bad {
	let found, rest = p.jobs.Take(id)
	p.jobs = rest
	match found {
	case Some[Task[bool]] as child:
		if defect { Unreachable("body defect") }
		if reject { return Bad }
		child.Value.Stop()
		true
	case None:
		false
	}
}

func exercisingTask(log sync.Ref[int], id int, reject bool, defect bool) bool fails Bad {
	let started = queue.New[bool](1)
	let idle = queue.New[bool](1)
	let child = task.Go() {
		scope.OnExit() { log.Set(log.Get()+1) }
		started.Push(true) catch { case queue.Closed: Unreachable("start queue closed") }
		idle.Pop() catch { case queue.Closed: false }
	}
	let _ = started.Pop() catch { case queue.Closed: Unreachable("start queue closed") }
	let mut p = pendingOwnedTasks{jobs: Map[int, Task[bool]]{id: child}}
	try DropTask(&p, id, reject, defect)
}

func DropPattern(found own Option[dropTicket], reject bool, defect bool) bool fails Bad {
	match found {
	case Some[dropTicket] as part:
		if part.Value.log.Get() != 0 { Unreachable("early release") }
		if defect { Unreachable("body defect") }
		if reject { return Bad }
		part.Value.Close()
		true
	case None:
		false
	}
}

type DropChoice = DropKeep | DropMove
type DropKeep { item dropTicket }
type DropMove { item dropTicket }

func DropPatternChoice(found own DropChoice, log sync.Ref[int]) bool {
	match found {
	case DropMove as part:
		if part.item.log.Get() != 0 { Unreachable("early release") }
		part.item.Close()
		true
	case DropKeep:
		if log.Get() != 0 { Unreachable("early release") }
		false
	}
}

test CheckDropTask {
	let id = %d
	let log = sync.NewRef(0)
	let mut empty = pendingOwnedTasks{jobs: Map[int, Task[bool]]{}}
	let absent = DropTask(&empty, id, false, false) catch { case Bad: true }
	assert !absent
	let stopped = exercisingTask(log, id, false, false) catch { case Bad: false }
	assert stopped
	assert log.Get() == 1
	log.Set(0)
	let rejected = exercisingTask(log, id, true, false) catch { case Bad: true }
	assert rejected
	assert log.Get() == 1
	log.Set(0)
	let crashing = task.Go() { try exercisingTask(log, id, false, true) }
	let crashed = match task.ExitOf(crashing) {
	case task.Defected: true
	case _: false
	}
	assert crashed
	assert log.Get() == 1
}

test CheckDropPattern {
	let id = %d
	let log = sync.NewRef(0)
	let empty = DropPattern(None, false, false) catch { case Bad: true }
	assert !empty
	let closed = DropPattern(Some[dropTicket]{Value: dropTicket{id: id, log: log, crash: false}}, false, false) catch { case Bad: false }
	assert closed
	assert log.Get() == id
	log.Set(0)
	let rejected = DropPattern(Some[dropTicket]{Value: dropTicket{id: id, log: log, crash: false}}, true, false) catch { case Bad: true }
	assert rejected
	assert log.Get() == id
	log.Set(0)
	let crashing = task.Go() {
		try DropPattern(Some[dropTicket]{Value: dropTicket{id: id, log: log, crash: false}}, false, true)
	}
	let crashed = match task.ExitOf(crashing) {
	case task.Defected: true
	case _: false
	}
	assert crashed
	assert log.Get() == id
}

test CheckDropPatternChoice {
	let id = %d
	let log = sync.NewRef(0)
	let kept = DropPatternChoice(DropKeep{item: dropTicket{id: id, log: log, crash: false}}, log)
	assert !kept
	assert log.Get() == id
	log.Set(0)
	let moved = DropPatternChoice(DropMove{item: dropTicket{id: id, log: log, crash: false}}, log)
	assert moved
	assert log.Get() == id
}
`, 1+seed%7, 1+seed%7, 1+seed%7)
	return src.String(), names
}
