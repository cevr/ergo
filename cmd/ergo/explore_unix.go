//go:build unix

package main

import (
	"os"
	"os/exec"
	"syscall"

	"ergo.local/prototype/internal/proclimit"
	"ergo.local/prototype/runtime/explore/sched"
)

// startWorker starts the explored test binary as a worker (sched.Serve in
// runtime/v1's explore_on.go): in a process group of its own, under the
// test memory limit, its requests on descriptor 3 and its replies on
// descriptor 4, its standard output and error appended to out.
func startWorker(binary string, args []string, out *os.File, limit uint64) (sched.Conn, error) {
	reqR, reqW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	repR, repW, err := os.Pipe()
	if err != nil {
		reqR.Close()
		reqW.Close()
		return nil, err
	}
	cmd := exploreWorkerCommand(binary, args, out, reqR, repW, limit)
	err = proclimit.Start(cmd, limit)
	reqR.Close()
	repW.Close()
	if err != nil {
		reqW.Close()
		repR.Close()
		return nil, err
	}
	return &workerConn{cmd: cmd, req: reqW, rep: repR}, nil
}

// exploreWorkerCommand prepares the exact command startWorker launches; it
// does not open descriptors or start a process.
func exploreWorkerCommand(binary string, args []string, out, reqR, repW *os.File, limit uint64) *exec.Cmd {
	cmd := exec.Command(binary, args...)
	cmd.Env = append(boundedProgramEnv(limit), sched.WorkerEnv+"=1")
	cmd.Stdout, cmd.Stderr = out, out
	cmd.ExtraFiles = []*os.File{reqR, repW}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd
}

// workerConn is a worker process.
type workerConn struct {
	cmd *exec.Cmd
	req *os.File
	rep *os.File
}

func (c *workerConn) Read(p []byte) (int, error)  { return c.rep.Read(p) }
func (c *workerConn) Write(p []byte) (int, error) { return c.req.Write(p) }

// Close ends the worker: it kills the worker's process group, which this
// process started, and reaps the worker.
func (c *workerConn) Close() error {
	c.req.Close()
	_ = syscall.Kill(-c.cmd.Process.Pid, syscall.SIGKILL)
	err := c.cmd.Wait()
	c.rep.Close()
	return err
}
