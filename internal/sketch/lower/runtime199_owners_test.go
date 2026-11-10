package lower

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Builders may call helpers that read fc.envExpr instead of using their env
// argument. Both must name the invocation environment, including nested
// closures. A recovered refusal inside a builder must leave that builder's
// binding unchanged. The maker's frame is reachable through fc only from a
// captured callback, which is not a source frame (acquireFlow).
func TestRuntime199EnvClosureBinding(t *testing.T) {
	t.Run("discarded", func(t *testing.T) {
		fc, file := i1EnvTestCtx(t)
		fc.envClosure(callerEnv, "_", file, func(env string) ([]string, string, string) {
			if env != "_" {
				t.Errorf("discarded parameter = %q; want _", env)
			}
			defer func() {
				if r, ok := recover().(refusal); !ok || !strings.Contains(r.msg, "internal compiler error") {
					t.Errorf("envExpr under a discarded parameter = %v; want an ICE", r)
				}
			}()
			fc.envExpr(file)
			return nil, "", ""
		})
		if got := fc.envExpr(file); got != "maker" {
			t.Errorf("env after builder = %q; want maker", got)
		}
	})
	for _, c := range []struct {
		name    string
		binding envBinding
		want    string
	}{
		{"caller", callerEnv, "invocation"},
		{"child", childEnv, "&invocation"},
		{"captured", capturedEnv, "maker"},
	} {
		for _, panics := range []bool{false, true} {
			name := c.name + "/return"
			if panics {
				name = c.name + "/panic"
			}
			t.Run(name, func(t *testing.T) {
				fc, file := i1EnvTestCtx(t)
				maker := fc.frame
				sentinel := new(int)
				func() {
					defer func() {
						got := recover()
						if panics && got != sentinel || !panics && got != nil {
							t.Errorf("builder panic = %v, expected panic: %t", got, panics)
						}
					}()
					fc.envClosure(c.binding, "invocation", file, func(env string) ([]string, string, string) {
						if got := fc.envExpr(file); got != c.want || env != c.want {
							t.Errorf("builder envExpr = %q, argument = %q; want %q", got, env, c.want)
						}
						if inFrame := c.binding == capturedEnv; (fc.frame == maker) != inFrame || !inFrame && fc.frame != nil {
							t.Errorf("builder frame is the maker's %t; want %t, and no other frame", fc.frame == maker, inFrame)
						}
						builderFrame := fc.frame
						fc.envClosure(callerEnv, "nested", file, func(env string) ([]string, string, string) {
							if got := fc.envExpr(file); got != "nested" || env != "nested" {
								t.Errorf("nested envExpr = %q, argument = %q; want nested", got, env)
							}
							return nil, "", ""
						})
						if got := fc.envExpr(file); got != c.want {
							t.Errorf("env after nested builder = %q; want %q", got, c.want)
						}
						// A nested refusal recovered inside this builder.
						func() {
							defer func() {
								if got := recover(); got != sentinel {
									t.Errorf("nested builder panic = %v", got)
								}
							}()
							fc.envClosure(callerEnv, "refused", file, func(string) ([]string, string, string) {
								fc.frame = newFrame(nil, false, true)
								panic(sentinel)
							})
						}()
						if got := fc.envExpr(file); got != c.want || fc.frame != builderFrame {
							t.Errorf("after a recovered nested refusal: env %q, builder frame restored %t; want %q, true", got, fc.frame == builderFrame, c.want)
						}
						if panics {
							panic(sentinel)
						}
						return nil, "", ""
					})
				}()
				if got := fc.envExpr(file); got != "maker" || fc.frame != maker {
					t.Errorf("after builder: env %q, maker frame restored %t; want maker, true", got, fc.frame == maker)
				}
			})
		}
	}
}

// i1EnvTestCtx is a function context whose env is "maker", inside an open
// maker frame.
func i1EnvTestCtx(t *testing.T) (*funcCtx, *syntax.File) {
	t.Helper()
	const src = "func main() {}\n"
	file, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	pkg, diags := check.Check("main", []*syntax.File{file}, nil)
	if len(diags) > 0 {
		t.Fatal(diags)
	}
	l := newLowerer(Unit{Package: pkg, Files: []*syntax.File{file}, Sources: []Source{{Path: testSourcePath, Text: src}}})
	fc := newFuncCtx(l, newNamer(nil), false)
	fc.env = "maker"
	fc.frame = newFrame(nil, false, true)
	return fc, file
}

func i1LastText(x ast.Expr) string {
	if concat, ok := x.(*ast.BinaryExpr); ok && concat.Op == token.ADD {
		return i1LastText(concat.Y)
	}
	if s, ok := x.(*ast.BasicLit); ok && s.Kind == token.STRING {
		text, _ := strconv.Unquote(s.Value)
		return text
	}
	return ""
}

// Scan all production declarations, including package-level initializers.
// File offsets, line numbers and a list of known sites are not the domain.
func i1LowerSource(t *testing.T, visit func(owner string, node ast.Node)) {
	t.Helper()
	files, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if !strings.HasSuffix(file.Name(), ".go") || strings.HasSuffix(file.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), file.Name(), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			owner := file.Name() + ":package"
			if fn, ok := decl.(*ast.FuncDecl); ok {
				owner = fn.Name.Name
			}
			visit(owner, decl)
		}
	}
}
