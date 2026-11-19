package check

import (
	"strings"
	"testing"
)

// TestGoConfinedRefs: a go.Ref to a Go value whose memory-only methods are
// not safe for concurrent use (rand.PCG) is refused where it may reach
// another task, as a provider cell is (goconfined.go, GB-RACE); an atomic,
// an Option that holds one, a PCG made inside the task and one handed to a
// Go interface are admitted. Each row is the smallest case of one clause:
// the mutants gbrace-* of testdata/mutants.json name the row that kills
// each.
func TestGoConfinedRefs(t *testing.T) {
	const pcg = "import go math/rand/v2\n\nfunc pcg() go.Ref[rand.PCG] {\n\tmatch rand.NewPCG(1, 2) {\n\tcase Some{Value: p}: p\n\tcase None: pcg()\n\t}\n}\n\n"
	for _, row := range []struct {
		name, src string
		line      int    // of the one diagnostic; 0: none
		want      string // a part of its message
	}{
		{name: "captured by a task", src: "import task\n" + pcg + "func main() {\n\tlet p = pcg()\n\tlet n = task.Go() { p.Uint64() }.Wait()\n\tlet _ = n\n}\n",
			line: 13, want: "p holds go.Ref[rand.PCG], which reaches a Go value whose memory-only methods are not safe for concurrent use"},
		{name: "sent through a queue", src: "import queue\n" + pcg + "func main() {\n\tlet q = queue.New[go.Ref[rand.PCG]](1)\n\tlet _ = q\n}\n",
			line: 12, want: "a type argument may carry it to another task"},
		{name: "made in the task", src: "import task\n" + pcg + "func main() {\n\tlet n = task.Go() { pcg().Uint64() }.Wait()\n\tlet _ = n\n}\n"},
		{name: "held in an Option", src: pcg + "func main() {\n\tlet o Option[go.Ref[rand.PCG]] = Some{Value: pcg()}\n\tlet _ = o\n}\n"},
		{name: "an atomic is shared", src: "import task\nimport go sync/atomic\n\nfunc main() needs go.Foreign {\n\tlet c = go.New[atomic.Int64]()\n\tlet n = task.Go() { c.Add(1) }.Wait()\n\tlet _ = n + c.Load()\n}\n"},
		{name: "into a Go interface", src: pcg + "func main() needs go.Foreign {\n\tlet r = rand.New(pcg())\n\tlet _ = r\n}\n"},
	} {
		t.Run(row.name, func(t *testing.T) {
			_, ds := CheckSource(row.src)
			if row.line == 0 {
				if len(ds) != 0 {
					t.Fatalf("want no diagnostic, got %s\n%s", describeDiags(ds), row.src)
				}
				return
			}
			if len(ds) != 1 || ds[0].Code != "S108" || ds[0].Line != row.line || !strings.Contains(ds[0].Message, row.want) {
				t.Fatalf("want one S108 at line %d containing %q, got %s\n%s", row.line, row.want, describeDiags(ds), row.src)
			}
		})
	}
}
