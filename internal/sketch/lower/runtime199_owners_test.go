package lower

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The emitted-source seam guards the single owner, including a new closure
// that no existing source fixture reaches. Live stop/drain tests below guard
// the environment the adapters pass. Neither check substitutes for the other.
func TestRuntime199EnvBindingOwner(t *testing.T) {
	i1LowerSource(t, func(owner string, node ast.Node) {
		var text strings.Builder
		envType := false
		ast.Inspect(node, func(n ast.Node) bool {
			if s, ok := n.(*ast.BasicLit); ok && s.Kind == token.STRING {
				value, err := strconv.Unquote(s.Value)
				if err != nil {
					t.Fatal(err)
				}
				text.WriteString(value)
				if strings.Contains(strings.ReplaceAll(value, " ", ""), "*Env") {
					envType = true
				}
			}
			// An Env value in a closure's body is not a parameter. Follow
			// the type spelling: "*" immediately before the env type name.
			if concat, ok := n.(*ast.BinaryExpr); ok && concat.Op == token.ADD {
				if call, ok := concat.Y.(*ast.CallExpr); ok {
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "env" {
						envType = envType || strings.HasSuffix(i1LastText(concat.X), "*")
					}
				}
			}
			return true
		})
		// A function type contains func( but no body. A declaration has
		// func followed by its name. Only a closure has both func( and {.
		if envType && strings.Contains(text.String(), "func(") && strings.Contains(text.String(), "{") && owner != "envClosure" {
			t.Errorf("%s writes an Env closure outside envClosure", owner)
		}
	})
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

func TestRuntime199FrameOwner(t *testing.T) {
	i1LowerSource(t, func(owner string, node ast.Node) {
		if owner == "newFrame" {
			return
		}
		isFrame := func(x ast.Expr) bool {
			id, ok := x.(*ast.Ident)
			return ok && id.Name == "frame"
		}
		ast.Inspect(node, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.CompositeLit:
				if isFrame(n.Type) {
					t.Errorf("%s constructs frame outside newFrame", owner)
				}
			case *ast.CallExpr:
				if id, ok := n.Fun.(*ast.Ident); ok && id.Name == "new" && len(n.Args) == 1 && isFrame(n.Args[0]) {
					t.Errorf("%s allocates frame outside newFrame", owner)
				}
			case *ast.ValueSpec:
				if isFrame(n.Type) {
					t.Errorf("%s declares a frame value outside newFrame", owner)
				}
			case *ast.AssignStmt:
				for _, rhs := range n.Rhs {
					if copy, ok := rhs.(*ast.StarExpr); ok {
						if field, ok := copy.X.(*ast.SelectorExpr); ok && field.Sel.Name == "frame" {
							t.Errorf("%s copies a frame outside newFrame", owner)
						}
					}
				}
			}
			return true
		})
	})
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

const i1ValueSpin = `func spin() int {
	let mut c = 0
	for { c += 1 }
	with Local { Probe.Tick() }
	c
}
`

const i1ValueStop = `	try ready.Pop()
	let mut delay = 0
	for delay < 20000000 { delay += 1 }
	t.Stop()
	Console.Println("stopped")
}
`

func i1ValuePrograms() map[string]string {
	return map[string]string{
		"fv-child": i1LiveDefs + i1ValueSpin + `func main() fails queue.Closed needs Console {
	let ready = queue.New[int](0)
	let f = spin
	let t = task.Go() {
		try ready.Push(1)
		f()
	}
` + i1ValueStop,
		"fv-param": i1LiveDefs + i1ValueSpin + `func run(ready queue.Queue[int], g func() int) int fails queue.Closed {
	try ready.Push(1)
	g()
}
func main() fails queue.Closed needs Console {
	let ready = queue.New[int](0)
	let f = spin
	let t = task.Go() { run(ready, f) }
` + i1ValueStop,
		"fv-package": i1LiveDefs + i1ValueSpin + `let pf = spin
func main() fails queue.Closed needs Console {
	let ready = queue.New[int](0)
	let t = task.Go() {
		try ready.Push(1)
		pf()
	}
` + i1ValueStop,
		"literal-control": i1LiveDefs + `func main() fails queue.Closed needs Console {
	let ready = queue.New[int](0)
	let f = func() int {
		let mut c = 0
		for { c += 1 }
		with Local { Probe.Tick() }
		c
	}
	let t = task.Go() {
		try ready.Push(1)
		f()
	}
` + i1ValueStop,
		"no-value-wrapper": i1LiveDefs + `func spawnIt[T](ready queue.Queue[int], f func() T) Task[T, queue.Closed] {
	task.Go() {
		try ready.Push(1)
		f()
	}
}
func main() fails queue.Closed needs Console {
	let ready = queue.New[int](0)
	let t = spawnIt(ready) {
		let mut c = 0
		for { c += 1 }
		with Local { Probe.Tick() }
	}
` + i1ValueStop,
		"task-result": i1LiveDefs + i1ValueSpin + `func start(ready queue.Queue[int]) Task[int, queue.Closed] fails queue.Closed {
	try ready.Push(1)
	spin()
	task.Go() { try ready.Pop() }
}
func main() fails queue.Closed needs Console {
	let ready = queue.New[int](0)
	let f = start
	let t = task.Go() {
		let nested = try f(ready)
		nested.Stop()
	}
` + i1ValueStop,
	}
}

// A value is made in the parent (or the package initializer), then called in
// the child. Stop must drain the child while the parent is still running.
// The queue handshake and the inherited counting delay put the child in its
// loop before Stop. Polls before that loop cannot hide a wrong-task adapter.
func TestRuntime199EnvBindingStops(t *testing.T) {
	for name, src := range i1ValuePrograms() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			runWithin(t, "envbinding", src, "stopped\n", 45*time.Second)
		})
	}
}
