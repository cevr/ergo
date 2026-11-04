package hermetic

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	testModule = "example.com/gate"
	testedPath = testModule + "/tested"
)

var moduleOf = &struct{ Path string }{testModule}

// source is one file of a package directory.
type source struct{ name, text string }

// writeDir writes sources into a new directory and returns it with the names
// of the files that are not tests and of those that are.
func writeDir(t *testing.T, sources ...source) (dir string, goFiles, testGoFiles []string) {
	t.Helper()
	dir = t.TempDir()
	for _, s := range sources {
		if err := os.WriteFile(filepath.Join(dir, s.name), []byte(s.text), 0o644); err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(s.name, "_test.go") {
			testGoFiles = append(testGoFiles, s.name)
		} else {
			goFiles = append(goFiles, s.name)
		}
	}
	return dir, goFiles, testGoFiles
}

// build is a linux/amd64 configuration of packages with the stub standard
// library.
func build(packages ...Package) Build {
	return Build{GOOS: "linux", GOARCH: "amd64", Packages: packages, Std: newStubImporter("amd64")}
}

// closure checks builds with testedPath as the tested package.
func closure(t *testing.T, builds ...Build) []Violation {
	t.Helper()
	violations, err := Closure(builds, testModule, testedPath)
	if err != nil {
		t.Fatal(err)
	}
	return violations
}

// check checks texts, one test file each, as the tested package, with
// dependencies as further module packages of the binary.
func check(t *testing.T, dependencies []Package, texts ...string) []Violation {
	t.Helper()
	var sources []source
	for i, text := range texts {
		sources = append(sources, source{fmt.Sprintf("file%d_test.go", i), text})
	}
	dir, goFiles, testGoFiles := writeDir(t, sources...)
	tested := Package{ImportPath: testedPath, Dir: dir, Module: moduleOf, GoFiles: goFiles, TestGoFiles: testGoFiles}
	return closure(t, build(append([]Package{tested}, dependencies...)...))
}

// dependency is a module package the tested package's binary holds.
func dependency(t *testing.T, path string, sources ...source) Package {
	t.Helper()
	dir, goFiles, _ := writeDir(t, sources...)
	return Package{ImportPath: testModule + "/" + path, Dir: dir, Module: moduleOf, GoFiles: goFiles}
}

// reviewProbe is the test package of the round-2 review's cache probes
// (round-2-receipts/review-probes_test.go): a test that runs a program
// through expression and passes only on the result it was cached with.
func reviewProbe(expression string) string {
	return fmt.Sprintf("package probe\nimport (\"os\"; \"os/exec\"; \"path/filepath\"; \"strings\"; \"testing\")\n// unique 1\nfunc TestInput(t *testing.T) {\n _ = os.Getenv(\"ERGO_TOOL_VERSIONS\"); _ = filepath.Separator\n output, err := %s.Output()\n if got := strings.TrimSpace(string(output)); err != nil || got != \"probe-build-marker-a\" { t.Fatalf(\"tool result %%q (%%v)\", got, err) }\n}\n", expression)
}

// selfHelper is the one helper a hermetic package may have: it runs the test
// binary itself in a mode.
const selfHelper = `package gate

import (
	"context"
	"errors"
	"os"
	"os/exec"
)

func runSelf(ctx context.Context, mode string) ([]byte, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, self, "-test.run=^TestHelper$")
	command.Env = append(os.Environ(), "GATE_TEST_MODE="+mode)
	output, err := command.Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return output, nil
	}
	return output, err
}
`

// inTest is a test file importing imports and "testing", with body in a test.
func inTest(body string, imports ...string) string {
	quoted := make([]string, len(imports))
	for i, path := range imports {
		quoted[i] = fmt.Sprintf("%q", path)
	}
	return fmt.Sprintf("package gate\nimport (%s; \"testing\")\nfunc TestX(t *testing.T) {\n%s\n}\n", strings.Join(quoted, "; "), body)
}

// fakeOS is a module package that imitates os: imported under the name os,
// its Executable is not the os package's.
func fakeOS(t *testing.T) Package {
	return dependency(t, "fake", source{"fake.go", "package fake\nfunc Executable() (string, error) { return \"/nonexistent/loop-probe-x\", nil }\n"})
}

// TestFilesRefuseWhatRunsAProgram checks the domain the check closes: every
// way a test runs a program other than itself, the review probes of rounds 2
// and 4 included, and every way it loads a library.
func TestFilesRefuseWhatRunsAProgram(t *testing.T) {
	cases := []struct {
		name, source, reason string
		dependencies         func(*testing.T) []Package
	}{
		// Round 2 B1: a program chosen at run time.
		{name: "review probe: absolute program", source: reviewProbe(`exec.Command(filepath.Join("/nonexistent/loop-probe-x", "unlisted"), "--build")`), reason: "not a variable bound to os.Executable()"},
		// Round 2 B1: a PATH lookup made by a child process.
		{name: "review probe: child's PATH", source: reviewProbe(`exec.Command("sh", "-c", "PATH=\"$1\"; export PATH; exec unlisted --build", "sh", "/nonexistent/loop-probe-x")`), reason: "not a variable bound to os.Executable()"},
		// Round 2 B1: a program whose shared library changed.
		{name: "review probe: shared library", source: reviewProbe(`exec.Command("seq", "--build")`), reason: "not a variable bound to os.Executable()"},
		// Round 4 B1: a local value named os whose Executable names another
		// program (round-4-receipts/verify_shadow_test.go).
		{name: "shadow: local value named os", source: inTest(`_ = os.ErrNotExist
os := struct{ Executable func() (string, error) }{Executable: func() (string, error) { return "/nonexistent/loop-probe-x/external-tool", nil }}
self, err := os.Executable()
if err != nil { t.Fatal(err) }
_ = exec.Command(self).Run()`, "os", "os/exec"), reason: "self is not bound to os.Executable()"},
		{name: "shadow: local func named like os.Executable under a dot import", source: inTest(`_ = Getpid()
Executable := func() (string, error) { return "/nonexistent/loop-probe-x", nil }
self, _ := Executable()
_ = exec.Command(self).Run()`, ".:os", "os/exec"), reason: "self is not bound to os.Executable()"},
		{name: "shadow: parameter named os", source: "package gate\nimport (\"os\"; \"os/exec\")\nvar _ = os.ErrNotExist\nfunc run(os struct{ Executable func() (string, error) }) {\n\tself, _ := os.Executable()\n\t_ = exec.Command(self).Run()\n}\n", reason: "self is not bound to os.Executable()"},
		{name: "shadow: another package imported as os", source: inTest(`self, _ := os.Executable(); _ = exec.Command(self).Run()`, "os=example.com/gate/fake", "os/exec"), reason: "self is not bound to os.Executable()", dependencies: func(t *testing.T) []Package { return []Package{fakeOS(t)} }},
		{name: "shadow: package-level value named os", source: "package gate\nimport \"os/exec\"\nvar os = struct{ Executable func() (string, error) }{Executable: func() (string, error) { return \"/nonexistent/loop-probe-x\", nil }}\nfunc run() {\n\tself, _ := os.Executable()\n\t_ = exec.Command(self).Run()\n}\n", reason: "self is not bound to os.Executable()"},
		{name: "shadow: function value of os.Executable", source: inTest(`executable := os.Executable; self, _ := executable(); _ = exec.Command(self).Run()`, "os", "os/exec"), reason: "self is not bound to os.Executable()"},
		{name: "shadow: method named Executable", source: "package gate\nimport \"os/exec\"\ntype system struct{}\nfunc (system) Executable() (string, error) { return \"/nonexistent/loop-probe-x\", nil }\nfunc run(os system) {\n\tself, _ := os.Executable()\n\t_ = exec.Command(self).Run()\n}\n", reason: "self is not bound to os.Executable()"},
		{name: "package-level self", source: "package gate\nimport (\"os\"; \"os/exec\")\nvar self, _ = os.Executable()\nfunc run() { _ = exec.Command(self).Run() }\n", reason: "self is a package variable"},
		{name: "self assigned, not defined", source: inTest(`var self string; self, _ = os.Executable(); _ = exec.Command(self).Run()`, "os", "os/exec"), reason: "self is not bound to os.Executable()"},
		{name: "seq", source: inTest(`_ = exec.Command("seq", "1", "3").Run()`, "os/exec"), reason: "exec.Command runs a program"},
		{name: "context", source: inTest(`_ = exec.CommandContext(t.Context(), "sh", "-c", "true").Run()`, "os/exec"), reason: "exec.CommandContext runs a program"},
		{name: "variable", source: inTest(`program := "seq"; _ = exec.Command(program).Run()`, "os/exec"), reason: "program is not bound to os.Executable()"},
		{name: "os.Args", source: inTest(`_ = exec.Command(os.Args[0]).Run()`, "os", "os/exec"), reason: "not a variable bound to os.Executable()"},
		{name: "rebound", source: inTest(`self, _ := os.Executable(); self = "/usr/bin/seq"; _ = exec.Command(self).Run()`, "os", "os/exec"), reason: "self is written again"},
		{name: "rebound in parentheses", source: inTest(`self, _ := os.Executable(); (self) = "/usr/bin/seq"; _ = exec.Command(self).Run()`, "os", "os/exec"), reason: "self is written again"},
		{name: "address", source: inTest(`self, _ := os.Executable(); p := &self; *p = "seq"; _ = exec.Command(self).Run()`, "os", "os/exec"), reason: "self is written again"},
		{name: "closure", source: inTest(`self, _ := os.Executable(); func() { self = "seq" }(); _ = exec.Command(self).Run()`, "os", "os/exec"), reason: "self is written again"},
		{name: "path field", source: inTest(`self, _ := os.Executable(); command := exec.Command(self); command.Path = "/usr/bin/seq"; _ = command.Run()`, "os", "os/exec"), reason: "sets a Path field"},
		{name: "path field of an embedding struct", source: inTest(`self, _ := os.Executable(); wrapped := struct{ *exec.Cmd }{exec.Command(self)}; wrapped.Path = "/usr/bin/seq"; _ = wrapped.Run()`, "os", "os/exec"), reason: "sets a Path field"},
		{name: "hand-built command", source: inTest(`_ = (&exec.Cmd{Path: "/usr/bin/seq"}).Run()`, "os/exec"), reason: "builds an exec.Cmd by hand"},
		{name: "function value", source: inTest(`run := exec.Command; _ = run("seq").Run()`, "os/exec"), reason: "without calling it"},
		{name: "look up", source: inTest(`_, _ = exec.LookPath("seq")`, "os/exec"), reason: "os/exec.LookPath"},
		{name: "renamed import", source: "package gate\nimport (run \"os/exec\"; \"testing\")\nfunc TestX(t *testing.T) { _ = run.Command(\"seq\").Run() }\n", reason: "os/exec.Command runs a program"},
		{name: "dot import", source: "package gate\nimport (. \"os/exec\"; \"testing\")\nfunc TestX(t *testing.T) { _ = Command(\"seq\").Run() }\n", reason: "os/exec.Command runs a program"},
		{name: "package variable", source: "package gate\nimport \"os/exec\"\nvar command = exec.Command(\"seq\")\n", reason: "outside a function"},
		{name: "start process", source: inTest(`_, _ = os.StartProcess("/bin/sh", nil, &os.ProcAttr{})`, "os"), reason: "os.StartProcess runs a program"},
		{name: "syscall exec", source: inTest(`_ = syscall.Exec("/bin/sh", nil, nil)`, "syscall"), reason: "syscall.Exec runs a program"},
		{name: "fork exec", source: inTest(`_, _ = syscall.ForkExec("/bin/sh", nil, nil)`, "syscall"), reason: "syscall.ForkExec runs a program"},
		{name: "raw system call by number", source: inTest(`_, _, _ = syscall.Syscall(59, 0, 0, 0)`, "syscall"), reason: "syscall.Syscall may run a program: its number is 59, which runs a program in linux/amd64"},
		{name: "raw execve", source: inTest(`_, _, _ = syscall.RawSyscall(syscall.SYS_EXECVE, 0, 0, 0)`, "syscall"), reason: "syscall.RawSyscall may run a program: its number is 59"},
		{name: "raw call with a variable number", source: inTest(`number := uintptr(1); _, _, _ = syscall.RawSyscall(number, 0, 0, 0)`, "syscall"), reason: "its number is not a constant"},
		{name: "raw call as a value", source: inTest(`call := syscall.Syscall6; _, _, _ = call(1, 0, 0, 0, 0, 0, 0)`, "syscall"), reason: "syscall.Syscall6 may run a program: it is not called"},
		{name: "cgo", source: "package gate\n// #include <stdio.h>\nimport \"C\"\n", reason: `imports "C"`},
		{name: "plugin", source: "package gate\nimport \"plugin\"\nvar _ = plugin.Open\n", reason: "imports plugin"},
		{name: "linkname", source: "package gate\nimport _ \"unsafe\"\n//go:linkname forkExec syscall.forkExec\nfunc forkExec()\n", reason: "go:linkname"},
		{name: "does not type-check", source: inTest(`_ = exec.Command(undefined).Run()`, "os/exec"), reason: "does not type-check in linux/amd64"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var dependencies []Package
			if c.dependencies != nil {
				dependencies = c.dependencies(t)
			}
			violations := check(t, dependencies, importNames(c.source))
			for _, v := range violations {
				if strings.Contains(v.Reason, c.reason) {
					return
				}
			}
			t.Errorf("no violation saying %q in %v\n%s", c.reason, violations, c.source)
		})
	}
}

// importNames rewrites inTest's "name=path" and ".:path" imports into named
// and dot imports.
func importNames(source string) string {
	for _, prefix := range []string{`".:`, `"os=`} {
		for {
			i := strings.Index(source, prefix)
			if i < 0 {
				break
			}
			name := strings.TrimSuffix(strings.TrimPrefix(prefix, `"`), ":")
			name = strings.TrimSuffix(name, "=")
			source = source[:i] + name + ` "` + source[i+len(prefix):]
		}
	}
	return source
}

// TestFilesAllowOneHelperThatRunsTheTestBinary: the helper itself, the uses
// of os/exec's types around it, and the real os.Executable reached through a
// renamed or a dot import, pass.
func TestFilesAllowOneHelperThatRunsTheTestBinary(t *testing.T) {
	admitted := map[string]string{
		"helper":          selfHelper,
		"runs nothing":    "package gate\nimport (\"os\"; \"syscall\")\nfunc pid() int { _ = syscall.Getpid(); return os.Getpid() }\n",
		"syscall.Exec":    "package gate\nimport (\"os\"; \"syscall\")\nfunc again() error {\n\tself, err := os.Executable()\n\tif err != nil {\n\t\treturn err\n\t}\n\treturn syscall.Exec(self, os.Args, os.Environ())\n}\n",
		"renamed os":      "package gate\nimport (system \"os\"; \"os/exec\")\nfunc run() { self, _ := system.Executable(); _ = exec.Command(self).Run() }\n",
		"dot-imported os": "package gate\nimport (. \"os\"; \"os/exec\")\nfunc run() { self, _ := Executable(); _ = exec.Command(self).Run() }\n",
		"var binding":     "package gate\nimport (\"os\"; \"os/exec\")\nfunc run() { var self, _ = os.Executable(); _ = exec.Command(self).Run() }\n",
		// An inner self is another variable: the outer one stays bound.
		"shadowed inner self": inTest(`self, _ := os.Executable(); if true { self, _ := os.Executable(); _ = self }; _ = exec.Command(self).Run()`, "os", "os/exec"),
		"seccomp and ioctl":   "package gate\nimport \"syscall\"\nconst sysSeccomp = 317\nfunc listen() { _, _, _ = syscall.RawSyscall(sysSeccomp, 1, 0, 0); _, _, _ = syscall.Syscall(syscall.SYS_IOCTL, 0, 0, 0) }\n",
	}
	for name, text := range admitted {
		t.Run(name, func(t *testing.T) {
			if violations := check(t, nil, text); len(violations) != 0 {
				t.Fatalf("refused: %v\n%s", violations, text)
			}
		})
	}
}

// TestFilesAllowOnlyOneHelper: a second function that runs the test binary,
// in the same file or another file of the package, is refused.
func TestFilesAllowOnlyOneHelper(t *testing.T) {
	second := strings.Replace(selfHelper, "func runSelf(", "func runSelfAgain(", 1)
	violations := check(t, nil, selfHelper, second)
	if len(violations) != 1 || !strings.Contains(violations[0].Reason, "runSelfAgain is a second function that runs the test binary (the first is runSelf)") {
		t.Fatalf("violations %v, want runSelfAgain refused", violations)
	}
}

// TestDependenciesMayRunTheTestBinaryMoreThanOnce: the one-helper rule is the
// tested package's; a package it uses (testinputs, which observes the tests)
// may run the test binary from two functions, and nothing else.
func TestDependenciesMayRunTheTestBinaryMoreThanOnce(t *testing.T) {
	second := strings.Replace(selfHelper, "func runSelf(", "func runSelfAgain(", 1)
	tested := "package gate\n"
	if violations := check(t, []Package{dependency(t, "observe", source{"a.go", selfHelper}, source{"b.go", second})}, tested); len(violations) != 0 {
		t.Fatalf("a dependency's two helpers were refused: %v", violations)
	}
	seq := strings.Replace(selfHelper, "exec.CommandContext(ctx, self,", "exec.CommandContext(ctx, \"seq\", self,", 1)
	if violations := check(t, []Package{dependency(t, "observe", source{"a.go", seq})}, tested); len(violations) != 1 {
		t.Fatalf("a dependency that runs seq: violations %v, want one", violations)
	}
}

// TestRawSystemCallsRunNoProgram: a raw system call passes only when its
// number is a constant that runs no program in each configuration whose
// files use it.
func TestRawSystemCallsRunNoProgram(t *testing.T) {
	use := source{"observe_linux.go", "package gate\nimport \"syscall\"\nfunc listen() { _, _, _ = syscall.RawSyscall(sysSeccomp, 1, 0, 0) }\n"}
	run := func(amd64, arm64 string) []Violation {
		t.Helper()
		dir, _, _ := writeDir(t, use,
			source{"sys_linux_amd64.go", "package gate\nconst sysSeccomp = " + amd64 + "\n"},
			source{"sys_linux_arm64.go", "package gate\nconst sysSeccomp = " + arm64 + "\n"})
		in := func(arch string) Build {
			tested := Package{ImportPath: testedPath, Dir: dir, Module: moduleOf, GoFiles: []string{use.name, "sys_linux_" + arch + ".go"}}
			return Build{GOOS: "linux", GOARCH: arch, Packages: []Package{tested}, Std: newStubImporter(arch)}
		}
		return closure(t, in("amd64"), in("arm64"))
	}
	if violations := run("317", "277"); len(violations) != 0 {
		t.Fatalf("seccomp was refused: %v", violations)
	}
	for _, c := range []struct{ amd64, arm64, reason string }{
		{"59", "277", "its number is 59, which runs a program in linux/amd64"},
		{"317", "221", "its number is 221, which runs a program in linux/arm64"},
		{"317", "281 + 0", "its number is 281, which runs a program in linux/arm64"},
	} {
		violations := run(c.amd64, c.arm64)
		if len(violations) != 1 || !strings.Contains(violations[0].Reason, c.reason) {
			t.Errorf("amd64 %s, arm64 %s: violations %v, want %q", c.amd64, c.arm64, violations, c.reason)
		}
	}
}

// TestClosureChecksEveryPackageOfTheBinary: the module's packages by their
// source, every file in some configuration; the standard library by its
// imports of os/exec; anything else is refused; cgo anywhere is refused.
func TestClosureChecksEveryPackageOfTheBinary(t *testing.T) {
	seqTest := "package gate\nimport (\"os/exec\"; \"testing\")\nfunc TestSeq(t *testing.T) { _ = exec.Command(\"seq\", \"3\").Run() }\n"
	// The tested package's tests are linked in.
	clean, _, cleanTests := writeDir(t, source{"helper_test.go", selfHelper}, source{"seq_test.go", seqTest})
	dirty, _, _ := writeDir(t,
		source{"a.go", "package gate\n"},
		// A file for another configuration counts when that configuration
		// is checked, and a file no checked configuration builds is refused.
		source{"seq_darwin.go", "//go:build darwin\n\npackage gate\nimport \"os/exec\"\nfunc seq() { _ = exec.Command(\"seq\", \"3\").Run() }\n"},
		source{"other_windows.go", "package gate\n"},
		// The tests of a package the binary only uses are not linked in.
		source{"seq_test.go", seqTest})
	packages := func(dirtyFiles ...string) []Package {
		return []Package{
			{ImportPath: testedPath, Dir: clean, Module: moduleOf, Imports: []string{"os", "os/exec"}, TestGoFiles: cleanTests},
			{ImportPath: testedPath + " [" + testedPath + ".test]", Dir: clean, Module: moduleOf, TestGoFiles: cleanTests},
			{ImportPath: testedPath + ".test", Dir: clean, Module: moduleOf},
			{ImportPath: testModule + "/dirty", Dir: dirty, Module: moduleOf, GoFiles: dirtyFiles},
			{ImportPath: "os/exec", Standard: true, Imports: []string{"os", "syscall"}},
			{ImportPath: "internal/fuzz", Standard: true, Imports: []string{"os/exec"}},
			{ImportPath: "go/build", Standard: true, Imports: []string{"os/exec"}},
			{ImportPath: "net", Standard: true, CgoFiles: []string{"cgo_unix.go"}},
			{ImportPath: "plugin", Standard: true},
			{ImportPath: "example.org/other", Dir: t.TempDir(), Module: &struct{ Path string }{"example.org/other"}},
		}
	}
	linux := build(packages("a.go")...)
	darwin := Build{GOOS: "darwin", GOARCH: "arm64", Packages: packages("a.go", "seq_darwin.go"), Std: newStubImporter("arm64")}
	violations := closure(t, linux, darwin)
	if _, err := Closure([]Build{linux}, testModule, testModule+"/absent"); err == nil {
		t.Error("a closure without the tested package was accepted")
	}
	if _, err := Closure(nil, testModule, testedPath); err == nil {
		t.Error("a closure without a configuration was accepted")
	}
	var got []string
	for _, v := range violations {
		where := v.Where
		for name, dir := range map[string]string{"clean": clean, "dirty": dirty} {
			if strings.HasPrefix(where, dir) {
				where = name + "/" + filepath.Base(strings.Split(where, ":")[0])
			}
		}
		got = append(got, where)
	}
	slices.Sort(got)
	want := []string{"clean/seq_test.go", "dirty/other_windows.go", "dirty/seq_darwin.go", "example.org/other", "go/build", "net", "plugin"}
	if !slices.Equal(got, want) {
		t.Fatalf("violations in %q, want %q:\n%v", got, want, violations)
	}
}
