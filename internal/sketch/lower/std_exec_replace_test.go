package lower

import (
	"path/filepath"
	"testing"
)

// execReplaceProgram prints a line, then replaces itself with sh, which
// prints its environment entry REPLACED, its working directory and exits
// 7; the directory is the program's one argument.
const execReplaceProgram = `import exec
import os

func main() fails os.Exit needs Console | Ambient {
	with context { os.Process: os.NewHost() } {
		let args = os.Process.Args()
		Console.Println("before")
		let c = exec.New("sh", []string{"-c", "echo \"$REPLACED\"; pwd -P; exit 7"}).WithEnv("REPLACED", "yes").WithDir(args[0])
		exec.Replace(exec.NewHost(), c) catch {
		case exec.NotFound, exec.Denied, exec.Failed:
			return os.Exit{Code: 3, Message: "not replaced"}
		}
	}
}
`

// TestExecReplaceBecomesTheProgram: after exec.Replace the process is the
// program: its output follows what this process printed (standard output
// is flushed first, though it is a pipe here and so buffered), it reads
// the command's environment entry, runs in the command's directory, and
// its status is the process's. Mutants it kills (runtime/v1/exec mutant
// ledger): no flush (proc1-replace-noflush: "before" is lost), the
// environment dropped (proc1-replace-noenv), the directory ignored
// (proc1-replace-nodir).
// It builds Go because the process replaces itself (execve); no
// in-process check survives that.
func TestExecReplaceBecomesTheProgram(t *testing.T) {
	needsBuild(t)
	exes := buildAll(t, []*lowered{lowerSource("execreplace", execReplaceProgram, true)}, true)
	if exes == nil {
		return
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runExeArgs(t, exes["execreplace"], dir)
	if want := "before\nyes\n" + dir + "\n"; code != 7 || stdout != want {
		t.Fatalf("exit %d (want 7), stdout %q (want %q), stderr:\n%s", code, stdout, want, firstLines(stderr, 8))
	}
}
