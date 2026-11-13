package lower

import (
	"bufio"
	"bytes"
	"fmt"
	"go/ast"
	"go/build"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path"
	"slices"
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/gobind"
)

// platformProgram is a program whose lowered Go reaches the runtime's
// platform-split bodies: a process run (exec), a terminal mode and size
// (term) and the terminal checks of io.
const platformProgram = `import exec
import io
import term

func main() needs Console | Ambient {
	with context { io.Stdout: io.NewHost() } {
		Console.Println("terminal \(io.Stdout.IsTerminal())")
	}
	let r = exec.NewHost().Run(exec.New("echo", []string{"hi"})) catch {
	case exec.NotFound:
		Console.Println("not found")
		return
	case exec.Denied:
		Console.Println("denied")
		return
	case exec.Failed as f:
		Console.Println("failed " + f.Reason)
		return
	}
	Console.Println(r.Stdout)
	with context { term.Terminal: term.NewHost() } {
		let w = term.Terminal.Size() catch {
		case term.NotTerminal:
			Console.Println("not a terminal")
			return
		}
		Console.Println("\(w.Cols)x\(w.Rows)")
	}
}
`

// TestRuntimeTypechecksOnEveryBuildPlatform: the runtime an emitted module
// holds (Runtime(), each package's files as go/build selects them for the
// platform) and a lowered program that calls its exec, term and io bodies
// type-check with go/types for each build platform beyond the binding
// platforms: windows/amd64, js/wasm and wasip1/wasm (gobind.BuildPlatforms).
// A runtime file that names a Unix-only declaration outside its build
// constraint (syscall.Kill, syscall.Termios, SysProcAttr.Setpgid) fails it
// for the platform it breaks.
//
// It builds Go because go/types needs the export data of Go's standard
// library for the foreign platform, which only the go command writes (go
// list -export, as typecheckGo's goExports); the runtime and the program
// are checked from source, in process, and nothing is linked.
func TestRuntimeTypechecksOnEveryBuildPlatform(t *testing.T) {
	program := lowerSource("platforms", platformProgram, true)
	if program.err != nil || len(program.diags) > 0 || program.panicked != nil || len(program.refused) > 0 {
		t.Fatalf("the program does not lower: %v %v %v %v", program.err, program.diags, program.panicked, program.refused)
	}
	checked := 0
	for _, p := range gobind.BuildPlatforms {
		if slices.Contains(gobind.Platforms, p) {
			continue
		}
		checked++
		t.Run(p.GOOS+"_"+p.GOARCH, func(t *testing.T) {
			for _, e := range typecheckOnPlatform(t, p.GOOS, p.GOARCH, program.mainGo()) {
				t.Error(e)
			}
		})
	}
	if checked == 0 {
		t.Fatal("no build platform outside the binding platforms")
	}
}

// typecheckOnPlatform type-checks the runtime packages and mainGo for
// goos/goarch and returns the type errors, each naming its file.
func typecheckOnPlatform(t *testing.T, goos, goarch, mainGo string) []string {
	t.Helper()
	ctxt := build.Default
	ctxt.GOOS, ctxt.GOARCH, ctxt.CgoEnabled = goos, goarch, false
	ctxt.BuildTags = nil
	// The runtime's packages: import path -> its files for the platform.
	sources := map[string]map[string][]byte{}
	for _, f := range Runtime() {
		dir, name := path.Split(f.Path)
		dir = strings.TrimSuffix(dir, "/")
		if !strings.HasSuffix(name, ".go") || !strings.HasPrefix(dir, runtimeDir+"/") {
			continue
		}
		pkg := runtimeModule + "/" + strings.TrimPrefix(dir, runtimeDir+"/")
		if sources[pkg] == nil {
			sources[pkg] = map[string][]byte{}
		}
		sources[pkg][name] = f.Data
	}
	fset := token.NewFileSet()
	files := map[string][]*ast.File{}
	var std []string
	addImports := func(f *ast.File) {
		for _, spec := range f.Imports {
			p := strings.Trim(spec.Path.Value, `"`)
			if !strings.HasPrefix(p, runtimeModule+"/") && !slices.Contains(std, p) {
				std = append(std, p)
			}
		}
	}
	for pkg, byName := range sources {
		names := make([]string, 0, len(byName))
		for n := range byName {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			data := byName[n]
			ctxt.OpenFile = func(string) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }
			match, err := ctxt.MatchFile("/nonexistent/loop-probe-x", n)
			if err != nil {
				t.Fatalf("%s/%s: %v", pkg, n, err)
			}
			if !match {
				continue
			}
			f, err := parser.ParseFile(fset, pkg+"/"+n, data, parser.SkipObjectResolution)
			if err != nil {
				t.Fatalf("%s/%s: %v", pkg, n, err)
			}
			files[pkg] = append(files[pkg], f)
			addImports(f)
		}
	}
	mainFile, err := parser.ParseFile(fset, "main.go", mainGo, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	addImports(mainFile)
	exports := stdExports(t, goos, goarch, std)
	gc := importer.ForCompiler(fset, "gc", func(p string) (io.ReadCloser, error) {
		file := exports[p]
		if file == "" {
			return nil, fmt.Errorf("no export data for %q on %s/%s", p, goos, goarch)
		}
		return os.Open(file)
	})
	var errs []string
	done := map[string]*types.Package{}
	sizes := types.SizesFor("gc", goarch)
	var imp importerFunc
	check := func(path string, fs []*ast.File) *types.Package {
		conf := types.Config{
			Importer:  imp,
			GoVersion: "go" + goVersion,
			Sizes:     sizes,
			Error:     func(err error) { errs = append(errs, fmt.Sprintf("%s/%s: %v", goos, goarch, err)) },
		}
		pkg, _ := conf.Check(path, fset, fs, nil)
		return pkg
	}
	imp = func(p string) (*types.Package, error) {
		if pkg, ok := done[p]; ok {
			return pkg, nil
		}
		if fs, ok := files[p]; ok {
			pkg := check(p, fs)
			done[p] = pkg
			return pkg, nil
		}
		if p == "unsafe" {
			return types.Unsafe, nil
		}
		return gc.Import(p)
	}
	for _, p := range []string{runtimeModule + "/runtime/v1"} {
		if _, err := imp(p); err != nil {
			t.Fatal(err)
		}
	}
	check(programModule+"/p/platforms", []*ast.File{mainFile})
	return errs
}

// importerFunc is a types.Importer of a function.
type importerFunc func(path string) (*types.Package, error)

func (f importerFunc) Import(path string) (*types.Package, error) { return f(path) }

// stdExports are the gc export data files of the standard-library
// packages paths and their dependencies, compiled for goos/goarch with cgo
// off: one go list -export -deps outside any module. It builds Go because
// only the go command writes export data, and go/types needs the foreign
// platform's.
func stdExports(t *testing.T, goos, goarch string, paths []string) map[string]string {
	t.Helper()
	sort.Strings(paths)
	args := append([]string{"list", "-e", "-export", "-deps", "-trimpath", "-f", "{{.ImportPath}}\t{{.Export}}"}, paths...)
	cmd := exec.Command("go", args...)
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch, "CGO_ENABLED=0", "GOWORK=off", "GOFLAGS=", "GO111MODULE=off")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -export for %s/%s: %v\n%s", goos, goarch, err, firstLines(stderr.String(), 20))
	}
	files := map[string]string{}
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		if p, file, ok := strings.Cut(scanner.Text(), "\t"); ok && file != "" {
			files[p] = file
		}
	}
	return files
}
