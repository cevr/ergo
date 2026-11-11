// Package irspike is the EXPERIMENTAL driver of the wf10 IR1 spike (owner
// q128, 2026-11-11): it builds an Ergo program through the source path
// (lower.Lower, stock go1.27.0) and through the typed-IR path
// (lower.LowerIR, the go1.27.0 derivative's -ergoir entry) with the same
// flags, and measures both. Nothing in the default build reads it; source
// emission stays the reference.
package irspike

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"ergo.local/prototype/internal/receipt"
	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/sketchfront"
)

// Path is the way a program reaches the Go compiler.
type Path int

const (
	// Source is the reference: Go source text, stock compiler.
	Source Path = iota
	// IR is typed IR bodies for the subset, the derivative compiler.
	IR
)

func (p Path) String() string {
	if p == IR {
		return "ir"
	}
	return "source"
}

// Toolchain is a Go installation and the build cache its builds use.
type Toolchain struct {
	GOROOT  string
	GOCACHE string
}

// Toolchains are the two installations the spike compares, from
// ERGO_IRSPIKE_GOROOT_SOURCE (a pristine go1.27.0), ERGO_IRSPIKE_GOROOT_IR
// (the derivative) and ERGO_IRSPIKE_CACHE (a directory for their caches).
// ok is false when one is unset.
func Toolchains() (src, ir Toolchain, ok bool) {
	s, i, c := os.Getenv("ERGO_IRSPIKE_GOROOT_SOURCE"), os.Getenv("ERGO_IRSPIKE_GOROOT_IR"), os.Getenv("ERGO_IRSPIKE_CACHE")
	if s == "" || i == "" || c == "" {
		return Toolchain{}, Toolchain{}, false
	}
	return Toolchain{GOROOT: s, GOCACHE: filepath.Join(c, "source")}, Toolchain{GOROOT: i, GOCACHE: filepath.Join(c, "ir")}, true
}

// Load checks the program at path (a .ergo file or a package folder) and
// returns its lowering unit.
func Load(path string) (lower.Unit, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return lower.Unit{}, err
	}
	var a sketchfront.Analysis
	if info, err := os.Stat(abs); err == nil && info.IsDir() {
		a, err = sketchfront.CheckDir(abs)
		if err != nil {
			return lower.Unit{}, err
		}
	} else {
		text, err := os.ReadFile(abs)
		if err != nil {
			return lower.Unit{}, err
		}
		a = sketchfront.Check(string(text))
	}
	if len(a.Diagnostics) > 0 {
		return lower.Unit{}, fmt.Errorf("%s does not check: %s", path, a.Diagnostics[0].Message)
	}
	return sketchfront.IRSpikeUnit(a, abs)
}

// Emit lowers u by path p and writes the Go module into dir, which must
// not exist yet.
func Emit(u lower.Unit, p Path, dir string) (lower.IRReport, error) {
	var files []lower.File
	var rep lower.IRReport
	var err error
	if p == IR {
		files, rep, err = lower.LowerIR(u)
	} else {
		files, err = lower.Lower(u)
	}
	if err != nil {
		return rep, err
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return rep, err
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		return rep, err
	}
	for _, f := range append(files, lower.Runtime()...) {
		name := filepath.Join(dir, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			return rep, err
		}
		if err := os.WriteFile(name, f.Data, 0o644); err != nil {
			return rep, err
		}
	}
	return rep, nil
}

// compileID is the derivative compiler's content hash. A release-versioned
// derivative prints the stock version to -V=full, so the go command's
// action IDs cannot tell it from the stock compiler or from its own
// earlier builds; its cache is therefore keyed by this hash.
func compileID(goroot string) (string, error) {
	f, err := os.Open(filepath.Join(goroot, "pkg", "tool", "linux_amd64", "compile"))
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}

// stableIR is a copy of the IR file named by its content hash, beside the
// build cache: the -ergoir flag, which the go command keys the compile
// on, then names the IR's content, wherever the module was written.
func stableIR(cache, file string) (string, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	name := filepath.Join(cache+"-ir", hex.EncodeToString(sum[:])+".json")
	if _, err := os.Stat(name); err == nil {
		return name, nil
	}
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return "", err
	}
	return name, os.WriteFile(name, data, 0o644)
}

// BuildOptions are the extra flags of one build.
type BuildOptions struct {
	// Toolexec runs each tool through this command (a meter).
	Toolexec string
	// Strip links without symbol and DWARF tables (-ldflags=-s -w).
	Strip bool
	// Race builds with the race detector (it needs cgo).
	Race bool
	// GCFlags are extra compiler flags of the program's root package.
	GCFlags []string
	// Stderr receives the go command's output.
	Stderr io.Writer
}

// Command is the go build of the module in dir to out by path p: the
// flags of ergo's own build action (receipt.GoBuildFlags), and for IR the
// -ergoir flag on the program's root package.
func Command(tc Toolchain, p Path, dir, out string, o BuildOptions) (*exec.Cmd, error) {
	cache := tc.GOCACHE
	args := append([]string{"build"}, receipt.GoBuildFlags...)
	gcflags := o.GCFlags
	if p == IR {
		id, err := compileID(tc.GOROOT)
		if err != nil {
			return nil, err
		}
		cache += "-" + id
		irFile, err := stableIR(cache, filepath.Join(dir, lower.IRFileName))
		if err != nil {
			return nil, err
		}
		gcflags = append([]string{"-ergoir=" + irFile}, gcflags...)
	}
	if len(gcflags) > 0 {
		args = append(args, "-gcflags=ergo.local/program="+strings.Join(gcflags, " "))
	}
	if o.Toolexec != "" {
		args = append(args, "-toolexec="+o.Toolexec)
	}
	if o.Strip {
		args = append(args, "-ldflags=-s -w")
	}
	if o.Race {
		args = append(args, "-race")
	}
	args = append(args, "-o", out, ".")
	cmd := exec.Command(filepath.Join(tc.GOROOT, "bin", "go"), args...)
	cmd.Dir = dir
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool {
		for _, k := range []string{"GOROOT=", "GOCACHE=", "GOWORK=", "CGO_ENABLED=", "GOFLAGS=", "GOTOOLCHAIN=", "GOOS=", "GOARCH="} {
			if strings.HasPrefix(kv, k) {
				return true
			}
		}
		return false
	})
	cgo := "0"
	if o.Race {
		cgo = "1"
	}
	cmd.Env = append(env, "GOROOT="+tc.GOROOT, "GOCACHE="+cache, "GOWORK=off", "CGO_ENABLED="+cgo, "GOTOOLCHAIN=local")
	cmd.Stdout, cmd.Stderr = o.Stderr, o.Stderr
	return cmd, nil
}

// Build builds the module in dir to out and measures the go command's
// process tree.
func Build(tc Toolchain, p Path, dir, out string, o BuildOptions) (Cost, error) {
	var stderr strings.Builder
	if o.Stderr == nil {
		o.Stderr = &stderr
	}
	cmd, err := Command(tc, p, dir, out, o)
	if err != nil {
		return Cost{}, err
	}
	c, err := Measure(cmd)
	if err != nil {
		return c, fmt.Errorf("%s build: %w\n%s", p, err, stderr.String())
	}
	return c, nil
}

// ErrNoToolchains is the skip reason when Toolchains is not configured.
var ErrNoToolchains = errors.New("set ERGO_IRSPIKE_GOROOT_SOURCE, ERGO_IRSPIKE_GOROOT_IR and ERGO_IRSPIKE_CACHE")
