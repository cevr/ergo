package v1

import (
	"os"
	"path/filepath"
	"runtime"
)

// The bodies of std os's Self on the host (os/self.ergo) and of
// os.Current. Each directory is Go's own answer, so the host's Self is
// Go's by construction; an error becomes NoDir with Go's text.

// NoDir is os.NoDir: a directory Self cannot name. It is a record of a v1
// package, so the lowering binds os.NoDir to this Go type
// (docs/spec/stdlib.md, Types 4).
type NoDir struct{ Message string }

// Error is the failure's text: its Ergo name and fields, as the lowering
// writes a record's.
func (e NoDir) Error() string { return "os.NoDir{Message:" + e.Message + "}" }

// absDir is a Self directory from Go's answer: Go's error, or a relative
// directory (Go returns $HOME or $XDG_*_HOME as set, so HOME=relative/dir
// gives "relative/dir"), is NoDir, so every directory Self names is
// absolute (law DirsAreAbsolute).
func absDir(dir string, err error) (string, error) {
	if err != nil {
		return "", NoDir{Message: err.Error()}
	}
	if !filepath.IsAbs(dir) {
		return "", NoDir{Message: "the directory " + dir + " is relative"}
	}
	return dir, nil
}

// OSPid is the body of os.Host's Pid: Go's os.Getpid.
//
//ergo:host
//ergo:wait none
func OSPid() int { return os.Getpid() }

// OSHomeDir is the body of os.Host's HomeDir: Go's os.UserHomeDir.
//
//ergo:host
//ergo:wait none
func OSHomeDir() (string, error) {
	return absDir(os.UserHomeDir())
}

// OSCacheDir is the body of os.Host's CacheDir: Go's os.UserCacheDir.
//
//ergo:host
//ergo:wait none
func OSCacheDir() (string, error) {
	return absDir(os.UserCacheDir())
}

// OSConfigDir is the body of os.Host's ConfigDir: Go's os.UserConfigDir.
//
//ergo:host
//ergo:wait none
func OSConfigDir() (string, error) {
	return absDir(os.UserConfigDir())
}

// OSExecutable is the body of os.Host's Executable: Go's os.Executable,
// absolute. Go may give a relative path on some systems, so a relative
// one is made absolute against the working directory, where it was found.
//
//ergo:host
//ergo:wait none
func OSExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", NoDir{Message: err.Error()}
	}
	if !filepath.IsAbs(exe) {
		if exe, err = filepath.Abs(exe); err != nil {
			return "", NoDir{Message: err.Error()}
		}
	}
	return exe, nil
}

// OSPlatformOS is os.Current's OS: the GOOS the program was built for.
//
//ergo:host
//ergo:wait none
func OSPlatformOS() string { return runtime.GOOS }

// OSPlatformArch is os.Current's Arch: the GOARCH the program was built
// for.
//
//ergo:host
//ergo:wait none
func OSPlatformArch() string { return runtime.GOARCH }
