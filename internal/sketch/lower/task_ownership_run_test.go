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
// a taken element in its remainder, or lose Take's input on a bounds panic.
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
		if (first+i)%8 == 5 {
			got = sortMapReleases(got)
		}
		wantFailure := (first+i)%8 == 6
		if (err != nil) != wantFailure || got != c.want {
			t.Errorf("seed %d (ERGO_SEED=%d go test -p 2 -run '^TestTaskOwnershipRunModel$' ./internal/sketch/lower)\n%s\nexit: %v\ngot:\n%s\nwant:\n%s\nstderr:\n%s", first+i, first+i, numbered(c.src), err, out.String(), c.want, errs.String())
		}
	}
}

func taskOwnProgram(seed int) (string, string) {
	n := 1 + (seed/8)%4
	mode := seed % 8 // task, restart, Closed, stop, slice, Map, panic, crossing
	var src, want strings.Builder
	var afterEnd strings.Builder
	src.WriteString("import task\nimport tasks\nimport sync\nimport queue\nimport slices\nimport strconv\n")
	src.WriteString("type Fault {}\ntype R { N int }\nfunc Close(r R) needs Console { Console.Println(\"close \" + strconv.Itoa(r.N)) }\n")
	src.WriteString("func main() fails Fault | tasks.Closed needs Console {\n")
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
			src.WriteString("let t = task.Go() {\n" + uses.String() + "1\n}\n")
		} else {
			src.WriteString("let t = task.Go(func() int needs Console {\n" + uses.String() + "1\n})\n")
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
		fmt.Fprintf(&src, "let set = tasks.NewSet[Fault](tasks.Restart{Limit: %d})\n", limit)
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
		src.WriteString("let set = tasks.NewSet[Fault](tasks.FailFast)\nset.Stop()\n")
		src.WriteString("tasks.Go(set) {\n" + uses.String() + "} catch { case tasks.Closed: Console.Println(\"rejected\") }\n")
		closes()
		want.WriteString("rejected\n")
	case 3:
		src.WriteString("let gate = queue.New[int](0)\nlet t = task.Go() {\nlet _ = try gate.Pop()\n")
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
	}
	src.WriteString("Console.Println(\"end\")\n}\n")
	if mode != 6 {
		want.WriteString("end\n")
	}
	want.WriteString(afterEnd.String())
	text := src.String()
	if mode != 0 && mode != 3 {
		text = strings.ReplaceAll(text, "import task\n", "")
	}
	if mode != 1 {
		text = strings.ReplaceAll(text, "import sync\n", "")
	}
	if mode != 3 {
		text = strings.ReplaceAll(text, "import queue\n", "")
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
