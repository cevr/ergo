package lower

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
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
// sender's capture entry, and register a Set capture on every restart.
func TestTaskOwnershipRunModel(t *testing.T) {
	needsBuild(t)
	first, count := 1, 24
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
		if err != nil || out.String() != c.want {
			t.Errorf("seed %d (ERGO_SEED=%d go test -p 2 -run '^TestTaskOwnershipRunModel$' ./internal/sketch/lower)\n%s\nexit: %v\ngot:\n%s\nwant:\n%s\nstderr:\n%s", first+i, first+i, numbered(c.src), err, out.String(), c.want, errs.String())
		}
	}
}

func taskOwnProgram(seed int) (string, string) {
	n := 1 + (seed/4)%4
	mode := seed % 4 // single task, restarted member, failed admission, stop
	var src, want strings.Builder
	src.WriteString("import task\nimport tasks\nimport sync\nimport queue\nimport strconv\n")
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
	}
	src.WriteString("Console.Println(\"end\")\n}\n")
	want.WriteString("end\n")
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
	return text, want.String()
}
