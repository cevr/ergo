package hermetic

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// source is one file of a package directory.
type source struct{ name, text string }

// checkFiles parses sources as one package directory and checks them.
func checkFiles(t *testing.T, tested bool, sources ...source) []Violation {
	t.Helper()
	fset := token.NewFileSet()
	var files []*ast.File
	for _, s := range sources {
		file, err := parser.ParseFile(fset, s.name, s.text, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse:\n%s\n%v", s.text, err)
		}
		files = append(files, file)
	}
	return Files(fset, files, tested)
}

// check checks texts, one file each, as the tested package.
func check(t *testing.T, texts ...string) []Violation {
	t.Helper()
	var sources []source
	for i, text := range texts {
		sources = append(sources, source{fmt.Sprintf("file%d_test.go", i), text})
	}
	return checkFiles(t, true, sources...)
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

// TestFilesRefuseWhatRunsAProgram checks the domain the check closes: every
// way a test runs a program other than itself, the three round-2 review
// probes included, and every way it loads a library.
func TestFilesRefuseWhatRunsAProgram(t *testing.T) {
	inTest := func(body string, imports ...string) string {
		quoted := make([]string, len(imports))
		for i, path := range imports {
			quoted[i] = fmt.Sprintf("%q", path)
		}
		return fmt.Sprintf("package gate\nimport (%s; \"testing\")\nfunc TestX(t *testing.T) {\n%s\n}\n", strings.Join(quoted, "; "), body)
	}
	cases := []struct {
		name, source, reason string
	}{
		// Round 2 B1: a program chosen at run time.
		{"review probe: absolute program", reviewProbe(`exec.Command(filepath.Join("/nonexistent/loop-probe-x", "unlisted"), "--build")`), "not a variable bound to os.Executable()"},
		// Round 2 B1: a PATH lookup made by a child process.
		{"review probe: child's PATH", reviewProbe(`exec.Command("sh", "-c", "PATH=\"$1\"; export PATH; exec unlisted --build", "sh", "/nonexistent/loop-probe-x")`), "not a variable bound to os.Executable()"},
		// Round 2 B1: a program whose shared library changed.
		{"review probe: shared library", reviewProbe(`exec.Command("seq", "--build")`), "not a variable bound to os.Executable()"},
		{"seq", inTest(`_ = exec.Command("seq", "1", "3").Run()`, "os/exec"), "exec.Command runs a program"},
		{"context", inTest(`_ = exec.CommandContext(t.Context(), "sh", "-c", "true").Run()`, "os/exec"), "exec.CommandContext runs a program"},
		{"variable", inTest(`program := "seq"; _ = exec.Command(program).Run()`, "os/exec"), "program is not bound to os.Executable()"},
		{"os.Args", inTest(`_ = exec.Command(os.Args[0]).Run()`, "os", "os/exec"), "not a variable bound to os.Executable()"},
		{"rebound", inTest(`self, _ := os.Executable(); self = "/usr/bin/seq"; _ = exec.Command(self).Run()`, "os", "os/exec"), "self is written again"},
		{"bound twice", inTest(`self, _ := os.Executable(); if true { self, _ := os.Executable(); _ = self }; _ = exec.Command(self).Run()`, "os", "os/exec"), "self is written again"},
		{"address", inTest(`self, _ := os.Executable(); p := &self; *p = "seq"; _ = exec.Command(self).Run()`, "os", "os/exec"), "self is written again"},
		{"closure", inTest(`self, _ := os.Executable(); func() { self = "seq" }(); _ = exec.Command(self).Run()`, "os", "os/exec"), "self is written again"},
		{"path field", inTest(`self, _ := os.Executable(); command := exec.Command(self); command.Path = "/usr/bin/seq"; _ = command.Run()`, "os", "os/exec"), "sets a Path field"},
		{"hand-built command", inTest(`_ = (&exec.Cmd{Path: "/usr/bin/seq"}).Run()`, "os/exec"), "builds an exec.Cmd by hand"},
		{"function value", inTest(`run := exec.Command; _ = run("seq").Run()`, "os/exec"), "without calling it"},
		{"look up", inTest(`_, _ = exec.LookPath("seq")`, "os/exec"), "os/exec.LookPath"},
		{"renamed import", "package gate\nimport (run \"os/exec\"; \"testing\")\nfunc TestX(t *testing.T) { _ = run.Command(\"seq\").Run() }\n", "os/exec.Command runs a program"},
		{"dot import", "package gate\nimport (. \"os/exec\"; \"testing\")\nfunc TestX(t *testing.T) { _ = Command(\"seq\").Run() }\n", "dot-imports os/exec"},
		{"package variable", "package gate\nimport \"os/exec\"\nvar command = exec.Command(\"seq\")\n", "outside a function"},
		{"start process", inTest(`_, _ = os.StartProcess("/bin/sh", nil, &os.ProcAttr{})`, "os"), "os.StartProcess runs a program"},
		{"syscall exec", inTest(`_ = syscall.Exec("/bin/sh", nil, nil)`, "syscall"), "syscall.Exec runs a program"},
		{"fork exec", inTest(`_, _ = syscall.ForkExec("/bin/sh", nil, nil)`, "syscall"), "syscall.ForkExec runs a program"},
		{"raw system call by number", inTest(`_, _, _ = syscall.Syscall(59, 0, 0, 0)`, "syscall"), "syscall.Syscall may run a program: its number is not a named constant"},
		{"raw execve", inTest(`_, _, _ = syscall.RawSyscall(syscall.SYS_EXECVE, 0, 0, 0)`, "syscall"), "syscall.RawSyscall may run a program: its number is SYS_EXECVE"},
		{"raw call as a value", inTest(`call := syscall.Syscall6; _, _, _ = call(1, 0, 0, 0, 0, 0, 0)`, "syscall"), "syscall.Syscall6 may run a program: it is not called"},
		{"cgo", "package gate\n// #include <stdio.h>\nimport \"C\"\n", `imports "C"`},
		{"plugin", "package gate\nimport \"plugin\"\nvar _ = plugin.Open\n", "imports plugin"},
		{"linkname", "package gate\nimport _ \"unsafe\"\n//go:linkname forkExec syscall.forkExec\nfunc forkExec()\n", "go:linkname"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			violations := check(t, c.source)
			for _, v := range violations {
				if strings.Contains(v.Reason, c.reason) {
					return
				}
			}
			t.Errorf("no violation saying %q in %v\n%s", c.reason, violations, c.source)
		})
	}
}

// TestFilesAllowOneHelperThatRunsTheTestBinary: the helper itself, and the
// uses of os/exec's types around it, pass.
func TestFilesAllowOneHelperThatRunsTheTestBinary(t *testing.T) {
	if violations := check(t, selfHelper); len(violations) != 0 {
		t.Fatalf("the helper was refused: %v", violations)
	}
	plain := "package gate\nimport (\"os\"; \"syscall\")\nfunc pid() int { _ = syscall.Getpid(); return os.Getpid() }\n"
	if violations := check(t, plain); len(violations) != 0 {
		t.Fatalf("a package that runs nothing was refused: %v", violations)
	}
	direct := "package gate\nimport (\"os\"; \"syscall\")\nfunc again() error {\n\tself, err := os.Executable()\n\tif err != nil {\n\t\treturn err\n\t}\n\treturn syscall.Exec(self, os.Args, os.Environ())\n}\n"
	if violations := check(t, direct); len(violations) != 0 {
		t.Fatalf("syscall.Exec of the test binary was refused: %v", violations)
	}
}

// TestFilesAllowOnlyOneHelper: a second function that runs the test binary,
// in the same file or another file of the package, is refused.
func TestFilesAllowOnlyOneHelper(t *testing.T) {
	second := strings.Replace(selfHelper, "func runSelf(", "func runSelfAgain(", 1)
	violations := check(t, selfHelper, second)
	if len(violations) != 1 || !strings.Contains(violations[0].Reason, "runSelfAgain is a second function that runs the test binary (the first is runSelf)") {
		t.Fatalf("violations %v, want runSelfAgain refused", violations)
	}
}

// TestDependenciesMayRunTheTestBinaryMoreThanOnce: the one-helper rule is the
// tested package's; a package it uses (testinputs, which observes the tests)
// may run the test binary from two functions, and nothing else.
func TestDependenciesMayRunTheTestBinaryMoreThanOnce(t *testing.T) {
	second := strings.Replace(selfHelper, "func runSelf(", "func runSelfAgain(", 1)
	if violations := checkFiles(t, false, source{"a.go", selfHelper}, source{"b.go", second}); len(violations) != 0 {
		t.Fatalf("a dependency's two helpers were refused: %v", violations)
	}
	seq := strings.Replace(selfHelper, "exec.CommandContext(ctx, self,", "exec.CommandContext(ctx, \"seq\",", 1)
	if violations := checkFiles(t, false, source{"a.go", seq}); len(violations) != 1 {
		t.Fatalf("a dependency that runs seq: violations %v, want one", violations)
	}
}

// TestRawSystemCallsRunNoProgram: a raw system call passes only when its
// number is a constant that is neither execve nor execveat on the
// architecture of the file that defines it.
func TestRawSystemCallsRunNoProgram(t *testing.T) {
	use := source{"observe_linux.go", "package gate\nimport \"syscall\"\nfunc listen() { _, _, _ = syscall.RawSyscall(sysSeccomp, 1, 0, 0); _, _, _ = syscall.Syscall(syscall.SYS_IOCTL, 0, 0, 0) }\n"}
	constants := func(amd64, arm64 string) []source {
		return []source{use,
			{"sys_linux_amd64.go", "package gate\nconst sysSeccomp = " + amd64 + "\n"},
			{"sys_linux_arm64.go", "package gate\nconst sysSeccomp = " + arm64 + "\n"},
		}
	}
	if violations := checkFiles(t, true, constants("317", "277")...); len(violations) != 0 {
		t.Fatalf("seccomp and ioctl were refused: %v", violations)
	}
	for _, c := range []struct{ amd64, arm64, reason string }{
		{"59", "277", "sysSeccomp is 59, which runs a program on amd64"},
		{"317", "221", "sysSeccomp is 221, which runs a program on arm64"},
		{"317", "1 << 3", "sysSeccomp is not an integer literal"},
	} {
		violations := checkFiles(t, true, constants(c.amd64, c.arm64)...)
		if len(violations) != 1 || !strings.Contains(violations[0].Reason, c.reason) {
			t.Errorf("amd64 %s, arm64 %s: violations %v, want %q", c.amd64, c.arm64, violations, c.reason)
		}
	}
	everywhere := checkFiles(t, true, use, source{"sys.go", "package gate\nconst sysSeccomp = 322\n"})
	if len(everywhere) != 1 || !strings.Contains(everywhere[0].Reason, "sysSeccomp is 322, which runs a program on amd64") {
		t.Errorf("a constant for every architecture equal to execveat on amd64: %v", everywhere)
	}
	other := checkFiles(t, true, use, source{"sys_linux_riscv64.go", "package gate\nconst sysSeccomp = 277\n"})
	if len(other) != 1 || !strings.Contains(other[0].Reason, "defined for riscv64") {
		t.Errorf("a constant for an architecture without a table: %v", other)
	}
}

// TestClosureChecksEveryPackageOfTheBinary: the module's packages by their
// source, every file whatever its build constraints; the standard library
// by its imports of os/exec; anything else is refused; cgo anywhere is
// refused.
func TestClosureChecksEveryPackageOfTheBinary(t *testing.T) {
	const module = "example.com/gate"
	moduleOf := &struct{ Path string }{module}
	clean, dirty := t.TempDir(), t.TempDir()
	write := func(dir, name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(clean, "helper_test.go", selfHelper)
	write(dirty, "a.go", "package gate\n")
	// A file for another platform still counts: the package must be
	// hermetic wherever the gate runs.
	write(dirty, "seq_darwin.go", "//go:build darwin\n\npackage gate\nimport \"os/exec\"\nfunc seq() { _ = exec.Command(\"seq\", \"3\").Run() }\n")
	// The tests of a package the binary only uses are not linked in.
	write(dirty, "seq_test.go", "package gate\nimport (\"os/exec\"; \"testing\")\nfunc TestSeq(t *testing.T) { _ = exec.Command(\"seq\", \"3\").Run() }\n")
	// The tested package's tests are.
	write(clean, "seq_test.go", "package gate\nimport (\"os/exec\"; \"testing\")\nfunc TestSeq(t *testing.T) { _ = exec.Command(\"seq\", \"3\").Run() }\n")
	packages := []Package{
		{ImportPath: module + "/clean", Dir: clean, Module: moduleOf, Imports: []string{"os", "os/exec"}},
		{ImportPath: module + "/clean [" + module + "/clean.test]", Dir: clean, Module: moduleOf},
		{ImportPath: module + "/dirty", Dir: dirty, Module: moduleOf},
		{ImportPath: "os/exec", Standard: true, Imports: []string{"os", "syscall"}},
		{ImportPath: "internal/fuzz", Standard: true, Imports: []string{"os/exec"}},
		{ImportPath: "go/build", Standard: true, Imports: []string{"os/exec"}},
		{ImportPath: "net", Standard: true, CgoFiles: []string{"cgo_unix.go"}},
		{ImportPath: "plugin", Standard: true},
		{ImportPath: "example.org/other", Dir: t.TempDir(), Module: &struct{ Path string }{"example.org/other"}},
	}
	violations, err := Closure(packages, module, module+"/clean")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Closure(packages, module, module+"/absent"); err == nil {
		t.Error("a closure without the tested package was accepted")
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
	want := []string{"clean/seq_test.go", "dirty/seq_darwin.go", "go/build", "net", "plugin", "example.org/other"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("violations in %q, want %q:\n%v", got, want, violations)
	}
}
