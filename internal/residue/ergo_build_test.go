package residue_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/ergobuild"
	"ergo.local/prototype/internal/ergoversion"
)

// TestErgoBuildsReadOneSetting: every build of the ergo tool in the
// repository's Go code takes its command or environment from
// internal/ergobuild, the one place that says the tool is built without cgo
// (ERGO-STARTUP). It refuses an exec.Command or exec.CommandContext whose
// literal arguments run `go build`, `go install` or `go run` of cmd/ergo
// (a path ending in cmd/ergo, or "." in cmd/ergo itself when the command
// sets no Dir: a test's own control program builds in its folder), and a mutant
// runner Tool of ./cmd/ergo without an Env. A module that cannot import
// ergobuild (hosts/mygo) passes when the same function names
// CGO_ENABLED=0. A `go build` it refuses also without -trimpath
// (ergobuild.Flags: every checkout builds the same bytes). Mutant: a build
// site that names the go command itself (start-ergo-build-bypasses-setting).
func TestErgoBuildsReadOneSetting(t *testing.T) {
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
	sites := 0
	for _, name := range strings.Split(strings.TrimSuffix(string(list), "\x00"), "\x00") {
		if name == "" || strings.Contains("/"+name, "/testdata/") || strings.HasPrefix(name, "internal/ergobuild/") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(repo, name))
		if err != nil {
			if os.IsNotExist(err) {
				continue // deleted in the checkout, not yet committed
			}
			t.Fatal(err)
		}
		if !strings.Contains(string(src), "cmd/ergo") && filepath.Dir(name) != "cmd/ergo" {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			continue // not this rule's business
		}
		inCLI := filepath.Dir(name) == "cmd/ergo"
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			var body ast.Node = decl
			if ok && fn.Body != nil {
				body = fn.Body
			}
			namesSetting := strings.Contains(string(src[fset.Position(body.Pos()).Offset:fset.Position(body.End()).Offset]), "CGO_ENABLED=0")
			// In cmd/ergo, "." is the tool unless the command runs in a
			// directory of its own (a test's control program).
			hasDir := strings.Contains(string(src[fset.Position(body.Pos()).Offset:fset.Position(body.End()).Offset]), ".Dir = ")
			ast.Inspect(body, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.CallExpr:
					if !isGoToolCall(n.Fun) || !buildsErgo(literals(n.Args), inCLI && !hasDir) {
						return true
					}
					sites++
					if !namesSetting {
						t.Errorf("%s: builds the ergo tool with the go command itself; take the command from ergobuild.Command (or its environment from ergobuild.Env), so the tool is built without cgo as everywhere else", fset.Position(n.Pos()))
					}
					if lits := literals(n.Args); slices.Contains(lits, "build") && !slices.Contains(lits, "-trimpath") {
						t.Errorf("%s: builds the ergo tool without -trimpath; take the command from ergobuild.Command, so every checkout builds the same bytes (ergobuild.Flags)", fset.Position(n.Pos()))
					}
				case *ast.CompositeLit:
					if id, ok := n.Type.(*ast.Ident); !ok || id.Name != "Tool" {
						return true
					}
					pkg, env := "", false
					for _, elt := range n.Elts {
						if kv, ok := elt.(*ast.KeyValueExpr); ok {
							if key, ok := kv.Key.(*ast.Ident); ok {
								switch key.Name {
								case "Package":
									if lit, ok := kv.Value.(*ast.BasicLit); ok {
										pkg, _ = strconv.Unquote(lit.Value)
									}
								case "Env":
									env = true
								}
							}
						}
					}
					if strings.HasSuffix(pkg, "cmd/ergo") {
						sites++
						if !env {
							t.Errorf("%s: the mutant runner builds the ergo tool without Env: []string{ergobuild.Cgo}", fset.Position(n.Pos()))
						}
					}
				}
				return true
			})
		}
	}
	if sites == 0 {
		t.Error("no build of cmd/ergo found; the rule checks nothing (update or drop it)")
	}
}

// isGoToolCall reports whether fun is exec.Command or exec.CommandContext.
func isGoToolCall(fun ast.Expr) bool {
	sel, ok := fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	x, ok := sel.X.(*ast.Ident)
	return ok && x.Name == "exec" && (sel.Sel.Name == "Command" || sel.Sel.Name == "CommandContext")
}

// literals are the string literals among args, unquoted.
func literals(args []ast.Expr) []string {
	var out []string
	for _, a := range args {
		if lit, ok := a.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			if s, err := strconv.Unquote(lit.Value); err == nil {
				out = append(out, s)
			}
		}
	}
	return out
}

// buildsErgo reports whether words run the go command's build, install or
// run of cmd/ergo.
func buildsErgo(words []string, inCLI bool) bool {
	verb, target := false, false
	for i, w := range words {
		if (w == "build" || w == "install" || w == "run") && i > 0 && words[i-1] == "go" {
			verb = true
		}
		if strings.HasSuffix(w, "cmd/ergo") || inCLI && w == "." {
			target = true
		}
	}
	return verb && target
}

// TestErgoDocsBuildAsTheToolSays: what the docs tell a reader to run builds
// the ergo tool as the tool tells them to (ERGO-STARTUP-2). The install
// command ergo prints (ergoversion.InstallCommand, S108's "install it: …")
// starts with ergobuild.Cgo, and so does every command line of a README
// that builds, installs or runs cmd/ergo, and every mention of the install
// command in a Markdown file. A dated record that quotes an old command in
// prose (a receipt, a research note) is not a command line and is left as
// it was. Mutants: a README's build line without the setting
// (start-readme-builds-with-cgo), the printed command without it
// (start-install-command-with-cgo).
func TestErgoDocsBuildAsTheToolSays(t *testing.T) {
	prefix := ergobuild.Cgo + " "
	if install := ergoversion.InstallCommand("v1.2.3"); !strings.HasPrefix(install, prefix) {
		t.Errorf("ergo prints %q to install itself; it should start with %q (internal/ergobuild)", install, prefix)
	}
	root, err := exec.Command("git", "--no-optional-locks", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	repo := strings.TrimSpace(string(root))
	ls := exec.Command("git", "--no-optional-locks", "ls-files", "-z", "--", "*.md")
	ls.Dir = repo
	list, err := ls.Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	const install = "go install github.com/cevr/ergo/cmd/ergo@"
	lines := 0
	for _, name := range strings.Split(strings.TrimSuffix(string(list), "\x00"), "\x00") {
		if name == "" {
			continue
		}
		src, err := os.ReadFile(filepath.Join(repo, name))
		if err != nil {
			if os.IsNotExist(err) {
				continue // deleted in the checkout, not yet committed
			}
			t.Fatal(err)
		}
		readme := filepath.Base(name) == "README.md"
		for i, line := range strings.Split(string(src), "\n") {
			if at := strings.Index(line, install); at >= 0 && !strings.HasSuffix(line[:at], prefix) {
				t.Errorf("%s:%d: the install command without %q, unlike the one ergo prints", name, i+1, prefix)
			}
			if !readme {
				continue
			}
			command := strings.TrimPrefix(strings.TrimLeft(line, " \t"), "$ ")
			bare := strings.TrimPrefix(command, prefix)
			if !buildsErgo(strings.Fields(bare), false) || !strings.HasPrefix(bare, "go ") {
				continue
			}
			lines++
			if bare == command {
				t.Errorf("%s:%d: %q builds the ergo tool with cgo; write %q", name, i+1, command, prefix+command)
			}
		}
	}
	if lines == 0 {
		t.Error("no README builds cmd/ergo; the rule checks nothing (update or drop it)")
	}
}
