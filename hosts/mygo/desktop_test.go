package mygo

import (
	"flag"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestMain removes the folder builtErgo built into.
func TestMain(m *testing.M) {
	flag.Parse()
	code := m.Run()
	if ergoDir != "" {
		os.RemoveAll(ergoDir)
	}
	os.Exit(code)
}

var agreeSeed = flag.Uint64("agree.seed", 0, "run TestMyGoHostAgreesWithTestHost on this seed only")

// agreeApps are the apps of cmd/hostagree; seed s plays app s mod 5.
var agreeApps = []string{"counter", "shelf", "form", "load", "pad"}

// words are texts a script types: empty, ASCII, and runes of two and
// three bytes, so Backspace must take a rune, not a byte.
var words = []string{"", "a", "bob", "é", "日本", "zé日"}

// genScript is the hostagree input of one seed: the app, then 4 to 19
// steps that app's labels make meaningful, a few that name no node, and
// focus moves of 0 to 5 Tabs.
func genScript(r *rand.Rand, app string) string {
	lines := []string{app}
	for range 4 + r.IntN(16) {
		var step string
		switch app {
		case "counter":
			step = pick(r, "tap inc", "tap dec", "tap reset", "key +", "key -", "key x", "key q", "tap nothing")
		case "shelf":
			step = pick(r, "tap add", "tap add", "tap del", "tap del", "tap row 1")
		case "form":
			step = pick(r, "type name\t"+pick(r, words...), "type note\t"+pick(r, words...), "tap done", "tap bold", "tap send", "key a")
		case "load":
			step = pick(r, "tap next", "settle", "settle")
		case "pad":
			// below the pad's text and its clear button, which the
			// window's hit test would give the pointer to instead
			step = fmt.Sprintf("point pad %s %d %d", pick(r, "down", "up", "move", "wheel"), r.IntN(200), 70+r.IntN(50))
			if r.IntN(4) == 0 {
				step = "tap clear"
			}
		}
		if r.IntN(5) == 0 {
			step = fmt.Sprintf("focus %d", r.IntN(6))
		}
		lines = append(lines, step)
	}
	return strings.Join(lines, "\n") + "\n"
}

var buildHostAgree = sync.OnceValues(func() (string, error) {
	ergo, err := builtErgo()
	if err != nil {
		return "", err
	}
	bin := filepath.Join(filepath.Dir(ergo), "hostagree")
	if out, err := bounded(ergo, "build", "./cmd/hostagree", "-o", bin).CombinedOutput(); err != nil {
		return "", fmt.Errorf("ergo build hostagree: %v\n%s", err, out)
	}
	return bin, nil
})

// TestMyGoHostAgreesWithTestHost is H3g's acceptance: the desktop driver
// (hosts/mygo/desktop), fed through a headless MyGo window, agrees with
// ui.TestHost on seeded scene scripts of five apps. After every step both
// hosts hold the same tree (ui.SameShapes) and the same app state, and
// each filled node shows its color in the window's rendered frame at the
// place the driver laid it out. A tap is a press and a release at the
// node's centre, so it passes the driver's layout, paint list and hit
// test; typing passes its Backspace and text input; focus steps Tab
// through its focus order and press with Enter.
//
// It builds and runs a Go binary because the driver is Ergo source that
// calls Go (go.Foreign): no in-process check runs it, and hostagree is
// the program that does. A failing seed prints the script and the step
// that differs, and the replay command.
func TestMyGoHostAgreesWithTestHost(t *testing.T) {
	bin, err := buildHostAgree()
	if err != nil {
		t.Fatal(err)
	}
	seeds := []uint64{}
	if *agreeSeed != 0 {
		seeds = append(seeds, *agreeSeed)
	} else {
		for s := uint64(1); s <= 60; s++ {
			seeds = append(seeds, s)
		}
	}
	steps, failed := 0, 0
	for _, seed := range seeds {
		app := agreeApps[seed%uint64(len(agreeApps))]
		script := genScript(rand.New(rand.NewPCG(seed, 0x6167726565)), app)
		cmd := bounded(bin)
		cmd.Stdin = strings.NewReader(script)
		out, err := cmd.CombinedOutput()
		text := string(out)
		var bad []string
		for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
			switch {
			case strings.HasPrefix(line, "ok "):
				steps++
			case line == "end":
			default:
				bad = append(bad, line)
			}
		}
		if err != nil || len(bad) > 0 || !strings.HasSuffix(text, "end\n") {
			failed++
			if failed <= 3 {
				t.Errorf("seed %d (%s): %v\n%s\nscript:\n%s\nreplay: go test -run TestMyGoHostAgreesWithTestHost -agree.seed=%d", seed, app, err, strings.Join(bad, "\n"), script, seed)
			}
		}
	}
	if failed > 3 {
		t.Errorf("%d more seeds differ", failed-3)
	}
	t.Logf("%d seeds, %d steps agree", len(seeds), steps)
}

// mygoAdmitted are the identifiers of MyGo's root package the host may
// use: the application (App, of type Application) and its windows, and the
// Content a window shows. Their methods and fields come with them.
// Nothing of mygo/ui is admitted.
var mygoAdmitted = map[string]bool{"App": true, "Application": true, "Window": true, "WindowOptions": true, "NewWindow": true, "Content": true}

// TestMyGoHostPrimitivesOnly holds the host to MyGo's drawing layer
// (H3g, q65): go/types over each non-test Go package of this module but
// the drawing module finds every object a file uses, and refuses an
// object of a MyGo package other than the drawing layer's (and the
// internal packages its aliases name), and of the root package one outside
// mygoAdmitted (a method counts as its receiver's type).
func TestMyGoHostPrimitivesOnly(t *testing.T) {
	fset := token.NewFileSet()
	imp := importer.ForCompiler(fset, "source", nil)
	used := 0
	for _, dir := range []string{".", "screen"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		var files []*ast.File
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			files = append(files, f)
		}
		info := &types.Info{Uses: map[*ast.Ident]types.Object{}, Selections: map[*ast.SelectorExpr]*types.Selection{}}
		if _, err := (&types.Config{Importer: imp}).Check(dir, fset, files, info); err != nil {
			t.Fatal(err)
		}
		for id, obj := range info.Uses {
			if obj.Pkg() == nil {
				continue
			}
			if why := refused(obj); why != "" {
				t.Errorf("%s: %s uses %s.%s: %s", fset.Position(id.Pos()), dir, obj.Pkg().Path(), obj.Name(), why)
			}
			used++
		}
	}
	if used == 0 {
		t.Fatal("no object used: the scan read nothing")
	}
}

// refused is why the host may not use obj, "" when it may.
func refused(obj types.Object) string {
	path := obj.Pkg().Path()
	const root = "github.com/egoist/mygo"
	switch {
	case path == root:
		name := obj.Name()
		if f, ok := obj.(*types.Func); ok {
			if recv := f.Signature().Recv(); recv != nil {
				name = named(recv.Type())
			}
		}
		if v, ok := obj.(*types.Var); ok && v.IsField() {
			return "" // a field of an admitted type: the type was named first
		}
		if !mygoAdmitted[name] {
			return "MyGo's root package admits App, Application, Window, WindowOptions, NewWindow and Content"
		}
	case path == root+"/drawing" || strings.HasPrefix(path, root+"/drawing/"):
	case strings.HasPrefix(path, root+"/internal/"):
		// The drawing layer's types are aliases of these, so their fields,
		// methods and constants' types land here; Go's internal rule
		// refuses an import of them from this module, so the drawing
		// layer is the only way in.
	case strings.HasPrefix(path, root+"/"):
		return "only the drawing layer of MyGo is admitted"
	}
	return ""
}

// named is the name of a method's receiver type.
func named(t types.Type) string {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	if n, ok := t.(*types.Named); ok {
		return n.Obj().Name()
	}
	return t.String()
}
