package sketchfront

import (
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestEmittedGoModMergesGoMod holds the emitted module's go.mod to one rule
// for every required module (lower.MergeGoModules; the G1 bug: a go.mod
// that requires ergo.local/prototype made the emitted go.mod replace it
// twice, and go build refused it). For seeded modules beside ergo.mod
// (require and replace in every combination, local directories relative,
// ./ inside the module and absolute, the same module required or replaced
// twice, the module's own path under or equal to the runtime's, and
// ergo.local/prototype required and replaced by a directory that holds the
// runtime's bytes or other ones, or by this repository, whose package
// ergo.local/prototype/runtime the copied tree does not hold: the bug's
// repro, kind "compiler repository") the reference is the generator's own
// record: an admitted module checks with no diagnostic, and the go command
// resolves every package of the emitted module, the packages only each
// replacement holds included; a conflicting one is one go.mod diagnostic,
// at the later go.mod line, that names both sources. A failing seed prints
// the go.mod, the expectation and the diagnostics or the go command's
// error; ERGO_GOMODMERGE_SEED replays one seed.
//
// Mutants it alone catches (the gomod-* entries of testdata/mutants.json):
// the runtime's replacement kept beside a directory that holds the same
// bytes (go: conflicting replacements, the bug); a directory that holds
// other runtime bytes admitted; two versions of one module both required;
// an absolute replacement joined to the module root; a version-less
// replacement not applied to the required version; the exact replacement
// of the required version not preferred over the version-less one; the
// main module's path admitted as a requirement; the conflict reported at
// the earlier line; the runtime's go.mod compared with the replacement's.
//
// It runs go list, which resolves modules and packages and builds nothing:
// whether go accepts an emitted go.mod is the go command's question, and
// no in-process check answers it.
func TestEmittedGoModMergesGoMod(t *testing.T) {
	seeds := 48
	first := uint64(1)
	if s := os.Getenv("ERGO_GOMODMERGE_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		first, seeds = n, 1
	}
	var runtime []lower.File
	for _, f := range lower.Runtime() {
		if rel, ok := strings.CutPrefix(f.Path, "ergo/"); ok && rel != "go.mod" {
			runtime = append(runtime, lower.File{Path: rel, Data: f.Data})
		}
	}
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for seed := first; seed < first+uint64(seeds); seed++ {
		c := genMergeCase(rand.New(rand.NewPCG(seed, 0x6e7)), runtime)
		kinds[c.kind] = true
		replay := fmt.Sprintf("seed %d (replay: ERGO_GOMODMERGE_SEED=%d), %s:\n--- go.mod:\n%s", seed, seed, c.kind, c.files["app/go.mod"])
		dir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		paths := strings.NewReplacer("$ROOT", dir, "$REPO", repo)
		for name, text := range c.files {
			name = filepath.Join(dir, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(name, []byte(paths.Replace(text)), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		a, err := CheckDir(filepath.Join(dir, "app", "cmd", "app"))
		if err != nil {
			t.Fatalf("%s\n%v", replay, err)
		}
		if c.conflict[0] != "" {
			matched := false
			for _, d := range a.Diagnostics {
				if d.File == GoModFile && d.Span.Line == c.line && strings.Contains(d.Message, c.conflict[0]) && strings.Contains(d.Message, c.conflict[1]) {
					matched = true
				}
			}
			if !matched || len(a.Diagnostics) != 1 {
				t.Fatalf("%s--- want one go.mod diagnostic at line %d naming %q and %q; got:\n%s", replay, c.line, c.conflict[0], c.conflict[1], diagnosticLines(a))
			}
			continue
		}
		if len(a.Diagnostics) > 0 {
			t.Fatalf("%s--- want no diagnostic; got:\n%s", replay, diagnosticLines(a))
		}
		files, err := lower.Lower(a.program.unit())
		if err != nil {
			t.Fatalf("%s\n%v", replay, err)
		}
		if err := resolveEmitted(t, filepath.Join(dir, "out"), files, c.imports); err != nil {
			t.Fatalf("%s%v", replay, err)
		}
	}
	if seeds > 1 {
		for _, k := range mergeKinds {
			if !kinds[k] {
				t.Errorf("no seed reached %s", k)
			}
		}
	}
}

// resolveEmitted writes the emitted module's files and the runtime into
// out, with a file that imports each of imports, and returns the go
// command's error, with the emitted go.mod, when go list cannot resolve
// every package of the module.
func resolveEmitted(t *testing.T, out string, files []lower.File, imports []string) error {
	t.Helper()
	deps := "package main\n\nimport (\n"
	for _, p := range imports {
		deps += "\t_ " + strconv.Quote(p) + "\n"
	}
	files = append(files, lower.File{Path: "deps.go", Data: []byte(deps + ")\n")})
	for _, f := range append(files, lower.Runtime()...) {
		name := filepath.Join(out, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, f.Data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("go", "list", "-mod=readonly", "-deps", ".")
	cmd.Dir = out
	cmd.Env = append(os.Environ(), "GOFLAGS=", "GOWORK=off", "GOPROXY=off", "GOTOOLCHAIN=local")
	if msg, err := cmd.CombinedOutput(); err != nil {
		gomod, _ := os.ReadFile(filepath.Join(out, "go.mod"))
		lines := strings.Split(strings.TrimRight(string(msg), "\n"), "\n")
		return fmt.Errorf("--- emitted go.mod:\n%s--- go list: %v\n%s", gomod, err, strings.Join(lines[max(0, len(lines)-12):], "\n"))
	}
	return nil
}

// mergeCase is one generated module: its files under the seed's folder
// ($ROOT stands for that folder), the Go packages the emitted module must
// resolve, and the conflict expected, if any: two texts its diagnostic
// names and the go.mod line it is at.
type mergeCase struct {
	kind     string
	files    map[string]string
	imports  []string
	conflict [2]string
	line     int
}

// mergeKinds are the shapes genMergeCase picks from; each seed run of the
// property reaches each.
var mergeKinds = []string{
	"admitted", "admitted runtime directory", "admitted own runtime module", "compiler repository",
	"two versions", "two replacements", "other runtime version", "other runtime bytes", "main module path",
}

// genMergeCase writes an Ergo module app beside the Go modules it requires.
func genMergeCase(r *rand.Rand, runtime []lower.File) mergeCase {
	c := mergeCase{kind: mergeKinds[r.IntN(len(mergeKinds))], files: map[string]string{
		"app/ergo.mod":          "module app\n",
		"app/cmd/app/main.ergo": "func main() needs Console {\n\tConsole.Println(\"hi\")\n}\n",
	}}
	var lines []string
	add := func(format string, args ...any) int {
		lines = append(lines, fmt.Sprintf(format, args...))
		return len(lines)
	}
	label := func(line int) string { return fmt.Sprintf("go.mod:%d ", line) }
	const runtimeLabel = "the Ergo runtime (ergo/) "
	own := "example.com/app"
	switch {
	case c.kind == "main module path":
		own = "ergo.local/program"
	case c.kind == "admitted own runtime module":
		own = "ergo.local/prototype"
	case c.kind == "compiler repository":
		// A module path under the repository's would make the go command
		// look for the module's packages in the repository too, outside
		// this test's closure (internal/closure); the other shapes put the
		// path there.
	case r.IntN(3) == 0:
		own = "ergo.local/prototype/examples/app"
	}
	moduleLine := add("module %s", quoteMaybe(r, own))
	add("")
	add("go 1.27")
	// prototype writes a directory that holds the runtime, other bytes of
	// it when other is set, and a package only it holds.
	prototype := func(dir string, other bool) {
		edit := r.IntN(len(runtime))
		for i, f := range runtime {
			data := string(f.Data)
			if other && i == edit {
				data += "\n// another revision\n"
			}
			c.files[dir+"/"+f.Path] = data
		}
		c.files[dir+"/extra/extra.go"] = "package extra\n"
		c.imports = append(c.imports, "ergo.local/prototype/extra")
	}
	if own == "ergo.local/prototype" {
		prototype("app", false)
	}
	n := 1 + r.IntN(3)
	for i := range n {
		mod := fmt.Sprintf("example.com/m%d", i)
		version := []string{"v0.0.0", "v1.2.0"}[r.IntN(2)]
		dir := [][2]string{
			{"mods/m" + strconv.Itoa(i), "../mods/m" + strconv.Itoa(i)},
			{"app/local/m" + strconv.Itoa(i), "./local/m" + strconv.Itoa(i)},
			{"mods/m" + strconv.Itoa(i), "$ROOT/mods/m" + strconv.Itoa(i)},
		}[r.IntN(3)]
		c.files[dir[0]+"/go.mod"] = "module " + mod + "\n\ngo 1.27\n"
		c.files[dir[0]+"/p/p.go"] = "package p\n"
		c.imports = append(c.imports, mod+"/p")
		req := add("require %s %s", quoteMaybe(r, mod), version)
		if r.IntN(4) == 0 {
			add("require %s %s // the same again", mod, version)
		}
		old := mod
		if r.IntN(2) == 0 {
			old += " " + version
		}
		rep := add("replace %s => %s", old, quoteMaybe(r, dir[1]))
		switch r.IntN(4) {
		case 0:
			// the same directory again, by another form when it is outside
			if strings.HasPrefix(dir[0], "mods/") {
				add("replace %s => %s", old, []string{"../" + dir[0], "$ROOT/" + dir[0]}[r.IntN(2)])
			} else {
				add("replace %s => %s", old, dir[1])
			}
		case 1:
			// a replacement of another version, or of every version beside
			// this exact one: go build applies the exact one
			if old == mod {
				add("replace %s v1.9.9 => ../never", mod)
			} else {
				add("replace %s => ../every", mod)
			}
		}
		if i == 0 {
			switch c.kind {
			case "two versions":
				again := add("require %s v1.3.0", mod)
				c.conflict, c.line = [2]string{label(req), label(again)}, again
			case "two replacements":
				again := add("replace %s => ../elsewhere", old)
				c.conflict, c.line = [2]string{label(rep), label(again)}, again
			}
		}
	}
	switch c.kind {
	case "admitted runtime directory", "other runtime version", "other runtime bytes":
		version := "v0.0.0"
		if c.kind == "other runtime version" {
			version = "v0.1.0"
		}
		req := add("require ergo.local/prototype %s", version)
		old := "ergo.local/prototype"
		if r.IntN(2) == 0 {
			old += " " + version
			if r.IntN(2) == 0 && c.kind == "admitted runtime directory" {
				// go build applies the exact replacement below, not this one
				add("replace ergo.local/prototype => ../every")
			}
		}
		target := []string{"../proto", "$ROOT/proto", "./proto"}[r.IntN(3)]
		dir := "proto"
		if target == "./proto" {
			dir = "app/proto"
		}
		rep := add("replace %s => %s", old, target)
		prototype(dir, c.kind == "other runtime bytes")
		c.files[dir+"/go.mod"] = "module ergo.local/prototype\n\ngo 1.27\n"
		switch c.kind {
		case "other runtime version":
			c.conflict, c.line = [2]string{runtimeLabel, label(req)}, req
		case "other runtime bytes":
			c.conflict, c.line = [2]string{runtimeLabel, label(rep)}, rep
		}
	case "compiler repository":
		// the G1 repro: this repository serves the runtime, and a package
		// that only the repository holds (the runtime's embed) resolves
		add("require ergo.local/prototype v0.0.0")
		add("replace ergo.local/prototype => %s", quoteMaybe(r, "$REPO"))
		c.imports = append(c.imports, "ergo.local/prototype/runtime")
	case "main module path":
		c.conflict, c.line = [2]string{fmt.Sprintf("go.mod:%d (its module) ", moduleLine), "emitted main module"}, moduleLine
	}
	c.files["app/go.mod"] = strings.Join(lines, "\n") + "\n"
	return c
}

// quoteMaybe is w, quoted one time in four.
func quoteMaybe(r *rand.Rand, w string) string {
	if r.IntN(4) == 0 {
		return strconv.Quote(w)
	}
	return w
}
