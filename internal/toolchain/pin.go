package toolchain

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"ergo.local/prototype/internal/pinset"
	"ergo.local/prototype/toolchain/ir"
	"ergo.local/prototype/toolchain/pin"
)

// PinSource is the pin sets of ergo's cache directory: installed under
// <cache>/toolchain/pins, named by toolchain/pin/lock, fetched from url
// (ERGO_PIN_URL; empty fetches none), with refusals on log.
func PinSource(cache, url string, log io.Writer) (pinset.Source, error) {
	lock, err := pinset.ParseLock(pin.Lock)
	if err != nil {
		return pinset.Source{}, err
	}
	return pinset.Source{Dir: filepath.Join(cache, "toolchain", "pins"), URL: url, Lock: lock, Log: log}, nil
}

// toolsFlags are the flags a tools set's compile is built with
// (buildCompile).
const toolsFlags = "-trimpath CGO_ENABLED=0"

// ToolsKey is the key of the tools set of a release and patch for the host
// goos/goarch: the patched compile.
func ToolsKey(release, patch, goos, goarch string) pinset.Key {
	return pinset.Key{Kind: "tools", Release: release, Patch: patch, GOOS: goos, GOARCH: goarch, Flags: toolsFlags}
}

// toolsSet is the patched compile of the installed tools set for this
// host, release and patch, when c has pin sets and one is there.
func (c PatchConfig) toolsSet(release, patch string) (string, bool) {
	if c.Pins == nil {
		return "", false
	}
	set := c.Pins.Open(ToolsKey(release, patch, runtime.GOOS, runtime.GOARCH).Name(), "the IR compiler")
	if set == nil {
		return "", false
	}
	e, ok := set.Entries["compile"]
	if !ok {
		return "", false
	}
	return set.Path(e), true
}

// copyExecutable copies from to a new executable file to.
func copyExecutable(from, to string) error {
	src, err := os.Open(from)
	if err != nil {
		return err
	}
	defer src.Close()
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	dst, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	_, err = io.Copy(dst, src)
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	return err
}

// PatchDigest is the sha256 of the IR patch toolchain/ir/lock ports to
// release.
func PatchDigest(release string) (string, error) {
	port, err := portOf(ir.Files, release)
	return port.digest, err
}

// ProduceTools builds the tools set of the pristine release at source for
// the host goos/goarch into dir: the release's cmd/compile with the IR
// patch, cross-built when the host is another.
func ProduceTools(source, goos, goarch, dir string) (pinset.Produced, error) {
	return produceTools(source, goos, goarch, dir, ir.Files)
}

func produceTools(source, goos, goarch, dir string, lockFS fs.FS) (pinset.Produced, error) {
	data, err := os.ReadFile(filepath.Join(source, "VERSION"))
	if err != nil {
		return pinset.Produced{}, err
	}
	release, _, _ := strings.Cut(string(data), "\n")
	release = strings.TrimSpace(release)
	port, err := portOf(lockFS, release)
	if err != nil {
		return pinset.Produced{}, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return pinset.Produced{}, err
	}
	tmp, err := os.MkdirTemp(dir, "tools-")
	if err != nil {
		return pinset.Produced{}, err
	}
	defer os.RemoveAll(tmp)
	if err := patchInto(tmp, source, port, buildCompileFor(goos, goarch)); err != nil {
		return pinset.Produced{}, fmt.Errorf("build the IR compiler for %s %s/%s: %w", release, goos, goarch, err)
	}
	compile := filepath.Join(tmp, "goroot", "pkg", "tool", hostTools(), "compile"+exe())
	digest, err := pinset.FileDigest(compile)
	if err != nil {
		return pinset.Produced{}, err
	}
	name := ToolsKey(release, port.digest, goos, goarch).Name()
	return pinset.Write(dir, name, []pinset.Entry{{Name: "compile", Digest: digest, Export: "-"}}, map[string]string{"compile": compile})
}
