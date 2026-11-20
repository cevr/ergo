//go:build unix

package task

import (
	"io/fs"
	"os/exec"
	"syscall"
)

// statOf is the part of a file's stat that changes when its content can
// have: size, modification and change times, inode and mode.
func statOf(info fs.FileInfo) fileStat {
	st := fileStat{size: info.Size(), mtime: info.ModTime().UnixNano(), mode: info.Mode()}
	if s, ok := info.Sys().(*syscall.Stat_t); ok {
		st.ino = s.Ino
		st.ctime = ctimeOf(s)
	}
	return st
}

// ownGroup puts cmd in a process group of its own, which killGroup ends.
func ownGroup(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

// killGroup ends the process group cmd started, and only it.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil && cmd.Process.Pid > 1 {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
