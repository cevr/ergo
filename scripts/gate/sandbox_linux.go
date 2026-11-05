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
	"strings"
	"syscall"
	"time"
)

// sandboxReadyTimeout bounds the export: a clone of the object store and a
// checkout of the head commit (seconds on this repository).
const sandboxReadyTimeout = 5 * time.Minute

// sandbox is a running merge-tier sandbox: a holder process that owns the
// user and pid namespaces, and bwrap, whose first process holds the mounts.
// Closing the holder's input ends both, and the kernel stops every process
// left in the namespaces.
type sandbox struct {
	holder, bwrap *exec.Cmd
	holdIn, wrapIn io.Closer
	nsenter        string
	pid            int
}

// startSandbox starts the sandbox of a plan and waits for the export. An
// error means the merge tier cannot run sandboxed, and fails closed.
func startSandbox(plan sandboxPlan) (enclosure, error) {
	if err := plan.admissible(); err != nil {
		return nil, err
	}
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
	s := &sandbox{nsenter: nsenter}
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
		return nil, err
	}
	s.holdIn = holdIn
	if err := s.holder.Start(); err != nil {
		return nil, fmt.Errorf("no user namespace: %w", err)
	}
	fail := func(err error) (enclosure, error) {
		_ = s.close()
		return nil, err
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
	go func() {
		var child struct {
			Pid int `json:"child-pid"`
		}
		if err := json.NewDecoder(info).Decode(&child); err != nil {
			ready <- fmt.Errorf("bwrap named no child: %w", err)
			return
		}
		s.pid = child.Pid
		line, err := bufio.NewReader(stdout).ReadString('\n')
		if err != nil || line != "ready\n" {
			ready <- errors.New("the export did not finish")
			return
		}
		ready <- nil
	}()
	select {
	case err = <-ready:
	case <-time.After(sandboxReadyTimeout):
		err = fmt.Errorf("the export took over %s", sandboxReadyTimeout)
	}
	if err != nil {
		_ = s.close()
		return nil, fmt.Errorf("%w: %s", err, strings.Join(strings.Fields(stderr.String()), " "))
	}
	return s, nil
}

func (s *sandbox) wrap(argv []string, dir string) []string {
	return sandboxWrap(s.nsenter, s.pid, dir, argv)
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
	return nil
}
