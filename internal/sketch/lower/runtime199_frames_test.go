package lower

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// One catalog drives both the ordinary frame axis and the nesting axis.
// sites names the lowering functions that allocate the frame; shared
// paths (methods/declarations and the three closure forms) share a site.
// A wire/initializer has only an expression body, so its nested statements
// run in an immediately called literal. The outer frame is tested too.
type i1FrameKind struct {
	name, goClass string
	params        bool
	sites         map[string]int
	program       func(params, row, attempt, body string) string
	wrap          func(depth int, body string) string
}

var i1FrameKinds = []i1FrameKind{
	{name: "decl", goClass: "decl", params: true, sites: map[string]int{"emitFuncBody": 1}, program: func(ps, row, _ string, body string) string {
		return "func subject(" + ps + ") int" + row + " {\n" + body + "}\n"
	}},
	{name: "literal", goClass: "lit", params: true, sites: map[string]int{"funcLit": 1}, program: func(ps, row, attempt string, body string) string {
		return "func subject(" + ps + ") int" + row + " {\nlet local = func() int" + row + " {\n" + body + "}\n" + attempt + "local()\n}\n"
	}, wrap: func(d int, body string) string {
		return "let local" + i1Depth(d) + " = func() int {\n" + body + "}\nlocal" + i1Depth(d) + "()\n"
	}},
	{name: "task", goClass: "lit", params: true, sites: map[string]int{"funcLit": 1}, program: func(ps, row, attempt string, body string) string {
		return "func subject(" + ps + ") int" + row + " {\nlet t = task.Go() {\n" + body + "}\n" + attempt + "t.Wait()\n}\n"
	}, wrap: func(d int, body string) string {
		return "let t" + i1Depth(d) + " = task.Go() {\n" + body + "}\nt" + i1Depth(d) + ".Wait()\n"
	}},
	{name: "trailing", goClass: "lit", params: true, sites: map[string]int{"funcLit": 1}, program: func(ps, row, attempt string, body string) string {
		return "func run(f func() int" + row + ") int" + row + " { " + attempt + "f() }\nfunc subject(" + ps + ") int" + row + " {\n" + attempt + "run() {\n" + body + "}\n}\n"
	}, wrap: func(_ int, body string) string { return "run() {\n" + body + "}\n" }},
	{name: "provider", goClass: "decl", sites: map[string]int{"emitFuncBody": 1}, program: func(_, _, _ string, body string) string {
		return "type Worker { Run(n int) int }\ntype Impl {}\nfunc Run(_ Impl, n int) int {\n" + body + "}\nlet Work = wire { Worker: Impl }\nfunc subject() int { with Work { Worker.Run(1) } }\n"
	}},
	// The block uses Probe: a with builds only the providers its block
	// reaches (rows.md, Wire 7; With.Acquired).
	{name: "wire", goClass: "lit", sites: map[string]int{"emitWire": 1}, program: func(_, _, _ string, body string) string {
		return "type Carrier { n int }\nfunc Tick(_ Carrier) {}\nfunc Mark(_ Carrier) {}\nlet Deep = wire { Probe: Carrier { n: func() int {\n" + body + "}() } }\nfunc subject() int { with Deep {\nProbe.Tick()\n1\n} }\n"
	}},
	{name: "initializer", goClass: "lit", sites: map[string]int{"packageVar": 2, "packagePattern": 1}, program: func(_, _, _ string, body string) string {
		// A binding, a destructuring initializer and a discarded value
		// exercise all three allocation paths in this frame class.
		value := "func() int {\n" + body + "}()"
		return "let seed = " + value + "\nlet other, second = (" + value + ", 0)\nlet _ = " + value + "\nfunc subject() int { seed + other + second }\n"
	}},
}

func i1Depth(d int) string { return strconv.Itoa(d) }

// With is a region, never another frame. It joins the nesting grammar
// separately from the catalog of frame-opening paths.
func i1NestingFrames() []struct {
	name  string
	frame bool
	wrap  func(int, string) string
} {
	var out []struct {
		name  string
		frame bool
		wrap  func(int, string) string
	}
	for _, f := range i1FrameKinds {
		if f.wrap != nil {
			out = append(out, struct {
				name  string
				frame bool
				wrap  func(int, string) string
			}{f.name, true, f.wrap})
		}
	}
	return append(out, struct {
		name  string
		frame bool
		wrap  func(int, string) string
	}{"with", false, func(_ int, body string) string { return "with Local {\n" + body + "}\n" }})
}

func TestRuntime199FrameClassCoverage(t *testing.T) {
	wantSites := map[string]int{}
	seenNames := map[string]bool{}
	for _, f := range i1FrameKinds {
		if seenNames[f.name] || f.program == nil || len(f.sites) == 0 {
			t.Fatalf("duplicate or incomplete frame obligation %s", f.name)
		}
		seenNames[f.name] = true
		for owner, count := range f.sites {
			if prev := wantSites[owner]; prev != 0 && prev != count {
				t.Errorf("inconsistent shared frame site %s", owner)
			}
			wantSites[owner] = count
		}
	}
	// Parse every production Go file. A new constructor call, including one in
	// an existing helper, fails until the catalog covers it. Do not grep
	// line numbers or infer the domain from the tests themselves.
	gotSites := map[string]int{}
	i1LowerSource(t, func(owner string, node ast.Node) {
		ast.Inspect(node, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "newFrame" {
					gotSites[owner]++
				}
			}
			return true
		})
	})
	for owner, got := range gotSites {
		if got != wantSites[owner] {
			t.Errorf("lowering opens %d frames in %s, catalog covers %d", got, owner, wantSites[owner])
		}
	}
	for owner, want := range wantSites {
		if gotSites[owner] != want {
			t.Errorf("catalog expects %d frames in %s, lowering opens %d", want, owner, gotSites[owner])
		}
	}
	for _, axis := range []struct {
		name  string
		cases []i1Case
	}{{"ordinary", append(i1Cases(), i1BlockCases()...)}, {"nesting", i1NestCases()}} {
		classes := map[string]int{}
		for _, c := range axis.cases {
			classes[strings.Split(c.name, "/")[0]]++
			if axis.name == "nesting" && strings.HasPrefix(c.name, "nest/") {
				// Before this catalog, all nesting roots were declarations.
				classes["nest-decl"]++
			}
		}
		for _, f := range i1FrameKinds {
			key := f.name
			if axis.name == "nesting" {
				key = "nest-" + key
			}
			if classes[key] == 0 {
				t.Errorf("%s axis never generates frame class %s", axis.name, f.name)
			}
		}
	}
	t.Logf("%d frame classes cover allocation sites %v", len(i1FrameKinds), gotSites)
	for _, frame := range i1FrameKinds {
		if frame.name != "wire" && frame.name != "initializer" {
			continue
		}
		for _, effect := range []struct {
			name, body string
			caps       int
			opaque     bool
			tasks      bool
		}{
			{name: "pure"},
			{name: "own-with", body: "with Local { Probe.Tick() }\n", caps: 1},
			{name: "host", body: "let xs = []int{}; let _ = xs.Append(1)\n", opaque: true},
			{name: "scope", body: "let t = task.Go() { 1 }; t.Stop()\n", opaque: true, tasks: true},
		} {
			t.Run(frame.name+"/"+effect.name, func(t *testing.T) {
				o := mustLower(t, "outerframe", i1Defs+frame.program("", "", "", effect.body+"1\n")+"func main() {}\n")
				found := 0
				for key, fp := range o.pkg.Info.Footprints {
					target := false
					switch key := key.(type) {
					case *syntax.ContextDecl:
						target = frame.name == "wire" && key.Name.Name == "Deep"
					case *syntax.VarSpec:
						target = frame.name == "initializer"
					}
					if target {
						found++
						if len(fp.Caps) != effect.caps || fp.Tasks != effect.tasks || fp.Opaque != effect.opaque || fp.EntryPoll != effect.opaque {
							t.Errorf("outer %s footprint %+v, want caps=%d tasks=%v opaque/entry=%v", frame.name, fp, effect.caps, effect.tasks, effect.opaque)
						}
					}
				}
				wantFrames := 1
				if frame.name == "initializer" {
					wantFrames = 3
				}
				if found != wantFrames {
					t.Errorf("found %d outer footprints, want %d", found, wantFrames)
				}
				f, err := parser.ParseFile(token.NewFileSet(), "main.go", o.mainGo(), 0)
				if err != nil {
					t.Fatal(err)
				}
				found = 0
				for _, decl := range f.Decls {
					fd, ok := decl.(*ast.FuncDecl)
					if !ok || fd.Body == nil {
						continue
					}
					var bodies []*ast.BlockStmt
					if frame.name == "wire" && fd.Name.Name == "buildDeep" {
						bodies = append(bodies, fd.Body)
					}
					if frame.name == "initializer" && fd.Name.Name == "ergoInit" {
						for _, stmt := range fd.Body.List {
							if block, ok := stmt.(*ast.BlockStmt); ok {
								bodies = append(bodies, block)
							}
						}
					}
					for _, body := range bodies {
						found++
						got := len(body.List) > 0 && isOwnCheck(body.List[0], i1EnvParam(fd.Type))
						if got != effect.opaque {
							t.Errorf("outer %s entry poll = %v, want %v", frame.name, got, effect.opaque)
						}
					}
				}
				if found != wantFrames {
					t.Errorf("found %d emitted outer frames, want %d", found, wantFrames)
				}
			})
		}
	}
}

// Check actual checked/emitted forms too. Several semantic frame classes
// share an allocation site, so allocation counts alone cannot catch a
// missing task, provider or ordinary trailing-block obligation.
func i1RequireFrameClasses(t *testing.T, o *lowered) {
	t.Helper()
	known := map[string]bool{}
	for _, f := range i1FrameKinds {
		known[f.name] = true
	}
	require := func(name string) {
		if !known[name] {
			t.Errorf("checked/emitted %s frame has no generator obligation", name)
		}
	}
	blocks := map[*syntax.BlockStmt]string{}
	for x, call := range o.pkg.Info.Calls {
		if x.Trailing == nil || call.Kind == check.CallWith {
			continue
		}
		class := "trailing"
		if call.Func != nil && call.Func.Pkg != nil && call.Func.Pkg.Path == "task" && call.Func.Name == "Go" {
			class = "task"
		}
		blocks[x.Trailing] = class
	}
	for key := range o.pkg.Info.Footprints {
		switch key := key.(type) {
		case *syntax.FuncDecl:
			require("decl")
		case *syntax.FuncLit:
			require("literal")
		case *syntax.BlockStmt:
			if class := blocks[key]; class != "" {
				require(class)
			} else {
				t.Error("footprint block belongs to no frame-opening call")
			}
		case *syntax.ContextDecl:
			require("wire")
		case *syntax.VarSpec:
			require("initializer")
		default:
			t.Errorf("unknown checked frame %T", key)
		}
	}
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", o.mainGo(), 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range f.Decls {
		if fd, ok := decl.(*ast.FuncDecl); ok && fd.Recv != nil && i1EnvParam(fd.Type) != nil {
			require("provider")
		}
	}
}
