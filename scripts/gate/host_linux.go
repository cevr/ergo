package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// readHostFacts describes the host facts that Go tests read without package
// os, so Go's test cache cannot see them: the file system (statfs(2) magic
// number) of the step's temporary directory, tmpDir; of the step's GOTMPDIR,
// goTmpDir, when it is set, since t.TempDir makes the tests' directories
// there (and in tmpDir only when it is not set); and of /dev/shm, which the
// output-storage property admits or refuses; the file system at the cgroup
// root, which says whether cgroup v2 is there; and whether systemd-run can
// reach a user service manager, which decides how a gate a test starts
// bounds its steps. A fact that cannot be read is recorded as such, so that
// it becoming readable changes the description too.
func readHostFacts(tmpDir, goTmpDir string) string {
	scope := "systemd-run absent"
	if _, err := exec.LookPath("systemd-run"); err == nil {
		scope = "systemd-run, no user bus"
		if userBus() {
			scope = "systemd-run, user bus"
		}
	}
	goTmp := "unset"
	if goTmpDir != "" {
		goTmp = fileSystem(existingAncestor(goTmpDir))
	}
	return fmt.Sprintf("tmp %s; gotmp %s; shm %s; cgroup %s; %s",
		fileSystem(existingAncestor(tmpDir)), goTmp, fileSystem("/dev/shm"), fileSystem(cgroupRoot), scope)
}

// fileSystem is the statfs(2) magic number of the file system that holds
// name, or why it cannot be read.
func fileSystem(name string) string {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(name, &stat); err != nil {
		return "unreadable"
	}
	return fmt.Sprintf("%#x", uint32(stat.Type))
}

// existingAncestor is name, or its nearest ancestor that exists: the gate
// makes a step's temporary directory only when the step starts.
func existingAncestor(name string) string {
	for {
		if _, err := os.Stat(name); err == nil {
			return name
		}
		parent := filepath.Dir(name)
		if parent == name {
			return name
		}
		name = parent
	}
}

// userBus reports whether the user's service manager has a bus where
// systemd-run looks for it (scopeEnvironment).
func userBus() bool {
	if address := os.Getenv("DBUS_SESSION_BUS_ADDRESS"); address != "" {
		return true
	}
	runtime := os.Getenv("XDG_RUNTIME_DIR")
	if runtime == "" {
		runtime = fmt.Sprintf("/run/user/%d", os.Getuid())
	}
	_, err := os.Stat(filepath.Join(runtime, "bus"))
	return err == nil
}
