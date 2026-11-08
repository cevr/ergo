package residue_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// noOptionalLocks is git's switch that stops a read-only command from
// taking the repository's optional locks: without it `git status` (and
// `git diff`, `git describe`) refreshes the index, which writes
// .git/index.lock and replaces .git/index.
const noOptionalLocks = "--no-optional-locks"

// TestGitCallsTakeNoOptionalLocks is the guard of b9-fix-gate round 3: every
// git process Go code in the repository starts (a test, a step, the gate)
// passes --no-optional-locks first, so it never writes the repository it
// reads. In the merge tier's export (scripts/gate exportCommit) .git is a
// directory inside the checkout, and the closure sweep fails a run during
// which anything in the checkout changed (internal/closure readLog): a
// `git status` in one step made five sweep steps of the batch-9 merge tier
// fail with ".git/index.lock changed during".
//
// The rule is syntactic, so it reads the same on every host: a call that
// runs the program "git" (exec.Command, exec.CommandContext, or a helper
// that takes the program and then its arguments as strings, a []string or
// a spread list) passes noOptionalLocks as the next argument, alone or as
// the first element of the []string the arguments are built from. A helper
// that takes git's arguments as one plain variable is a known gap.
func TestGitCallsTakeNoOptionalLocks(t *testing.T) {
	root, err := exec.Command("git", "--no-optional-locks", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	repo := strings.TrimSpace(string(root))
	ls := exec.Command("git", "--no-optional-locks", "ls-files", "-z", "--", "*.go")
	ls.Dir = repo
	list, err := ls.Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	files := 0
	for _, name := range strings.Split(strings.TrimSuffix(string(list), "\x00"), "\x00") {
		if name == "" {
			continue
		}
		src, err := os.ReadFile(filepath.Join(repo, name))
		if err != nil {
			t.Fatal(err)
		}
		files++
		for _, at := range gitCallsTakingLocks(t, name, src) {
			t.Errorf("%s: git runs without %s first, so it may write the repository's index (b9-fix-gate r3)", at, noOptionalLocks)
		}
	}
	if files < 100 {
		t.Fatalf("read %d tracked Go files; the listing is broken", files)
	}
}

// gitCallsTakingLocks returns the position of each call in src that runs
// git without noOptionalLocks as its first argument.
func gitCallsTakingLocks(t *testing.T, name string, src []byte) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	var out []string
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		for i, arg := range call.Args[:max(0, len(call.Args)-1)] {
			next := call.Args[i+1]
			if !isString(arg, "git") || startsWithNoOptionalLocks(next) {
				continue
			}
			if isExecCommand(call.Fun) || gitArguments(call, i+1) {
				out = append(out, fset.Position(arg.Pos()).String())
			}
		}
		return true
	})
	return out
}

// isExecCommand reports whether fun is exec.Command or exec.CommandContext.
func isExecCommand(fun ast.Expr) bool {
	sel, ok := fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "exec" && (sel.Sel.Name == "Command" || sel.Sel.Name == "CommandContext")
}

// gitArguments reports whether call.Args[i], after "git" in a call that is
// no exec.Command, reads as git's arguments: a string, a []string literal,
// an append, or a list spread into the call (args...). A value of another
// form (a path, a map lookup) names git as a tool, not a run of it.
func gitArguments(call *ast.CallExpr, i int) bool {
	switch arg := call.Args[i].(type) {
	case *ast.BasicLit:
		return arg.Kind == token.STRING
	case *ast.CompositeLit:
		return true
	case *ast.CallExpr:
		fun, ok := arg.Fun.(*ast.Ident)
		return ok && fun.Name == "append"
	}
	return call.Ellipsis.IsValid() && i == len(call.Args)-1
}

// startsWithNoOptionalLocks reports whether the argument after "git" is
// noOptionalLocks, a []string literal that starts with it, or an append
// to such a literal.
func startsWithNoOptionalLocks(arg ast.Expr) bool {
	if call, ok := arg.(*ast.CallExpr); ok {
		if fun, ok := call.Fun.(*ast.Ident); ok && fun.Name == "append" && len(call.Args) > 0 {
			arg = call.Args[0]
		}
	}
	if lit, ok := arg.(*ast.CompositeLit); ok && len(lit.Elts) > 0 {
		arg = lit.Elts[0]
	}
	return isString(arg, noOptionalLocks)
}

func isString(e ast.Expr, want string) bool {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return false
	}
	s, err := strconv.Unquote(lit.Value)
	return err == nil && s == want
}

// TestGitCallRule pins the rule on fixed sources.
func TestGitCallRule(t *testing.T) {
	for _, c := range []struct {
		src  string
		bad  int
		name string
	}{
		{`exec.Command("git", "status")`, 1, "a bare call"},
		{`exec.Command("git", args...)`, 1, "arguments built elsewhere"},
		{`exec.CommandContext(ctx, "git", "-C", dir, "ls-files")`, 1, "a context call"},
		{`run(t, dir, "git", append(identity, "add", ".")...)`, 1, "a helper that takes the program"},
		{`exec.Command("git", append([]string{"-C", dir}, args...)...)`, 1, "the switch not first"},
		{`exec.Command("git", "--no-optional-locks", "status")`, 0, "the switch first"},
		{`exec.Command("git", append([]string{"--no-optional-locks", "-C", dir}, args...)...)`, 0, "the switch first in an append"},
		{`exec.Command("git", []string{"--no-optional-locks", "status"}...)`, 0, "the switch first in a literal"},
		{`command("git", "rev-parse", "HEAD")`, 1, "a helper with literal arguments"},
		{`run("git", args...)`, 1, "a helper with arguments spread"},
		{`exec.Command("git", sub)`, 1, "exec.Command with any argument"},
		{`link(t, bin, "git", realTools["git"])`, 0, "git named as a tool"},
		{`exec.LookPath("git")`, 0, "no arguments after git"},
		{`filepath.Join(dir, "git")`, 0, "git as the last argument"},
		{`f([]string{"git", "sh"})`, 0, "git in a list"},
	} {
		src := "package p\n\nfunc f() {\n\t" + c.src + "\n}\n"
		if got := gitCallsTakingLocks(t, "p.go", []byte(src)); len(got) != c.bad {
			t.Errorf("%s: %s: %d calls flagged, want %d", c.name, c.src, len(got), c.bad)
		}
	}
}
