package residue_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestExecEnvIsAValue is the guard of STD-EXEC-ENV: std exec gives a
// program exactly the environment its Command names (its Env over the
// Runner's Env()), so nothing in the runtime's Go copies this process's
// environment into a child, or looks a program up on this process's PATH.
// The one read of the whole environment is runtime/v1's OSEnviron, the body
// of os.Process.Environ, which a program calls only through the os.Process
// capability.
//
// The rule is syntactic, over the Go files of runtime/v1 and its packages
// that are not tests:
//
//   - os.Environ and syscall.Environ are called only in the body of
//     OSEnviron;
//   - runtime/v1/exec, which starts the programs, calls none of
//     os.Getenv, os.LookupEnv, exec.Command, exec.CommandContext,
//     exec.LookPath and a Cmd's Environ: each reads this process's
//     environment or PATH for the child. An exec.Command of an absolute
//     path literal is the sandbox helper, this binary re-exec'd
//     (exec_sandbox_linux.go): it looks nothing up, and the programs it
//     forks get only their Command's environment.
//
// Mutant it kills (testdata/mutants.json): residue-exec-env-inherits.
func TestExecEnvIsAValue(t *testing.T) {
	root := filepath.Join("..", "..", "runtime", "v1")
	files := 0
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == "testdata" {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files++
		rel, _ := filepath.Rel(root, path)
		for _, at := range envReads(t, filepath.ToSlash(rel), src) {
			t.Errorf("%s: reads this process's environment for a child; the environment is a value (STD-EXEC-ENV): pass it through Command.Env", at)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files < 20 {
		t.Fatalf("read %d Go files of runtime/v1; the walk is broken", files)
	}
}

// envReads returns the position of each call in src, the file rel of
// runtime/v1 ("exec/exec.go"), that breaks TestExecEnvIsAValue's rule.
func envReads(t *testing.T, rel string, src []byte) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, rel, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", rel, err)
	}
	inExec := strings.HasPrefix(rel, "exec/")
	var out []string
	for _, decl := range file.Decls {
		host := false
		if fn, ok := decl.(*ast.FuncDecl); ok {
			host = fn.Recv == nil && fn.Name.Name == "OSEnviron" && !strings.Contains(rel, "/")
		}
		ast.Inspect(decl, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg := ""
			if id, ok := sel.X.(*ast.Ident); ok {
				pkg = id.Name
			}
			name := sel.Sel.Name
			switch {
			case name == "Environ" && (pkg == "os" || pkg == "syscall"):
				if !host {
					out = append(out, fset.Position(call.Pos()).String())
				}
			case !inExec:
			case pkg == "exec" && name == "Command" && absoluteLiteral(call):
				// The sandbox helper, this binary re-exec'd by its path: no
				// lookup, and no program of a Command runs with its environment.
			case pkg == "os" && (name == "Getenv" || name == "LookupEnv"),
				pkg == "exec" && (name == "Command" || name == "CommandContext" || name == "LookPath"),
				name == "Environ" && len(call.Args) == 0:
				out = append(out, fset.Position(call.Pos()).String())
			}
			return true
		})
	}
	return out
}

// absoluteLiteral reports whether call's first argument is a string literal
// that names an absolute path.
func absoluteLiteral(call *ast.CallExpr) bool {
	if len(call.Args) == 0 {
		return false
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return false
	}
	s, err := strconv.Unquote(lit.Value)
	return err == nil && strings.HasPrefix(s, "/")
}

// TestExecEnvRule pins TestExecEnvIsAValue's rule on fixed sources.
func TestExecEnvRule(t *testing.T) {
	for _, c := range []struct {
		rel, src string
		bad      int
		name     string
	}{
		{"host.go", "func OSEnviron(p string) []string { return os.Environ() }", 0, "the host function"},
		{"host.go", "func other() []string { return os.Environ() }", 1, "another runtime function"},
		{"exec/exec.go", "func OSEnviron() []string { return os.Environ() }", 1, "the name in another package"},
		{"exec/exec.go", "func f() { cmd.Env = append(os.Environ(), kv) }", 1, "the parent's environment under a child's"},
		{"exec/exec.go", "func f() { _ = syscall.Environ() }", 1, "syscall's copy"},
		{"exec/exec.go", "func f() { cmd.Env = cmd.Environ() }", 1, "a Cmd's environment"},
		{"exec/exec.go", "func f() { _ = exec.Command(name, args...) }", 1, "a lookup on this PATH"},
		{"exec/exec.go", "func f() { _, _ = exec.LookPath(name) }", 1, "LookPath"},
		{"exec/exec_sandbox_linux.go", "func f() { _ = exec.Command(\"/proc/self/exe\", arg) }", 0, "the helper by its path"},
		{"exec/exec.go", "func f() { _ = os.Getenv(\"PATH\") }", 1, "this PATH read"},
		{"exec/exec.go", "func f() { _, _ = ExecLookPath(name, env[\"PATH\"]) }", 0, "the child's PATH"},
		{"trace.go", "func f() { _ = os.Getenv(TraceEnv) }", 0, "a runtime setting outside exec"},
	} {
		src := "package p\n\n" + c.src + "\n"
		if got := envReads(t, c.rel, []byte(src)); len(got) != c.bad {
			t.Errorf("%s: %s: %d calls flagged, want %d", c.name, c.src, len(got), c.bad)
		}
	}
}
