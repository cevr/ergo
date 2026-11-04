package build

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
)

// helperVariable selects a helper mode of the test binary; rootVariable
// names the cache root a helper publishes into; stagedVariable names the
// compiler the replace helper renames over its own path.
const (
	helperVariable = "ERGO_BUILD_TEST_HELPER"
	rootVariable   = "ERGO_BUILD_TEST_ROOT"
	stagedVariable = "ERGO_BUILD_TEST_STAGED"
)

// identityMarker is a string the test binary carries. The linker sets it
// with -X to build a variant that differs only there, and on Linux a copy
// can be patched in exactly one byte of it.
var identityMarker = "ergo-identity-marker-0"

// helperMain publishes baseInput into the cache at $ERGO_BUILD_TEST_ROOT
// and prints the module directory. In mode "crash" the process exits with
// status 3 once staging is complete and before the rename, as a builder
// killed mid-publication would: no deferred cleanup runs. In mode
// "identity" it prints the marker and CompilerIdentity. In mode "replace"
// it renames the compiler at $ERGO_BUILD_TEST_STAGED over its own
// executable's path, as a reinstall does, then builds baseInput keyed by
// CompilerIdentity and prints the identity, the module directory and
// whether the module is private, as JSON; mode "replace-early" acquires
// the identity before the replacement.
func helperMain(mode string) int {
	fail := func(err error) int {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if mode == "sweep-stall" {
		return sweepStallHelper()
	}
	if mode == "lease" {
		return leaseHelper()
	}
	if mode == "hold" {
		return holdHelper()
	}
	if mode == "trim-crash" {
		return trimCrashHelper()
	}
	if mode == "identity" {
		identity, err := CompilerIdentity()
		if err != nil {
			return fail(err)
		}
		fmt.Println(identityMarker + " " + identity)
		return 0
	}
	if mode == "replace" || mode == "replace-early" {
		path, err := os.Executable()
		if err != nil {
			return fail(err)
		}
		if mode == "replace-early" {
			if _, err := CompilerIdentity(); err != nil {
				return fail(err)
			}
		}
		if err := os.Rename(os.Getenv(stagedVariable), path); err != nil {
			return fail(err)
		}
		identity, err := CompilerIdentity()
		if err != nil {
			return fail(err)
		}
		in := baseInput()
		in.Compiler = identity
		module, err := Cache{Root: os.Getenv(rootVariable)}.Module(in, emitFor(in, nil))
		if err != nil {
			return fail(err)
		}
		out, err := json.Marshal(replaced{Identity: identity, Dir: module.Dir, Private: module.Private})
		if err != nil {
			return fail(err)
		}
		fmt.Println(string(out))
		return 0
	}
	if mode == "crash" {
		testHookStaged = func(string) error { os.Exit(3); return nil }
	}
	in := baseInput()
	module, err := Cache{Root: os.Getenv(rootVariable)}.Module(in, emitFor(in, nil))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(module.Dir)
	return 0
}

const testRuntime = "package rt\n\nconst Name = \"r1\"\n"

// baseInput is the input every mutation starts from.
func baseInput() Input {
	return Input{
		Sources: []Source{
			{Path: "/src/app/a.ergo", Text: []byte("func main() {}\n")},
			{Path: "/src/app/b.ergo", Text: []byte("func helper() {}\n")},
		},
		Dependencies: []Dependency{
			{Name: "prelude", Interface: "iface-prelude-1", Implementation: "impl-prelude-1"},
			{Name: "timeout", Interface: "iface-timeout-1", Implementation: "impl-timeout-1"},
		},
		Compiler: "sha256:compiler-1",
		Emitter:  "test-emit/1",
		Runtime:  []File{{Path: "rt/rt.go", Data: []byte(testRuntime)}, {Path: "rt/extra.go", Data: []byte("package rt\n\nconst Extra = 1\n")}},
		Language: "ergo-v1-test",
		Mode:     ModeBuild,
	}
}

// emitFor is an emitter that reads only in, as Cache.Module requires: its
// program prints the canonical summary of in, the runtime's Name and a
// link-time flag, so a module and its executable show every input. calls,
// when not nil, counts its runs.
func emitFor(in Input, calls *atomic.Int32) Emitter {
	return func() ([]File, error) {
		if calls != nil {
			calls.Add(1)
		}
		summary, err := in.Summarize()
		if err != nil {
			return nil, err
		}
		description, err := json.Marshal(summary)
		if err != nil {
			return nil, err
		}
		main := "package main\n\nimport (\n\t\"os\"\n\n\t\"ergotest/rt\"\n)\n\n" +
			"var flag = \"unset\"\n\n" +
			"const description = " + strconv.Quote(string(description)) + "\n\n" +
			"func main() { os.Stdout.WriteString(description + \"|runtime=\" + rt.Name + \"|flag=\" + flag + \"\\n\") }\n"
		return []File{{Path: "go.mod", Data: []byte("module ergotest\n\ngo 1.23\n")}, {Path: "main.go", Data: []byte(main)}}, nil
	}
}

// expectedOutput is what the executable of in prints, computed from in.
func expectedOutput(t *testing.T, in Input, runtimeName, flag string) string {
	t.Helper()
	summary, err := in.Summarize()
	if err != nil {
		t.Fatal(err)
	}
	description, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	return string(description) + "|runtime=" + runtimeName + "|flag=" + flag + "\n"
}

// hostToolchain is the go command on PATH with Go's default target and no
// flags, whatever the test environment sets.
func hostToolchain() Toolchain { return Toolchain{Go: "go"} }

// link runs the toolchain's action for module and returns the executable.
func link(t *testing.T, tc Toolchain, module Module) (string, error) {
	t.Helper()
	output := filepath.Join(t.TempDir(), "program")
	child := tc.Command(module, output)
	out, err := child.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("%w: %s", err, out)
	}
	return output, nil
}

// runProgram runs an executable and returns its stdout.
func runProgram(t *testing.T, binary string) string {
	t.Helper()
	out, err := exec.Command(binary).Output()
	if err != nil {
		t.Fatalf("run %s: %v", binary, err)
	}
	return string(out)
}

// fileState is a file's digest, metadata and identity.
type fileState struct {
	digest [32]byte
	info   fs.FileInfo
}

// snapshot records every file under dir, the manifest included.
func snapshot(t *testing.T, dir string) map[string]fileState {
	t.Helper()
	states := map[string]fileState{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		states[p] = fileState{digest: sha256.Sum256(data), info: info}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return states
}

// unchanged reports whether two snapshots hold the same files with the same
// bytes, inode and modification time.
func unchanged(a, b map[string]fileState) bool {
	if len(a) != len(b) {
		return false
	}
	for p, x := range a {
		y, ok := b[p]
		if !ok || x.digest != y.digest || !os.SameFile(x.info, y.info) || !x.info.ModTime().Equal(y.info.ModTime()) {
			return false
		}
	}
	return true
}

// entries lists the names under root/sub, or nil when it is absent.
func entries(t *testing.T, root, sub string) []string {
	t.Helper()
	list, err := os.ReadDir(filepath.Join(root, sub))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range list {
		names = append(names, e.Name())
	}
	return names
}

// mustVerify checks that the published module of in verifies and holds
// exactly the files its emitter and runtime produce.
func mustVerify(t *testing.T, root string, in Input) Module {
	t.Helper()
	summary, err := in.Summarize()
	if err != nil {
		t.Fatal(err)
	}
	key := summary.Key()
	dir := filepath.Join(root, "modules", key)
	if _, err := verify(dir, key, summary); err != nil {
		t.Fatalf("published module does not verify: %v", err)
	}
	emitted, err := emitFor(in, nil)()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range append(append([]File{}, in.Runtime...), emitted...) {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(f.Path)))
		if err != nil || !bytes.Equal(data, f.Data) {
			t.Fatalf("published %s differs from its emission: %v", f.Path, err)
		}
	}
	return Module{Dir: dir, Key: key}
}
