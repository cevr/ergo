package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestGoContextInterrupts is IG10's acceptance (docs/spec/interop.md, Host
// calls; #202's G8 host-call stage): a Go function whose first parameter
// is context.Context is bound with that parameter hidden, and the call
// passes the calling task's context, through import go and through a user
// extern (a Level 2 adapter of runtime/trace.Log). A task blocked in such
// a call is interrupted by timeout.After: Go's wait ends on ctx.Done(), the task
// unwinds at the call (the context.Canceled error Go returns is never
// seen as a go.Error, and nothing after the call runs), its exit action
// runs, masked, where a Go call gets a context that cannot be cancelled,
// and no goroutine is left. Outside a mask the context can be cancelled.
//
// Mutants it alone catches: the interruption point after the call dropped
// (the task goes on with "go error context canceled"); the mask ignored
// (the release's context is cancellable); the user extern's call not
// passing the context (go build fails); the binding not hiding the
// parameter (the program does not check).
//
// Builds Go because the subject is a running Go wait that an interruption
// of the Ergo task must end: only the emitted program shows it.
func TestGoContextInterrupts(t *testing.T) {
	binary := buildTestCLI(t)
	tmp := t.TempDir()
	app := filepath.Join(tmp, "app")
	writeFiles(t, tmp, map[string]string{
		"hostwait/go.mod": "module example.com/hostwait\n\ngo 1.27\n",
		"hostwait/hostwait.go": `package hostwait

import (
	"context"
	"runtime"
)

// Block waits until ctx is cancelled and returns its error.
func Block(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

// Cancellable reports whether ctx can be cancelled.
func Cancellable(ctx context.Context) bool { return ctx.Done() != nil }

// Goroutines is runtime.NumGoroutine.
func Goroutines() int { return runtime.NumGoroutine() }
`,
		"app/ergo.mod": "module app\n",
		"app/go.mod":   "module app\n\ngo 1.27\n\nrequire example.com/hostwait v0.0.0\n\nreplace example.com/hostwait => ../hostwait\n",
		"app/cmd/app/main.ergo": `import go example.com/hostwait
import scope
import strconv
import timeout

// A Level 2 adapter of a Go function with a hidden context.
extern go runtime/trace.Log func traceLog(category string, message string) needs go.Foreign

func waits() needs Console | go.Foreign {
	scope.OnExit() {
		Console.Println("released, cancellable " + strconv.FormatBool(hostwait.Cancellable()))
	}
	hostwait.Block() catch {
	case go.Error as e:
		Console.Println("go error " + e.Text)
	}
	Console.Println("after the wait")
}

func main() needs Console | Ambient | go.Foreign {
	let before = hostwait.Goroutines()
	with context { Scheduler: NewLiveScheduler() } {
		timeout.After(20ms) { waits() } catch {
		case timeout.Expired:
			Console.Println("interrupted")
		}
	}
	traceLog("ergo", "after")
	Console.Println("cancellable " + strconv.FormatBool(hostwait.Cancellable()))
	Console.Println("leaked " + strconv.Itoa(hostwait.Goroutines() - before))
}
`,
	})
	env := goModuleEnv(tmp, filepath.Join(tmp, "noproxy"))
	run := func(args ...string) string {
		t.Helper()
		stdout, stderr, code := runSmokeCommand(t, commandIn(binary, app, env, args...))
		if code != 0 {
			t.Fatalf("ergo %s: exit %d\n%s%s", strings.Join(args, " "), code, stdout, stderr)
		}
		return string(stdout)
	}
	run("bind", "go", "example.com/hostwait")
	want := "released, cancellable false\ninterrupted\ncancellable true\nleaked 0\n"
	if got := run("run", "cmd/app"); got != want {
		t.Errorf("ergo run cmd/app:\n%s\nwant:\n%s", got, want)
	}
}
