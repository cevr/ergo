package lower

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestTaskOwnershipRunModel checks the emitted moving-capture protocol
// against a reference trace: captures leave the sender on admission and
// are released once by the child before Wait returns, including failed
// admission and the root lifetime of a restarted Set member. Counts and
// callback forms vary by seed. ERGO_SEED replays a program with both traces.
//
// It builds Go because the subject is cross-goroutine lifetime and release
// order; go/types cannot execute task admission, restart or shutdown.
// Mutants unique to it: omit the child's capture registration, keep the
// sender's capture entry, register a Set capture on every restart, leave
// a taken element in its remainder, lose Take's input on a bounds panic,
// drop the resource a drained task ended with (Q5), or pass no release
// to a std boundary that drops a returned value (Gap 47).
func TestTaskOwnershipRunModel(t *testing.T) {
	needsBuild(t)
	first, count := 1, 32
	if s := os.Getenv("ERGO_SEED"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			t.Fatal(err)
		}
		first, count = n, 1
	}
	type runCase struct{ name, src, want string }
	var cases []runCase
	var programs []*lowered
	for seed := first; seed < first+count; seed++ {
		src, want := taskOwnProgram(seed)
		name := fmt.Sprintf("taskown_%d", seed)
		cases = append(cases, runCase{name, src, want})
		programs = append(programs, mustLower(t, name, src))
	}
	exes := buildAll(t, programs, true)
	if exes == nil {
		return
	}
	for i, c := range cases {
		cmd := exec.Command(exes[c.name])
		var out, errs bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errs
		err := runProgram(t, cmd)
		got := out.String()
		if (first+i)%taskOwnModes == 5 {
			got = sortMapReleases(got)
		}
		wantFailure := (first+i)%taskOwnModes == 6
		if (err != nil) != wantFailure || got != c.want {
			t.Errorf("seed %d (ERGO_SEED=%d go test -p 2 -run '^TestTaskOwnershipRunModel$' ./internal/sketch/lower)\n%s\nexit: %v\ngot:\n%s\nwant:\n%s\nstderr:\n%s", first+i, first+i, numbered(c.src), err, out.String(), c.want, errs.String())
		}
	}
}

// taskOwnModes counts the program shapes: task, restart, Closed, stop,
// slice, Map, panic, crossing, unread result, std boundaries.
const taskOwnModes = 14

func taskOwnProgram(seed int) (string, string) {
	n := 1 + (seed/taskOwnModes)%4
	mode := seed % taskOwnModes
	var src, want strings.Builder
	var afterEnd strings.Builder
	src.WriteString("import task\nimport tasks\nimport sync\nimport queue\nimport slices\nimport strconv\nimport scope\n")
	src.WriteString("type Fault {}\ntype R { N int }\nfunc Close(r R) needs Console { Console.Println(\"close \" + strconv.Itoa(r.N)) }\n")
	src.WriteString("func main() fails Fault | tasks.Closed needs Console {\n")
	// Keep the existing move controls: these destinations release after
	// the later resource bindings, so R27 must not turn them into lends.
	if mode == 0 {
		src.WriteString("let mut t = task.Go() { 1 }\n")
	}
	if mode == 3 {
		src.WriteString("let mut t = task.Go(func() fails queue.Closed {})\n")
	}
	if mode == 1 {
		fmt.Fprintf(&src, "let set = tasks.NewSet[Fault](tasks.Restart{Limit: %d})\n", 1+(seed/3)%3)
	}
	if mode == 2 {
		src.WriteString("let set = tasks.NewSet[Fault](tasks.FailFast)\nset.Stop()\n")
	}
	for i := 0; i < n; i++ {
		fmt.Fprintf(&src, "let r%d = R{N: %d}\n", i, i)
	}
	var uses strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&uses, "Console.Println(\"use \" + strconv.Itoa(r%d.N))\n", i)
	}
	closes := func() {
		for i := n - 1; i >= 0; i-- {
			fmt.Fprintf(&want, "close %d\n", i)
		}
	}
	switch mode {
	case 0:
		if seed%2 == 0 {
			src.WriteString("t = task.Go() {\n" + uses.String() + "1\n}\n")
		} else {
			src.WriteString("t = task.Go(func() int needs Console {\n" + uses.String() + "1\n})\n")
		}
		src.WriteString("let v = t.Wait()\nConsole.Println(\"waited \" + strconv.Itoa(v))\n")
		for i := 0; i < n; i++ {
			fmt.Fprintf(&want, "use %d\n", i)
		}
		closes()
		want.WriteString("waited 1\n")
	case 1:
		limit := 1 + (seed/3)%3
		src.WriteString("let calls = sync.NewRef[int](0)\n")
		src.WriteString("try tasks.Go(set) {\nlet run = sync.Modify(calls, func(v int) (int, int) { (v + 1, v + 1) })\n")
		src.WriteString(uses.String())
		fmt.Fprintf(&src, "if run <= %d { Unreachable(\"restart\") }\n}\ntry set.Wait()\nConsole.Println(\"waited\")\n", limit)
		for run := 0; run <= limit; run++ {
			for i := 0; i < n; i++ {
				fmt.Fprintf(&want, "use %d\n", i)
			}
		}
		closes()
		want.WriteString("waited\n")
	case 2:
		src.WriteString("tasks.Go(set) {\n" + uses.String() + "} catch { case tasks.Closed: Console.Println(\"rejected\") }\n")
		closes()
		want.WriteString("rejected\n")
	case 3:
		src.WriteString("let gate = queue.New[int](0)\nt = task.Go() {\nlet _ = try gate.Pop()\n")
		for i := 0; i < n; i++ {
			fmt.Fprintf(&src, "let _ = r%d.N\n", i)
		}
		src.WriteString("}\nt.Stop()\nConsole.Println(\"stopped\")\n")
		closes()
		want.WriteString("stopped\n")
	case 4:
		var parts []string
		for i := 0; i < n; i++ {
			parts = append(parts, fmt.Sprintf("r%d", i))
		}
		index := (seed/8*3 + 1) % n
		fmt.Fprintf(&src, "let xs = []R{%s}\nlet value, left = slices.Take(xs, %d)\nConsole.Println(\"taken \" + strconv.Itoa(value.N))\nvalue.Close()\n", strings.Join(parts, ", "), index)
		fmt.Fprintf(&want, "taken %d\nclose %d\n", index, index)
		for i := n - 1; i >= 0; i-- {
			if i != index {
				fmt.Fprintf(&afterEnd, "close %d\n", i)
			}
		}
	case 5:
		var parts []string
		for i := 0; i < n; i++ {
			parts = append(parts, fmt.Sprintf("%d: r%d", i, i))
		}
		index := (seed / 8) % n
		if seed%12 < 6 {
			index = n // a missing key leaves the entire remainder owned
		}
		fmt.Fprintf(&src, "let m = Map[int, R]{%s}\nlet found, left = m.Take(%d)\nmatch found { case Some[R] as item: Console.Println(\"taken \" + strconv.Itoa(item.Value.N)); item.Value.Close(); case None: Console.Println(\"missing\") }\n", strings.Join(parts, ", "), index)
		if index < n {
			fmt.Fprintf(&want, "taken %d\nclose %d\n", index, index)
		} else {
			want.WriteString("missing\n")
		}
		for i := 0; i < n; i++ {
			if i != index {
				fmt.Fprintf(&afterEnd, "close %d\n", i)
			}
		}
	case 6:
		var parts []string
		for i := 0; i < n; i++ {
			parts = append(parts, fmt.Sprintf("r%d", i))
		}
		fmt.Fprintf(&src, "let xs = []R{%s}\nlet value, left = slices.Take(xs, %d)\nvalue.Close()\n", strings.Join(parts, ", "), n)
		closes() // panic occurs before the source container is handed off
	case 7:
		src.WriteString("let xs = []int{10, 20, 30}\nlet value, rest = slices.Take(xs, 1)\n")
		src.WriteString("Console.Println(strconv.FormatBool(xs[1] == 20 && value == 20 && len(rest) == 2 && rest[0] == 10 && rest[1] == 30))\n")
		src.WriteString("let m = Map[int, int]{1: 10, 2: 20}\nlet found, left = m.Take(1)\nConsole.Println(strconv.FormatBool(m.Get(1) == Some[int]{Value: 10} && found == Some[int]{Value: 10} && left.Get(1) == None && left.Get(2) == Some[int]{Value: 20}))\n")
		want.WriteString("true\ntrue\n")
		for i := n - 1; i >= 0; i-- {
			fmt.Fprintf(&afterEnd, "close %d\n", i)
		}
	case 8:
		// Q5: a task that ended with a resource no Wait took. The child's
		// Push has room, so it ends with its value before main's Pop
		// returns, and the drain releases that value before main's own.
		// A Wait moves the value to main instead: one release either way.
		// With capacity 0 the body may return its value after main's
		// release stopped the task: stop wins at the boundary (Gap 47, B4),
		// and the drain still releases the value (measured: most runs).
		fmt.Fprintf(&src, "let done = queue.New[int](%d)\nlet t = task.Go() {\ntry done.Push(1)\nR{N: 100}\n}\nlet _ = try done.Pop()\n", (seed/2)%2)
		if seed%2 == 1 {
			src.WriteString("let v = try t.Wait()\n")
		}
		afterEnd.WriteString("close 100\n")
		for i := n - 1; i >= 0; i-- {
			fmt.Fprintf(&afterEnd, "close %d\n", i)
		}
	case 9:
		// Gap 47 (B4): std boundaries that drop values a block returned.
		// Blocks with no interruption point always return, so Race's
		// first block wins and it releases the losers in block order;
		// Transform with limit 1 fails at call k and releases the results
		// before it (its []R glue releases last first).
		m, k := n+1, (seed/taskOwnModes)%(n+1)
		src.WriteString("let won = tasks.Race([]func() R needs Console{\n")
		for i := 0; i < m; i++ {
			fmt.Fprintf(&src, "func() R needs Console { R{N: %d} },\n", 400+i)
			if i > 0 {
				fmt.Fprintf(&want, "close %d\n", 400+i)
			}
		}
		src.WriteString("})\nConsole.Println(\"won \" + strconv.Itoa(won.N))\n")
		want.WriteString("won 400\n")
		var xs []string
		for i := 0; i < m; i++ {
			xs = append(xs, strconv.Itoa(i))
		}
		fmt.Fprintf(&src, "let got = tasks.Transform([]int{%s}, 1, func(x int) R fails Fault {\nif x == %d {\nreturn Fault\n}\nR{N: 300 + x}\n}) catch {\ncase Fault:\nConsole.Println(\"failed\")\n[]R{}\n}\n", strings.Join(xs, ", "), k)
		for i := k - 1; i >= 0; i-- {
			fmt.Fprintf(&want, "close %d\n", 300+i)
		}
		want.WriteString("failed\n")
		afterEnd.WriteString("close 400\n")
		for i := n - 1; i >= 0; i-- {
			fmt.Fprintf(&afterEnd, "close %d\n", i)
		}
	case 10:
		// The child acknowledges its uses, then stays alive until scope
		// shutdown. Its exit action proves draining precedes root release.
		src.WriteString("let done = queue.New[int](0)\nlet gate = queue.New[int](0)\nlet t = task.Go() {\nscope.OnExit() { Console.Println(\"drained\") }\n" + uses.String() + "try done.Push(1)\nlet _ = try gate.Pop()\n}\nlet _ = try done.Pop()\n")
		for i := 0; i < n; i++ {
			fmt.Fprintf(&want, "use %d\n", i)
		}
		afterEnd.WriteString("drained\n")
		for i := n - 1; i >= 0; i-- {
			fmt.Fprintf(&afterEnd, "close %d\n", i)
		}
	case 11:
		src.WriteString("let set = tasks.NewSet[Fault](tasks.FailFast)\ntry tasks.Go(set) {\n" + uses.String() + "}\ntry set.Wait()\n")
		for i := 0; i < n; i++ {
			fmt.Fprintf(&want, "use %d\n", i)
		}
		for i := n - 1; i >= 0; i-- {
			fmt.Fprintf(&src, "r%d.Close()\n", i)
			fmt.Fprintf(&want, "close %d\n", i)
		}
	case 12:
		src.WriteString("let f = func() int needs Console {\n" + uses.String() + "1\n}\nlet t = task.Go(f)\nlet _ = t.Wait()\n")
		for i := 0; i < n; i++ {
			fmt.Fprintf(&want, "use %d\n", i)
		}
		for i := n - 1; i >= 0; i-- {
			fmt.Fprintf(&afterEnd, "close %d\n", i)
		}
	case 13:
		src.WriteString("let p = R{N: 100}\nlet pair = Pair{Root: p, Worker: task.Go(func() int fails Fault needs Console { Console.Println(\"use \" + strconv.Itoa(p.N)); 1 })}\nlet _ = try pair.Worker.Wait()\npair.Root.Close()\n")
		want.WriteString("use 100\nclose 100\n")
		for i := n - 1; i >= 0; i-- {
			fmt.Fprintf(&afterEnd, "close %d\n", i)
		}
	}
	src.WriteString("Console.Println(\"end\")\n}\n")
	if mode != 6 {
		want.WriteString("end\n")
	}
	want.WriteString(afterEnd.String())
	text := src.String()
	if mode == 13 {
		text += "type Pair { Root R, Worker Task[int, Fault] }\n"
	}
	if mode != 10 {
		text = strings.ReplaceAll(text, "import scope\n", "")
	}
	if mode != 0 && mode != 3 && mode != 8 && mode != 10 && mode != 12 && mode != 13 {
		text = strings.ReplaceAll(text, "import task\n", "")
	}
	if mode != 1 {
		text = strings.ReplaceAll(text, "import sync\n", "")
	}
	if mode != 3 && mode != 8 && mode != 10 {
		text = strings.ReplaceAll(text, "import queue\n", "")
	} else if mode == 8 || mode == 10 {
		text = strings.Replace(text, "fails Fault | tasks.Closed", "fails Fault | tasks.Closed | queue.Closed", 1)
	}
	if mode != 4 && mode != 6 && mode != 7 {
		text = strings.ReplaceAll(text, "import slices\n", "")
	}
	return text, want.String()
}

// A Map's release order is unspecified; only its remaining release lines
// are a multiset. The prefix still checks admission and hand-off order.
func sortMapReleases(trace string) string {
	before, after, ok := strings.Cut(trace, "end\n")
	if !ok {
		return trace
	}
	lines := strings.FieldsFunc(after, func(r rune) bool { return r == '\n' })
	sort.Strings(lines)
	if len(lines) == 0 {
		return before + "end\n"
	}
	return before + "end\n" + strings.Join(lines, "\n") + "\n"
}
