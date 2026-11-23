package irspike

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRTaskMovesMatchSource is the seeded property over task blocks that
// move the frame's resources (GenerateTaskMove): a literal run once owns
// what it moves, a Set's repeated block lends what its member's root
// owns, and the sender hands each capture out at the call. Both literals
// have IR bodies, and the program prints the same under both lowerings,
// each close in its place. A failing seed prints the Ergo source, both
// outcomes and its replay command.
//
// It builds Go because the blocks run on tasks of their own and the
// closes run at their frames' and roots' ends; only the compiled program
// runs them.
//
// Mutants (not run, CRUNCH): task-move-go-hand-dropped (task.Go's sender
// keeps the moved Res), task-move-set-hand-dropped (the Set member's
// sender keeps it), task-move-root-unowned (the member's root owns
// nothing), task-move-return-refused (spawn's returned task stays on
// the source path).
func TestIRTaskMovesMatchSource(t *testing.T) {
	f7Property(t, "TestIRTaskMovesMatchSource", GenerateTaskMove, taskMoveFuncs...)
}

// TestIRTaskMoveSpawnUsesGoWith checks the promised struct capture for
// GenerateTaskMove's only task.Go. Mutant: task-move-result-blocks-gowith
// (the result of the block's own frame keeps the heap closure).
func TestIRTaskMoveSpawnUsesGoWith(t *testing.T) {
	file := filepath.Join(t.TempDir(), "p.ergo")
	if err := os.WriteFile(file, []byte(GenerateTaskMove(1)), 0o644); err != nil {
		t.Fatal(err)
	}
	u, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	files, rep, err := lower.LowerIR(u)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(rep.IR, "spawn") || !slices.ContainsFunc(files, func(f lower.File) bool {
		return f.Path == lower.IRFileName && bytes.Contains(f.Data, []byte(`"name":"GoWith"`))
	}) {
		t.Fatalf("spawn must use GoWith in an IR body (fallback %v)", rep.Fallback)
	}
}

// TestIRChatServeAllHasBody exercises the real Set member that captures
// a connection destructured from Queue.Pop. Mutants:
// task-move-tuple-unregistered, task-move-interface-close-refused.
func TestIRChatServeAllHasBody(t *testing.T) {
	root := filepath.Join("..", "..", "..", "examples", "chat")
	for _, target := range []struct {
		dir  string
		test bool
	}{{"cmd/chat", false}, {"cmd/chat", true}, {"memory", true}, {"server", true}} {
		u, err := Load(filepath.Join(root, target.dir))
		if err != nil {
			t.Fatal(err)
		}
		lowerBody := lower.LowerIR
		if target.test {
			lowerBody = lower.LowerIRTests
		}
		_, rep, err := lowerBody(u)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.ContainsFunc(rep.IR, func(f string) bool {
			return f == "serveAll" || strings.HasSuffix(f, "__serveAll")
		}) {
			t.Fatalf("%s (test=%t): serveAll has no IR body (fallback %v)", target.dir, target.test, rep.Fallback)
		}
		t.Logf("%s (test=%t): serveAll has an IR body", target.dir, target.test)
	}
}
