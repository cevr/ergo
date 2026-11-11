package closure

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"ergo.local/prototype/internal/gobind"
)

// The std store: a Go build cache that holds only the compiled standard
// library, kept across sweeps, from which each package's shared cache
// (runPlan) starts. A swept run starts with an empty GOCACHE, so a package
// whose tests build Go programs compiled the standard library again for
// each platform it builds for, under observation. The sweep of cmd/ergo
// (two runs, each the package's tests in effect) took 1073 CPU-s without
// the store and 681 CPU-s with it, 6m4s and 4m27s, at load 21-27
// (gate16b-fix2, measured on one host); at the batch-16 gate's load the
// runs without it took 18m37s and 18m50s, at their 19m30s deadline.
//
// It hides no input of the closure. A cache entry stands for the reads of
// the build that made it (sharedcache_test.go), and the store's builds are
// `go build std` alone, with cgo off, run from an empty directory outside
// the checkout: they compile packages of GOROOT only and run no C linker,
// and fillStdStore refuses a GOROOT inside the checkout. The closure classifies only inputs inside the
// working tree, so a read of GOROOT that a store entry saves a run was
// never an input it checks.

// stdStoreDir is the directory, under the temporary directory, that holds
// the store of the current toolchain and the lock that guards its filling.
const stdStoreDir = "ergo-closure-std"

// stdStoreComplete marks a store whose builds all ended.
const stdStoreComplete = "complete"

// stdBuild is one go command that fills the store: its arguments and the
// variables it sets over the sweep's environment.
type stdBuild struct {
	env  []string
	args []string
}

// stdBuilds are the store's builds: the standard library with -trimpath
// and cgo off for each platform a program links for (internal/sketch/build,
// Toolchain.Command, with receipt.GoBuildFlags, whose other flags do not
// change a compile). Every build has cgo off: a cgo build of std
// (runtime/cgo, net) runs GNU ld, whose lookups in its working directory
// are inputs the closure declares (everyPackage, -lc and the others) and
// that the store, filled outside the checkout, would hide. The tests' own
// cgo builds of the host std (the CLI's go build, go test -c) stay in each
// package's runs, as before the store (sweep of cmd/ergo at gate16b-fix2:
// with cgo builds in the store those six reads were gone).
func stdBuilds() []stdBuild {
	var builds []stdBuild
	for _, p := range gobind.Platforms {
		builds = append(builds, stdBuild{env: []string{"CGO_ENABLED=0", "GOOS=" + p.GOOS, "GOARCH=" + p.GOARCH}, args: []string{"build", "-trimpath", "std"}})
	}
	return builds
}

// goSettings are the go env values that choose the store: the toolchain a
// test's go command runs in the checkout, and what changes its compiles.
var goSettings = []string{"GOVERSION", "GOROOT", "GOFLAGS", "GOAMD64", "GOARM64", "GOEXPERIMENT", "CGO_ENABLED", "CC", "CGO_CFLAGS"}

// fillStdStore returns the std store for the toolchain the checkout at root
// selects, and fills it first if no sweep did: under a lock, into a fresh
// directory that becomes the store only once every build ended, its files
// read-only so that no run can change an entry through its link. It
// removes the stores of other toolchains. A GOROOT inside root is refused:
// its entries would hide inputs of the checkout.
func fillStdStore(root string) (string, error) {
	env := sweepEnvironment()
	query := exec.Command("go", append([]string{"env", "-json"}, goSettings...)...)
	query.Dir, query.Env = root, env
	out, err := query.Output()
	if err != nil {
		return "", fmt.Errorf("go env in %s: %w", root, err)
	}
	var settings map[string]string
	if err := json.Unmarshal(out, &settings); err != nil {
		return "", fmt.Errorf("go env: %w", err)
	}
	if rel, err := filepath.Rel(root, settings["GOROOT"]); err != nil || rel == "." || !strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("GOROOT %s lies in the checkout %s: the std store would hide its inputs", settings["GOROOT"], root)
	}
	sum := sha256.New()
	fmt.Fprintf(sum, "%s\n%v\n", out, stdBuilds())
	key := hex.EncodeToString(sum.Sum(nil))[:16]

	parent := filepath.Join(os.TempDir(), stdStoreDir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", err
	}
	lock, err := os.OpenFile(filepath.Join(parent, "lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return "", err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return "", err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	store := filepath.Join(parent, key)
	if _, err := os.Stat(filepath.Join(store, stdStoreComplete)); err == nil {
		return store, nil
	}
	// Under the lock no other sweep fills, so every other entry is a store
	// of another toolchain or an ended fill.
	entries, err := os.ReadDir(parent)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.Name() != "lock" {
			if err := removeTree(filepath.Join(parent, e.Name())); err != nil {
				return "", err
			}
		}
	}
	fill, err := os.MkdirTemp(parent, key+".fill-")
	if err != nil {
		return "", err
	}
	work, err := os.MkdirTemp("", "ergo-closure-std-work-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(work)
	for _, b := range stdBuilds() {
		build := exec.Command("go", b.args...)
		build.Dir = work
		build.Env = withEnv(env, append([]string{"GOCACHE=" + fill, "GOTOOLCHAIN=" + settings["GOVERSION"]}, b.env...)...)
		if out, err := build.CombinedOutput(); err != nil {
			return "", fmt.Errorf("filling the std store: %s go %s: %v\n%s", strings.Join(b.env, " "), strings.Join(b.args, " "), err, out)
		}
	}
	err = filepath.WalkDir(fill, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		return os.Chmod(p, 0o444)
	})
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(fill, stdStoreComplete), nil, 0o444); err != nil {
		return "", err
	}
	if err := os.Rename(fill, store); err != nil {
		return "", err
	}
	return store, nil
}

// seedCache links every entry of the store (the files of its two-digit
// subdirectories) into the empty Go cache dir, or copies it where a link
// fails, and returns how many it placed. The links share the store's
// read-only files.
func seedCache(store, dir string) (int, error) {
	placed := 0
	subdirs, err := os.ReadDir(store)
	if err != nil {
		return 0, err
	}
	for _, sub := range subdirs {
		if !sub.IsDir() || len(sub.Name()) != 2 {
			continue
		}
		files, err := os.ReadDir(filepath.Join(store, sub.Name()))
		if err != nil {
			return placed, err
		}
		if err := os.MkdirAll(filepath.Join(dir, sub.Name()), 0o755); err != nil {
			return placed, err
		}
		for _, f := range files {
			from, to := filepath.Join(store, sub.Name(), f.Name()), filepath.Join(dir, sub.Name(), f.Name())
			if err := os.Link(from, to); err != nil {
				if err := copyFile(from, to); err != nil {
					return placed, err
				}
			}
			placed++
		}
	}
	return placed, nil
}

// copyFile copies a file to a new read-only file.
func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o444)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	return errors.Join(err, out.Close())
}
