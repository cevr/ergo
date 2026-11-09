package lower

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"ergo.local/prototype/internal/lawledger"
)

// TestExecRuns: std exec (stdlib.md, exec) runs a program through the
// host's Runner, which main binds under Ambient. A status that is not 0 is
// a Result; a missing program is exec.NotFound, which a catch arm takes by
// name and which an uncaught failure writes with its text and status 1.
// A stopped task's Run ends its program: main stops each task once its
// program has started, and a stopped program never reaches the line after
// its 20s sleep; a stopped pipeline leaves none of its processes (an
// Effect case). A Script binds in the same place and gives back what it
// holds.
//
// Each check is an event, not a wall-clock bound (common.md, Test rule).
// A bound of 15s on the whole test, the build included, failed a correct
// run whose go build compiled the runtime into a cold cache (exec-flake:
// 17.5s with a fresh GOCACHE and GOMAXPROCS=1, 0.9s warm). The waits in
// the program end when a file holds the line they wait for, or when the
// program that started them is gone, so no wait outlives a hung test.
func TestExecRuns(t *testing.T) {
	// The started program and the pipeline's processes write their pids
	// to started and pids, and a program that runs past its sleep writes
	// to late. Each file exists before the program runs, so the shell's
	// tests read it at once.
	dir := t.TempDir()
	started, pidfile, late := filepath.Join(dir, "started"), filepath.Join(dir, "pids"), filepath.Join(dir, "late")
	for _, f := range []string{started, pidfile, late} {
		if err := os.WriteFile(f, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runBehaviors(t, []behavior{
		{name: "exec", src: strings.NewReplacer("STARTED", started, "PIDFILE", pidfile, "LATE", late).Replace(`import exec
import task

func show(c exec.Command) needs Console | exec.Runner {
	let r = exec.Runner.Run(c) catch {
	case exec.NotFound as n:
		Console.Println("not found \(n.Name)")
		return
	case exec.Denied as d:
		Console.Println("denied \(d.Name)")
		return
	case exec.Failed as f:
		Console.Println("failed \(f.Name)")
		return
	}
	Console.Println("[\(r.Stdout)|\(r.Stderr)|\(r.Status)]")
}

func run() needs Console | exec.Runner {
	show(exec.New("sh", []string{"-c", "echo a; echo b >&2; exit 3"}))
	show(exec.New("sh", []string{"-c", "printf '%s %s' \"$ERGO_X\" \"$(cat)\""}).WithEnv("ERGO_X", "env").WithStdin("in"))
	show(exec.New("pwd", []string{}).WithDir("/"))
	show(exec.New("ergo-exec-missing-7f3a", []string{}))
	show(exec.New("true", []string{}).WithDir("/nonexistent/loop-probe-x"))
	let t = task.Go() { show(exec.New("sh", []string{"-c", "echo $$ >> STARTED; sleep 20; echo late >> LATE"})) }
	show(exec.New("sh", []string{"-c", "while [ ! -s STARTED ] && ps -p $PPID > /dev/null; do sleep 0.01; done"}))
	t.Stop()
	Console.Println("stopped")
	let p = task.Go() { show(exec.New("sh", []string{"-c", "(sleep 20 & echo $! >> PIDFILE; wait) | (sleep 20 & echo $! >> PIDFILE; wait) & while [ $(wc -l < PIDFILE) -lt 2 ]; do sleep 0.01; done; echo $$ >> PIDFILE; wait; echo late >> LATE"})) }
	show(exec.New("sh", []string{"-c", "while [ $(wc -l < PIDFILE) -lt 3 ] && ps -p $PPID > /dev/null; do sleep 0.01; done"}))
	p.Stop()
	Console.Println("pipeline stopped")
}

func main() needs Console | Ambient {
	with context { exec.Runner: exec.NewHost() } { run() }
	let mut results = Map[string, exec.Result]{}
	let c = exec.New("git", []string{"status"})
	results[exec.Key(c)] = exec.Result{Status: 1, Stdout: "recorded", Stderr: ""}
	with context { exec.Runner: exec.Script{Results: results} } {
		show(c)
		show(exec.New("git", []string{"log"}))
	}
}
`), stdout: "[a\n|b\n|3]\n[env in||0]\n[/\n||0]\nnot found ergo-exec-missing-7f3a\nfailed true\n[||0]\nstopped\n[||0]\npipeline stopped\n[recorded||1]\nnot found git\n"},
		{name: "execUncaught", src: `import exec

func main() fails exec.NotFound | exec.Denied | exec.Failed needs Console | Ambient {
	let r = try exec.NewHost().Run(exec.New("/nonexistent/loop-probe-x", []string{}))
	Console.Println(r.Stdout)
}
`, exit: 1, stderr: []string{"error: exec.NotFound{Name:/nonexistent/loop-probe-x}\n"}},
	})
	// Stop returns after the task ends, so a stop that did not end the
	// program makes it run past its sleep and write to late before main
	// goes on: the order of the kill and the program's own end, which no
	// load on the host changes.
	if data, err := os.ReadFile(late); err != nil {
		t.Fatal(err)
	} else if len(data) > 0 {
		t.Errorf("a stopped program ran to its end (late holds %q): the stop did not end the program", data)
	}
	// Effect: NodeChildProcessSpawner.test.ts "kills every process in a
	// pipeline". The stopped task's program ran a pipeline of two stages,
	// each with a process of its own, and wrote their pids and then its
	// own; after the stop, no process of the program's group is left.
	t.Run("effect.child-process.kills-pipeline", func(t *testing.T) {
		data, err := os.ReadFile(pidfile)
		if err != nil {
			t.Fatal(err)
		}
		pids := strings.Fields(string(data))
		if len(pids) != 3 {
			t.Fatalf("pids %q; want the two stages' and the program's", pids)
		}
		for _, pid := range pids {
			if inGroup(pid, pids[2]) {
				t.Errorf("process %s of the stopped pipeline (group %s) is left", pid, pids[2])
			}
		}
	})
}

// inGroup reports whether pid names a live process or a zombie of the
// process group pgid, from its /proc stat line (Linux). A pid with no
// entry, or one the host gave to a process of another group, is not.
func inGroup(pid, pgid string) bool {
	data, err := os.ReadFile("/proc/" + pid + "/stat")
	if err != nil {
		return false
	}
	s := string(data)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return true // the kernel is tearing the process down: it is still there
	}
	f := strings.Fields(s[i+1:])
	return len(f) < 3 || f[2] == pgid
}

// execLawsProbe restates each law whose ledger runner is TestExecLaws, as
// a test of the same name (TestStdLawsRun's naming), and the Runner law
// against a Script, the std's test provider.
const execLawsProbe = `import exec

test ExecWithEnvOverrides(key string, first string, second string) {
	let c = exec.New("x", []string{}).WithEnv(key, first).WithEnv(key, second)
	assert c.Env.Get(key) == Some[string]{Value: second}
}

test ExecWithEnvKeepsTheOriginal(key string, first string, second string) {
	let c = exec.New("x", []string{}).WithEnv(key, first)
	let d = c.WithEnv(key, second)
	assert c.Env.Get(key) == Some[string]{Value: first}
	assert d.Env.Get(key) == Some[string]{Value: second}
}

test ExecScriptReplaysRecorded(name string, arg string, status int, out string) {
	let c = exec.New(name, []string{arg, arg})
	let joined = exec.New(name, []string{arg + "," + arg})
	let mut results = Map[string, exec.Result]{}
	results[exec.Key(c)] = exec.Result{Status: status, Stdout: out, Stderr: ""}
	results[exec.Key(joined)] = exec.Result{Status: status + 1, Stdout: out, Stderr: ""}
	let r = try exec.Script{Results: results}.Run(c)
	let s = try exec.Script{Results: results}.Run(joined)
	assert r.Status == status
	assert r.Stdout == out
	assert r.Stderr == ""
	assert s.Status == status+1
}

test ScriptNotFoundNamesTheProgram(name string, other string) {
	let mut results = Map[string, exec.Result]{}
	results[exec.Key(exec.New(other, []string{}))] = exec.Result{Status: 0, Stdout: "", Stderr: ""}
	let s = exec.Script{Results: results}
	let mut named = true
	s.Run(exec.New(name, []string{})) catch {
	case exec.NotFound as n:
		named = n.Name == name
	case exec.Denied, exec.Failed:
		named = true
	}
	assert named
}
`

// execLawStatements is the statement of each law the probe restates, the
// law's source text as the ledger writes it, by ledger key; lawledger.Run
// holds it equal to the ledger (fu-ledger-runner).
var execLawStatements = map[string]string{
	"exec ScriptReplaysRecorded":   `ScriptReplaysRecorded(name string, arg string, status int, out string) { let c = New(name, []string{arg, arg}) let joined = New(name, []string{arg + "," + arg}) let mut results = Map[string, Result]{} results[Key(c)] = Result{Status: status, Stdout: out, Stderr: ""} results[Key(joined)] = Result{Status: status + 1, Stdout: out, Stderr: ""} let r = Script{Results: results}.Run(c) catch { case NotFound, Denied, Failed: return false } let s = Script{Results: results}.Run(joined) catch { case NotFound, Denied, Failed: return false } r.Status == status && r.Stdout == out && r.Stderr == "" && s.Status == status+1 }`,
	"exec WithEnvKeepsTheOriginal": `WithEnvKeepsTheOriginal(key string, first string, second string) { let c = New("x", []string{}).WithEnv(key, first) let d = c.WithEnv(key, second) c.Env.Get(key) == Some[string]{Value: first} && d.Env.Get(key) == Some[string]{Value: second} }`,
	"exec WithEnvOverrides":        `WithEnvOverrides(key string, first string, second string) { let c = New("x", []string{}).WithEnv(key, first).WithEnv(key, second) c.Env.Get(key) == Some[string]{Value: second} }`,
}

// TestExecLaws runs the probe with --seed 1; lawledger.Run holds that it
// restates exactly the ledger's laws whose runner is this test, each with
// the row's statement, and that each passed. ScriptNotFoundNamesTheProgram
// is the probe's own test of the Script provider, not a ledger row.
func TestExecLaws(t *testing.T) {
	exes := buildAll(t, []*lowered{lowerTestBuild(t, "execlaws", execLawsProbe)}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExeArgs(t, exes["execlaws"], "--seed", "1")
	if code != 0 {
		t.Errorf("the exec law tests failed: exit %d\n%s\n%s", code, stdout, firstLines(stderr, 20))
	}
	if !regexp.MustCompile(`(?m)^ok ScriptNotFoundNamesTheProgram( \(\d+ cases\))?$`).MatchString(stdout) {
		t.Errorf("no ok line for ScriptNotFoundNamesTheProgram:\n%s", stdout)
	}
	lawledger.Run(t, lawsPassed(t, execLawStatements, func(key string) string {
		pkg, law, _ := strings.Cut(key, " ")
		return stdLawsTestName(pkg, law)
	}, execLawsProbe, stdout, "ScriptNotFoundNamesTheProgram"))
}
