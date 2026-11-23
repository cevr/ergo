package exec

import (
	"os"
	"syscall"
	"time"

	"ergo.local/prototype/runtime/rt"
)

// ExecDetached is exec.Detached: a program started to outlive the code
// that started it. Pid is its process id and the id of its session and
// process group; Start is its start time as the host reports it (Linux:
// the clock ticks after boot, /proc/<pid>/stat field 22; Darwin: the
// microseconds after the epoch, kinfo_proc p_starttime; 0 where the host
// gives none), so Alive and Kill never act on another process that reused
// the pid. Name is the program's name, for messages.
type ExecDetached struct {
	Pid   int
	Start int64
	Name  string
}

// ExecSpawn is the body of exec.Host's Spawn on the caller's task t: it
// starts name with args as ExecStart does (env over the host's
// environment, dir, stdin), in a new session and process group (setsid),
// with its standard output and standard error going to the sinks outKind
// and errKind, which are Inherit, Discard or ToFile, or ToStdout for
// errKind (standard error goes where standard output goes): a ToCaller sink is
// Failed, as no caller outlives the program to read it. It returns once
// the program has started. A stop of t does not end the program, and
// neither does the end of this process: the program is reparented. While
// this process lives, a goroutine waits for the program, so its end
// leaves no zombie; that wait owns nothing else. stdin reaches the program
// only while this process lives.
//
//ergo:host
//ergo:wait caller
func ExecSpawn(t *rt.Task, name string, args []string, env map[string]string, dir string, stdin string, outKind int, outPath string, outAppend bool, errKind int, errPath string, errAppend bool) (ExecDetached, error) {
	t.Check()
	cmd, err := ExecCommand(name, args, env, dir, stdin)
	if err != nil {
		return ExecDetached{}, err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	var files []*os.File
	closeAll := func() {
		for _, f := range files {
			_ = f.Close() // the program holds its own copies
		}
	}
	for i, s := range []struct {
		kind   int
		path   string
		append bool
		label  string
	}{{outKind, outPath, outAppend, "stdout"}, {errKind, errPath, errAppend, "stderr"}} {
		var w *os.File
		switch s.kind {
		case execInherit:
			w = []*os.File{os.Stdout, os.Stderr}[i]
		case execDiscard:
			// nil: os/exec gives the program the null device.
		case execToFile:
			flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
			if s.append {
				flags = os.O_WRONLY | os.O_CREATE | os.O_APPEND
			}
			f, err := os.OpenFile(s.path, flags, 0o644)
			if err != nil {
				closeAll()
				return ExecDetached{}, ExecFailed{Name: name, Reason: s.label + " " + s.path + ": " + fsReason(err)}
			}
			files = append(files, f)
			w = f
		case execToCaller:
			closeAll()
			return ExecDetached{}, ExecFailed{Name: name, Reason: s.label + ": a detached program has no caller to read it (ToCaller)"}
		case execToStdout:
			if i == 0 {
				closeAll()
				return ExecDetached{}, execToItself(name)
			}
			cmd.Stderr = cmd.Stdout // nil (the null device) or standard output's file
			continue
		default:
			panic("exec: unknown sink kind")
		}
		if w == nil {
			continue
		}
		if i == 0 {
			cmd.Stdout = w
		} else {
			cmd.Stderr = w
		}
	}
	err = cmd.Start()
	closeAll()
	if err != nil {
		return ExecDetached{}, execErr(name, err)
	}
	pid := cmd.Process.Pid
	start := execStartTime(pid)
	go func() { _ = cmd.Wait() }() // the reap; see the doc comment
	return ExecDetached{Pid: pid, Start: start, Name: name}, nil
}

// ExecAlive is the body of exec.Host's Alive: the process pid has not
// ended (a zombie has) and its start time is start, so it is the program
// a Spawn started and not another one that reused the pid.
//
//ergo:host
//ergo:wait none
func ExecAlive(pid int, start int64) bool {
	if pid <= 0 {
		return false // no Spawn gives one; -1 and 0 would name groups
	}
	return execRunning(pid, start)
}

// execSpawnPoll is the first pause of Kill's wait for a group's end; each
// pause doubles up to execSpawnPollMax. The host gives no event for the
// end of a process this process is not the parent of.
const (
	execSpawnPoll    = 50 * time.Microsecond
	execSpawnPollMax = 10 * time.Millisecond
)

// ExecKill is the body of exec.Host's Kill on the caller's task t: when
// the process pid with the start time start is alive, it sends SIGTERM to
// its process group, waits up to grace for the group to end, then sends
// SIGKILL and waits (bounded by execReapWait, as Run does) until no
// process of the group is left. A process that is gone, or a pid another
// process now has, is no failure and gets no signal. A stop of t during
// the grace ends the wait early and goes on to SIGKILL. A grace of 0
// sends SIGKILL at once.
//
//ergo:host
//ergo:wait caller
func ExecKill(t *rt.Task, pid int, start int64, grace time.Duration) {
	if pid <= 0 || !execRunning(pid, start) {
		return
	}
	group := pid // Spawn's setsid made the program its group's leader
	// The program counts as well as its group: on Darwin a group whose
	// processes are exiting can answer ESRCH before the program is a
	// zombie, which a wait on the group alone took for its end (I19 macOS
	// check: TestExecSpawnOutlivesItsStarter, alive after Kill).
	left := func() bool { return syscall.Kill(-group, 0) == nil || execRunning(pid, start) }
	if grace > 0 {
		execSignalGroup(group, syscall.SIGTERM)
		deadline := time.NewTimer(grace)
		defer deadline.Stop()
	wait:
		for pause := execSpawnPoll; left(); pause = min(2*pause, execSpawnPollMax) {
			select {
			case <-deadline.C:
				break wait
			case <-t.Done():
				break wait
			case <-time.After(pause):
			}
		}
	}
	execKillGroup(group)
	if execRunning(pid, start) {
		_ = execKill(pid, syscall.SIGKILL) // the program itself, whatever its group answered
	}
	execAwaitGroupEnd(group)
	deadline := time.Now().Add(execReapWait)
	for pause := execSpawnPoll; execRunning(pid, start) && time.Now().Before(deadline); pause = min(2*pause, execSpawnPollMax) {
		time.Sleep(pause)
	}
}
