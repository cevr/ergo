package toolchain

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// inertPackages are the packages TestPatchedCompileIsInertWithoutErgoIR
// compiles with both compiles: standard packages that hold generics, the
// type checker, reflection-heavy code and formatting, and a package of
// this repository that is plain Go, not lowered Ergo. None holds assembly
// or embedded files, so a compile of its Go files is the whole package.
var inertPackages = []string{"fmt", "slices", "encoding/json", "go/types", "ergo.local/prototype/internal/proclimit"}

// TestPatchedCompileIsInertWithoutErgoIR: the patched compile, given no
// IR flag, writes the object the release's stock compile writes, byte for
// byte, for standard packages and a non-Ergo Go package, and prints the
// same -V=full line. That is what lets Go's build cache share every
// package without -ergoir between stock and patched builds (the patched
// compile keeps the stock -V=full line; docs/tooling.md, The patched
// compile). The release is the go command on PATH's; the patch is the one
// toolchain/ir/lock ports to it, built here into a new cache.
//
// It builds Go because the question is what two compiler binaries write;
// no in-process check reaches their output. Mutant: a patch hunk is
// active without -ergoir (irt-patch-active-without-ergoir, a mutant of
// the patch, run with ERGO_TOOLCHAIN_TEST_IR naming a lock directory that
// holds the mutated patch and its digest).
func TestPatchedCompileIsInertWithoutErgoIR(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier: builds the patched compile")
	}
	out, err := exec.Command("go", "env", "GOROOT").Output()
	if err != nil {
		t.Fatalf("go env GOROOT: %v", err)
	}
	source := strings.TrimSpace(string(out))
	c := PatchConfig{Cache: t.TempDir(), Source: source}
	if dir := os.Getenv("ERGO_TOOLCHAIN_TEST_IR"); dir != "" {
		c.lock = os.DirFS(dir)
	}
	patched, err := Patch(c)
	if err != nil {
		t.Fatal(err)
	}
	tools := filepath.Join("pkg", "tool", hostTools())
	stock := filepath.Join(source, tools, "compile"+exe())
	ours := filepath.Join(patched.Root, tools, "compile"+exe())
	runCompile := func(t *testing.T, compile, dir string, args ...string) string {
		t.Helper()
		return runTool(t, source, compile, dir, args...)
	}
	if a, b := runCompile(t, stock, "", "-V=full"), runCompile(t, ours, "", "-V=full"); a != b {
		t.Errorf("-V=full: stock %q, patched %q", a, b)
	}
	pkgs := listExport(t, source)
	work := t.TempDir()
	for _, path := range inertPackages {
		p, ok := pkgs[path]
		if !ok {
			t.Fatalf("go list has no %s", path)
		}
		if len(p.SFiles) > 0 || len(p.EmbedFiles) > 0 || len(p.CgoFiles) > 0 {
			t.Fatalf("%s holds assembly, embedded or cgo files: not a whole package by its Go files", path)
		}
		var cfg strings.Builder
		for _, imp := range p.Imports {
			resolved := imp
			if m, ok := p.ImportMap[imp]; ok {
				resolved = m
			}
			if resolved == "unsafe" || resolved == "C" {
				continue
			}
			dep, ok := pkgs[resolved]
			if !ok || dep.Export == "" {
				t.Fatalf("%s imports %s, which has no export data", path, resolved)
			}
			cfg.WriteString("packagefile " + imp + "=" + dep.Export + "\n")
		}
		dir := filepath.Join(work, strings.ReplaceAll(path, "/", "_"))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		importcfg := filepath.Join(dir, "importcfg")
		if err := os.WriteFile(importcfg, []byte(cfg.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		args := []string{"-p", path, "-importcfg", importcfg, "-trimpath", p.Dir + "=>" + path, "-pack", "-c=1", "-nolocalimports"}
		if p.Standard {
			args = append(args, "-std")
		}
		if p.Module != nil && p.Module.GoVersion != "" {
			args = append(args, "-lang=go"+p.Module.GoVersion)
		}
		var files []string
		for _, f := range p.GoFiles {
			files = append(files, filepath.Join(p.Dir, f))
		}
		objects := map[string][]byte{}
		for name, compile := range map[string]string{"stock": stock, "patched": ours} {
			obj := filepath.Join(dir, name+".a")
			runCompile(t, compile, p.Dir, append(append(slices.Clone(args), "-o", obj), files...)...)
			data, err := os.ReadFile(obj)
			if err != nil {
				t.Fatal(err)
			}
			objects[name] = data
		}
		if !bytes.Equal(objects["stock"], objects["patched"]) {
			t.Errorf("%s: the patched compile without -ergoir wrote another object than the stock one (%d and %d bytes)", path, len(objects["stock"]), len(objects["patched"]))
		}
	}
}

// listedPackage is what TestPatchedCompileIsInertWithoutErgoIR reads of
// go list -json.
type listedPackage struct {
	ImportPath, Dir, Export   string
	Standard                  bool
	GoFiles, SFiles, CgoFiles []string
	EmbedFiles, Imports       []string
	ImportMap                 map[string]string
	Module                    *struct{ GoVersion string }
}

// listExport lists inertPackages and their dependencies with export data,
// by the release at source's go command, which compiles them with the
// stock compile.
func listExport(t *testing.T, source string) map[string]listedPackage {
	t.Helper()
	cmd := exec.Command(filepath.Join(source, "bin", "go"+exe()), append([]string{"list", "-export", "-deps", "-json"}, inertPackages...)...)
	cmd.Env = append(os.Environ(), "GOROOT="+source, "GOTOOLCHAIN=local", "GOFLAGS=", "CGO_ENABLED=0", "GOWORK=off")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -export: %v\n%s", err, stderr.String())
	}
	pkgs := map[string]listedPackage{}
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var p listedPackage
		if err := dec.Decode(&p); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		pkgs[p.ImportPath] = p
	}
	return pkgs
}

// runTool runs a compile in dir with GOROOT goroot, both compiles alike,
// and returns its stdout; a failure fails the test with its output.
func runTool(t *testing.T, goroot, compile, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(compile, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOROOT="+goroot)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s %s: %v\n%s", compile, strings.Join(args, " "), err, stderr.String())
	}
	return stdout.String()
}
