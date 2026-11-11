package lower

import (
	"bytes"
	"os/exec"
	"syscall"
	"testing"
)

// TestStdinStopRuns (batch 12 C3; stdlib.md, io 3): a stop ends a task's
// wait in io.Stdin.Lines. The reader hands its first line to main through
// a queue and waits for the next; main then stops it, with standard input
// still open and empty. Before C3 the stop waited for the read, so the
// program waited for its input to close. The test keeps the input open
// until the program has exited, so a stop that does not end the read hangs
// the test, which its timeout reports. It builds the program because the
// runtime/v1 model test calls the Go body directly: only a build checks
// that the lowering passes the iterating task, the one the stop reaches.
func TestStdinStopRuns(t *testing.T) {
	needsBuild(t)
	const src = `import io
import queue
import task

func read(q queue.Queue[string]) fails queue.Closed needs io.Stdin {
	for line in io.Stdin.Lines() {
		try q.Push(line)
	}
}

func main() fails queue.Closed needs Console | Ambient {
	with context { io.Stdin: io.NewStdin() } {
		let q = queue.New[string](1)
		let reader = task.Go() { try read(q) }
		Console.Println("got \(try q.Pop())")
		reader.Stop()
		Console.Println("stopped")
	}
}
`
	exes := buildAll(t, []*lowered{mustLower(t, "stdinstop", src)}, true)
	if exes == nil {
		return
	}
	probeRan(exes["stdinstop"])
	cmd := exec.Command(exes["stdinstop"])
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// The input closes when the test ends, even by its timeout, which ends
	// a program that a broken stop left waiting.
	defer in.Close()
	if _, err := in.Write([]byte("go\n")); err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	if err != nil || stdout.String() != "got go\nstopped\n" {
		t.Errorf("exit %v, stdout %q, stderr %.300q", err, stdout.String(), stderr.String())
	}
}
