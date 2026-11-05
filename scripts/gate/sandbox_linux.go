package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// sandboxReadyTimeout bounds bwrap's start, until its first process says
// ready.
const sandboxReadyTimeout = 5 * time.Minute

// sandbox is a running merge-tier sandbox: a holder process that owns the
// user and pid namespaces, and bwrap, whose first process holds the mounts.
// Closing the holder's input ends both, and the kernel stops every process
// left in the namespaces.
type sandbox struct {
	holder, bwrap  *exec.Cmd
	holdIn, wrapIn io.Closer
	nsenter        string
	pid            int
	fresh          string
	modCache       string
	goRoot         string
}

// startSandbox starts the sandbox of a plan: it admits the read-only trees
// by their resolved identity and masks what their check found
// (admitInputs), exports the head commit and seeds the module cache on the
// host, into the run's directory, and waits until bwrap's first process is
// ready. An error means the merge tier cannot run sandboxed, and fails
// closed.
func startSandbox(plan sandboxPlan) (enclosure, error) {
	admitted, err := admitInputs(plan)
	if err != nil {
		return nil, err
	}
	plan.Admitted = admitted
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		return nil, errors.New("no bwrap (bubblewrap) on PATH")
	}
	nsenter, err := exec.LookPath("nsenter")
	if err != nil {
		return nil, errors.New("no nsenter on PATH")
	}
	hold, err := exec.LookPath("cat")
	if err != nil {
		return nil, errors.New("no cat on PATH")
	}
	// The run's directory: Mkdir fails if it exists, so every directory
	// the run writes starts empty.
	if err := os.MkdirAll(filepath.Dir(plan.Fresh), 0o700); err != nil {
		return nil, fmt.Errorf("the runs' directory: %w", err)
	}
	if err := os.Mkdir(plan.Fresh, 0o700); err != nil {
		return nil, fmt.Errorf("the run's directory: %w", err)
	}
	s := &sandbox{nsenter: nsenter, fresh: plan.Fresh, modCache: plan.ModCache, goRoot: plan.GoRoot}
	fail := func(err error) (enclosure, error) {
		_ = s.close()
		return nil, err
	}
	for i := range plan.Scratch {
		if err := os.Mkdir(sandboxFresh(plan.Fresh, i), 0o700); err != nil {
			return fail(err)
		}
	}
	if i := slices.Index(plan.Scratch, plan.Checkout); i < 0 {
		return fail(fmt.Errorf("the checkout %s is not a scratch path", plan.Checkout))
	} else if err := exportCommit(plan.Source, plan.Head, sandboxFresh(plan.Fresh, i)); err != nil {
		return fail(err)
	}
	if plan.ModCache != "" {
		if _, err := seedModules(plan.Source, plan.Head, plan.HostModCache, sandboxFresh(plan.Fresh, slices.Index(plan.Scratch, plan.ModCache))); err != nil {
			return fail(fmt.Errorf("the module inputs: %w", err))
		}
	}
	// The holder is the first process of a new pid namespace, in a new
	// user namespace that maps only the gate's own user: it reads its
	// input until the gate closes it or exits.
	s.holder = exec.Command(hold)
	uid, gid := os.Getuid(), os.Getgid()
	s.holder.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWUSER | syscall.CLONE_NEWPID,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: uid, HostID: uid, Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: gid, HostID: gid, Size: 1}},
	}
	holdIn, err := s.holder.StdinPipe()
	if err != nil {
		return fail(err)
	}
	s.holdIn = holdIn
	if err := s.holder.Start(); err != nil {
		return fail(fmt.Errorf("no user namespace: %w", err))
	}
	var files []*os.File
	for _, name := range []string{"user", "pid"} {
		file, err := os.Open(fmt.Sprintf("/proc/%d/ns/%s", s.holder.Process.Pid, name))
		if err != nil {
			return fail(err)
		}
		defer file.Close()
		files = append(files, file)
	}
	info, infoWrite, err := os.Pipe()
	if err != nil {
		return fail(err)
	}
	defer info.Close()
	s.bwrap = exec.Command(bwrap, sandboxArgv(plan)...)
	s.bwrap.ExtraFiles = append(files, infoWrite)
	var stderr bytes.Buffer
	s.bwrap.Stderr = &stderr
	wrapIn, err := s.bwrap.StdinPipe()
	if err != nil {
		infoWrite.Close()
		return fail(err)
	}
	s.wrapIn = wrapIn
	stdout, err := s.bwrap.StdoutPipe()
	if err != nil {
		infoWrite.Close()
		return fail(err)
	}
	err = s.bwrap.Start()
	infoWrite.Close()
	if err != nil {
		return fail(fmt.Errorf("start bwrap: %w", err))
	}
	ready := make(chan error, 1)
	var childPid atomic.Int64
	go func() {
		var child struct {
			Pid int `json:"child-pid"`
		}
		if err := json.NewDecoder(info).Decode(&child); err != nil {
			ready <- fmt.Errorf("bwrap named no child: %w", err)
			return
		}
		childPid.Store(int64(child.Pid))
		line, err := bufio.NewReader(stdout).ReadString('\n')
		if err != nil || line != "ready\n" {
			ready <- errors.New("the export did not finish")
			return
		}
		ready <- nil
	}()
	// bwrap, given a pid namespace and --unshare-pid, does not exit when
	// its first process fails (a mount that cannot be made, measured: it
	// waits until killed), so the gate watches that process itself.
	deadline := time.After(sandboxReadyTimeout)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for waiting := true; waiting; {
		select {
		case err = <-ready:
			waiting = false
		case <-tick.C:
			if pid := childPid.Load(); pid != 0 && processEnded(int(pid)) {
				err, waiting = errors.New("the sandbox's first process ended before the export finished"), false
			}
		case <-deadline:
			err, waiting = fmt.Errorf("the export took over %s", sandboxReadyTimeout), false
		}
	}
	if err == nil {
		s.pid = int(childPid.Load())
	}
	if err != nil {
		// A sandbox that failed has nothing to keep: bwrap need not be
		// waited for.
		_ = s.bwrap.Process.Kill()
		_ = s.close()
		return nil, fmt.Errorf("%w: %s", err, strings.Join(strings.Fields(stderr.String()), " "))
	}
	return s, nil
}

// processEnded reports whether the process pid is gone or a zombie.
func processEnded(pid int) bool {
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return true
	}
	// The state follows the command name's closing parenthesis.
	end := bytes.LastIndexByte(stat, ')')
	return end < 0 || end+2 >= len(stat) || stat[end+2] == 'Z' || stat[end+2] == 'X'
}

func (s *sandbox) wrap(argv []string, dir string) []string {
	return sandboxWrap(s.nsenter, s.pid, dir, argv)
}

func (s *sandbox) environment(env []string) []string {
	return sandboxEnvironment(env, s.modCache, s.goRoot)
}

// close ends the sandbox. The holder goes first: when the first process of
// the pid namespace exits, the kernel stops every process left in it and in
// the sandbox's namespace below it, and bwrap, which otherwise waits on its
// child well after the export's input closed (measured: 10 s, to the kill),
// sees its child end. A process that outlives 10 s is killed.
func (s *sandbox) close() error {
	if s.wrapIn != nil {
		s.wrapIn.Close()
	}
	if s.holdIn != nil {
		s.holdIn.Close()
	}
	for _, command := range []*exec.Cmd{s.holder, s.bwrap} {
		if command == nil || command.Process == nil {
			continue
		}
		done := make(chan struct{})
		go func() {
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				_ = command.Process.Kill()
			}
		}()
		_ = command.Wait()
		close(done)
	}
	// Every process in the sandbox has ended: remove what the run wrote,
	// restoring the permissions a step took away (emptyStepTempDir).
	if s.fresh == "" {
		return nil
	}
	if err := emptyStepTempDir(s.fresh); err != nil {
		return err
	}
	return os.Remove(s.fresh)
}
