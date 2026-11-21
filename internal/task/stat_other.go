//go:build !unix

package task

import (
	"io/fs"
	"os/exec"
)

func statOf(info fs.FileInfo) fileStat {
	return fileStat{size: info.Size(), mtime: info.ModTime().UnixNano(), mode: info.Mode()}
}

func ownGroup(*exec.Cmd) {}

func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		cmd.Process.Kill()
	}
}
