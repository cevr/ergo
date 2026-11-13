package sketchfront

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"maps"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/gobind"
)

// TestModuleResolutionAgrees is MD1's acceptance (grammar.md, Source files
// 1, Modules): over generated module graphs, the loader's resolution (each
// package it reads, in dependency order, and its folder) and its
// diagnostics equal a reference resolver's, which reads the generated model
// and no file. The graphs hold diamonds, two versions of one module (the
// higher wins), local replacements, import cycles across modules, imports
// from a module go.mod does not require, go.sum lines that are missing,
// copies that are missing or changed after go.sum hashed them, Go modules
// with no ergo.mod, an ergo.mod naming another module, a package a
// version does not hold, and a call of a dependency's unexported function.
// A failing seed prints its files, both outputs and the replay command.
//
// Mutants it alone catches (testdata/mutants.json, md1-*): a copy's hash
// not checked; a go.mod read for the graph not checked; a missing go.sum
// line accepted; the lower of two versions chosen; the dependencies'
// go.mod files not read; a replacement ignored; a module without ergo.mod,
// or whose ergo.mod names another module, accepted; the cache path not
// escaped. It also catches a dependency's unexported function made visible
// (md1-unexported-visible), the visibility rule across the module line.
//
// MD3 (grammar.md, Source files 1, Trusted Go) adds, from a stream of its
// own: dependency packages that use Go (import go, extern go), the root's
// trust go lines, and functions that reach the console with no row, in the
// root and in dependencies; the reference takes that diagnostic from a
// check of the function alone, so the Ambient rule is the same on both
// sides of the module line. Mutants it alone catches (md3-*): trust lines
// ignored or not read, an extern go not seen as Go, a dependency's
// diagnostics dropped.
func TestModuleResolutionAgrees(t *testing.T) {
	if len(Check(leakSource).Diagnostics) == 0 {
		t.Fatalf("a function that reaches the console with no row checks:\n%s", leakSource)
	}
	for seed := uint64(1); seed <= 160; seed++ {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			m := genModules(seed)
			dir := t.TempDir()
			root := m.write(t, dir)
			cache := filepath.Join(dir, "modcache")
			a, err := checkDirIn(hostFS{}, filepath.Join(root, "cmd"), gobind.Platform{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}, cache)
			if err != nil {
				t.Fatal(err)
			}
			var got strings.Builder
			for _, p := range a.Packages() {
				fmt.Fprintf(&got, "package %s %s\n", p.Path, m.short(dir, p.Dir))
			}
			got.WriteString(m.short(dir, diagnosticLines(a)))
			want := m.reference(dir)
			if got.String() != want {
				t.Fatalf("seed %d: the loader and the reference resolver differ\n--- files\n%s--- loader\n%s--- reference\n%s--- replay: go test -run 'TestModuleResolutionAgrees/seed=%d$' ./internal/sketchfront",
					seed, m.listing(), got.String(), want, seed)
			}
		})
	}
}

// The generated model. Module paths have an upper-case letter, so the
// cache's escaped spelling is reached.
type genPkg struct {
	imports []string // import paths, in source order
	secret  string   // an import path whose unexported function F calls
	// goUse is how the package uses Go (MD3): 0 not, 1 `import go
	// strings` called from F, 2 `extern go strings.ToUpper`.
	goUse int
	// leak declares H, which writes to the console with no row.
	leak bool
	// extra is more source after F (TestModuleSnapshotAgrees: declared
	// types, whose checked form names the checker); "" in MD1's graphs.
	extra string
}

type genVersion struct {
	requires [][2]string       // go.mod's require lines: path, version
	pkgs     map[string]genPkg // by folder name
	ergoMod  string            // ergo.mod's text; "" for a Go module
	tamper   bool              // a file changes after go.sum hashed the copy
	modEdit  bool              // the cache's go.mod changes after go.sum hashed it
}

type genModel struct {
	paths    []string // module paths
	versions []string
	mods     map[string]map[string]*genVersion
	rootReq  [][2]string
	local    map[string]*genVersion // replace path => ../local/<i>
	root     genPkg                 // app/cmd
	lib      *genPkg                // app/lib, when generated
	noSum    map[string]bool        // go.sum keys left out
	noCopy   map[string]bool        // path@version copies not written
	trust    []string               // the root ergo.mod's trust go lines
}

func genModules(seed uint64) *genModel {
	r := rand.New(rand.NewPCG(seed, 0x6d6431))
	m := &genModel{versions: []string{"v1.0.0", "v1.2.0", "v1.10.0"}, mods: map[string]map[string]*genVersion{},
		local: map[string]*genVersion{}, noSum: map[string]bool{}, noCopy: map[string]bool{}}
	n := 2 + r.IntN(3)
	for i := range n {
		m.paths = append(m.paths, fmt.Sprintf("example.com/Mod%d", i))
	}
	pkgPaths := func(self string) []string {
		var out []string
		for _, p := range m.paths {
			if p != self {
				f := folders(p)
				out = append(out, p+"/"+f[0], p+"/"+f[1])
			}
		}
		return out
	}
	pick := func(from []string, k int) []string {
		var out []string
		for range k {
			if c := from[r.IntN(len(from))]; !slices.Contains(out, c) {
				out = append(out, c)
			}
		}
		return out
	}
	content := func(path string) *genVersion {
		v := &genVersion{pkgs: map[string]genPkg{}, ergoMod: "module " + path + "\n"}
		switch r.IntN(12) {
		case 0:
			v.ergoMod = ""
		case 1:
			v.ergoMod = "module example.com/other\n"
		}
		for _, q := range m.paths {
			if q != path && r.IntN(3) == 0 {
				v.requires = append(v.requires, [2]string{q, m.versions[r.IntN(len(m.versions))]})
			}
		}
		for k, folder := range folders(path) {
			if k == 1 && r.IntN(2) == 0 {
				continue
			}
			imports := pick(pkgPaths(path), r.IntN(3))
			if r.IntN(10) == 0 {
				imports = append(imports, "app/lib")
			}
			v.pkgs[folder] = genPkg{imports: imports}
		}
		return v
	}
	for _, path := range m.paths {
		m.mods[path] = map[string]*genVersion{}
		for _, v := range m.versions {
			if r.IntN(3) > 0 || v == "v1.0.0" {
				c := content(path)
				m.mods[path][v] = c
				// A broken go.mod hides the whole graph, so it is rarer.
				switch r.IntN(40) {
				case 0, 1, 2:
					c.tamper = true
				case 3, 4:
					m.noSum[path+" "+v] = true
				case 5:
					m.noSum[path+" "+v+"/go.mod"] = true
				case 6:
					c.modEdit = true
				case 7, 8:
					m.noCopy[path+"@"+v] = true
				}
			}
		}
	}
	// A requirement names a generated version.
	known := func(path string) string {
		var vs []string
		for v := range m.mods[path] {
			vs = append(vs, v)
		}
		slices.Sort(vs)
		return vs[r.IntN(len(vs))]
	}
	for _, path := range m.paths {
		if r.IntN(5) > 0 {
			m.rootReq = append(m.rootReq, [2]string{path, known(path)})
			if r.IntN(6) == 0 {
				m.local[path] = content(path)
			}
		}
	}
	var all []*genVersion
	for _, path := range m.paths {
		if c := m.local[path]; c != nil {
			all = append(all, c)
		}
		for _, v := range m.versions {
			if c := m.mods[path][v]; c != nil {
				all = append(all, c)
			}
		}
	}
	for _, c := range all {
		for i, req := range c.requires {
			c.requires[i][1] = known(req[0])
		}
	}
	m.root = genPkg{imports: pick(pkgPaths(""), 1+r.IntN(3))}
	if r.IntN(3) == 0 {
		lib := genPkg{imports: pick(pkgPaths(""), r.IntN(2))}
		m.lib = &lib
		m.root.imports = append(m.root.imports, "app/lib")
	}
	if r.IntN(3) == 0 {
		m.root.secret = m.root.imports[0]
	}
	// MD3's draws come from their own stream, so MD1's graphs stay as
	// they were: uses of Go in dependencies, the root's trust lines, and
	// functions that reach the console with no row, on either side of
	// the module line.
	r3 := rand.New(rand.NewPCG(seed, 0x6d6433))
	mark := func(pkgs map[string]genPkg, dependency bool) {
		for _, folder := range slices.Sorted(maps.Keys(pkgs)) {
			p := pkgs[folder]
			if dependency && r3.IntN(3) == 0 {
				p.goUse = 1 + r3.IntN(2)
			}
			p.leak = r3.IntN(6) == 0
			pkgs[folder] = p
		}
	}
	for _, path := range m.paths {
		if c := m.local[path]; c != nil {
			mark(c.pkgs, true)
		}
		for _, v := range m.versions {
			if c := m.mods[path][v]; c != nil {
				mark(c.pkgs, true)
			}
		}
		if r3.IntN(2) == 0 {
			m.trust = append(m.trust, path)
		}
	}
	m.root.leak = r3.IntN(6) == 0
	if m.lib != nil {
		m.lib.leak = r3.IntN(6) == 0
	}
	return m
}

// folders are the package folders a version of module path may hold, named
// apart across modules so imports need no alias: Mod2 holds mod2a, mod2b.
func folders(path string) [2]string {
	base := strings.ToLower(path[strings.LastIndex(path, "/")+1:])
	return [2]string{base + "a", base + "b"}
}

// qualifier is the name an import path is imported under.
func qualifier(path string) string { return path[strings.LastIndex(path, "/")+1:] }

// source is a package's one file: F adds the F of each import.
func (p genPkg) source() string {
	var b strings.Builder
	for _, path := range p.imports {
		fmt.Fprintf(&b, "import %s\n", path)
	}
	if p.goUse == 1 {
		b.WriteString("import go strings\n")
	}
	b.WriteString("\nfunc F() int {\n\treturn 1")
	for _, path := range p.imports {
		if path == p.secret {
			fmt.Fprintf(&b, " + %s.f()", qualifier(path))
		} else {
			fmt.Fprintf(&b, " + %s.F()", qualifier(path))
		}
	}
	if p.goUse == 1 {
		b.WriteString(" + strings.Count(\"a\", \"a\")")
	}
	b.WriteString("\n}\n\nfunc f() int {\n\treturn 0\n}\n")
	if p.leak {
		b.WriteString("\n" + leakSource)
	}
	b.WriteString(p.extra)
	if p.goUse == 2 {
		b.WriteString("\nextern go strings.ToUpper func Upper(s string) string\n")
	}
	return b.String()
}

// leakSource reaches the console with no row: the Ambient rule's
// diagnostic, whose text and position the reference takes from a check of
// this one text alone (rows.md, Capability safety).
const leakSource = "func H() {\n\tConsole.Println(\"\")\n}\n"

// lineOf is the 1-based line of src that starts with prefix; 0 for none.
func lineOf(src, prefix string) int {
	for i, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(line, prefix) {
			return i + 1
		}
	}
	return 0
}

func (v *genVersion) files(path string) map[string]string {
	files := map[string]string{"go.mod": v.goMod(path)}
	if v.ergoMod != "" {
		files["ergo.mod"] = v.ergoMod
	}
	for folder, p := range v.pkgs {
		files[folder+"/x.ergo"] = p.source()
	}
	return files
}

func (v *genVersion) goMod(path string) string {
	text := "module " + path + "\n\ngo 1.27\n"
	for _, req := range v.requires {
		text += "\nrequire " + req[0] + " " + req[1] + "\n"
	}
	return text
}

// refHash is the h1 hash of files, each named prefix/name, spelled apart
// from the loader's: one summary string, then one SHA-256.
func refHash(prefix string, files map[string]string) string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	summary := ""
	for _, name := range names {
		summary += fmt.Sprintf("%x  %s\n", sha256.Sum256([]byte(files[name])), prefix+name)
	}
	sum := sha256.Sum256([]byte(summary))
	return "h1:" + base64.StdEncoding.EncodeToString(sum[:])
}

func escapeRef(s string) string {
	out := ""
	for _, r := range s {
		if r >= 'A' && r <= 'Z' {
			out += "!" + string(r+32)
		} else {
			out += string(r)
		}
	}
	return out
}

// tampered is the copy's files after the change go.sum does not know.
func tampered(files map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range files {
		out[k] = v
	}
	out["go.mod"] += "// changed\n"
	return out
}

// write writes the root module under dir/app, the copies and go.mod files
// under dir/modcache, and the replacements under dir/local; it returns the
// root.
func (m *genModel) write(t *testing.T, dir string) string {
	t.Helper()
	put := func(name, text string) {
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var sum strings.Builder
	for _, path := range m.paths {
		for _, v := range m.versions {
			c := m.mods[path][v]
			if c == nil {
				continue
			}
			files := c.files(path)
			if !m.noSum[path+" "+v] {
				fmt.Fprintf(&sum, "%s %s %s\n", path, v, refHash(path+"@"+v+"/", files))
			}
			if !m.noSum[path+" "+v+"/go.mod"] {
				fmt.Fprintf(&sum, "%s %s/go.mod %s\n", path, v, refHash("", map[string]string{"go.mod": files["go.mod"]}))
			}
			mod := files["go.mod"]
			if c.modEdit {
				mod = tampered(files)["go.mod"]
			}
			put(filepath.Join(dir, "modcache", "cache", "download", escapeRef(path), "@v", v+".mod"), mod)
			if m.noCopy[path+"@"+v] {
				continue
			}
			if c.tamper {
				files = tampered(files)
			}
			for name, text := range files {
				put(filepath.Join(dir, "modcache", escapeRef(path)+"@"+v, filepath.FromSlash(name)), text)
			}
		}
	}
	root := filepath.Join(dir, "app")
	goMod := "module app\n\ngo 1.27\n\nrequire (\n"
	for _, req := range m.rootReq {
		goMod += "\t" + req[0] + " " + req[1] + "\n"
	}
	goMod += ")\n"
	for i, path := range m.paths {
		if c := m.local[path]; c != nil {
			goMod += fmt.Sprintf("\nreplace %s => ../local/%d\n", path, i)
			for name, text := range c.files(path) {
				put(filepath.Join(dir, "local", fmt.Sprint(i), filepath.FromSlash(name)), text)
			}
		}
	}
	ergoMod := "module app\n"
	for _, path := range m.trust {
		ergoMod += "trust go " + path + "\n"
	}
	put(filepath.Join(root, "ergo.mod"), ergoMod)
	put(filepath.Join(root, "go.mod"), goMod)
	put(filepath.Join(root, "go.sum"), sum.String())
	put(filepath.Join(root, "cmd", "x.ergo"), m.root.source())
	if m.lib != nil {
		put(filepath.Join(root, "lib", "x.ergo"), m.lib.source())
	}
	return root
}

// short names the test's temporary folder $T.
func (m *genModel) short(dir, s string) string { return strings.ReplaceAll(s, dir, "$T") }

func (m *genModel) listing() string {
	var b strings.Builder
	fmt.Fprintf(&b, "root requires %v, replaces %d, lib %v, trusts %v\n== app/cmd/x.ergo\n%s", m.rootReq, len(m.local), m.lib != nil, m.trust, m.root.source())
	if m.lib != nil {
		fmt.Fprintf(&b, "== app/lib/x.ergo\n%s", m.lib.source())
	}
	for _, path := range m.paths {
		for _, v := range m.versions {
			if c := m.mods[path][v]; c != nil {
				fmt.Fprintf(&b, "== %s@%s ergo.mod %q tamper %v nosum %v/%v nocopy %v\n%s", path, v, c.ergoMod, c.tamper,
					m.noSum[path+" "+v], m.noSum[path+" "+v+"/go.mod"], m.noCopy[path+"@"+v], c.goMod(path))
				for _, folder := range folders(path) {
					if p, ok := c.pkgs[folder]; ok {
						fmt.Fprintf(&b, "-- %s\n%s", folder, p.source())
					}
				}
			}
		}
	}
	return b.String()
}

// refPkg is a package the reference reads.
type refPkg struct {
	path, dir, file string // file: its x.ergo as diagnostics name it
	pkg             genPkg
	mod, name       string // its required module's path and path@version; "" for app's
}

// reference resolves the model as grammar.md's Modules rule says, with the
// loader's order: packages depth first in import order, a cycle at the
// import that closes it (then nothing is checked), else each package
// checked after its imports, a package with an unresolved import reporting
// it there, and a package that imports a broken one not checked.
func (m *genModel) reference(dir string) string {
	cache := "$T/modcache"
	leak := Check(leakSource).Diagnostics
	req := map[string]string{}
	for _, r := range m.rootReq {
		req[r[0]] = r[1]
	}
	graphErr, graphRead := "", false
	selected := map[string]string{}
	readGraph := func() {
		if graphRead {
			return
		}
		graphRead = true
		for p, v := range req {
			selected[p] = v
		}
		var paths []string
		for p := range req {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for _, p := range paths {
			c := m.local[p]
			if c == nil {
				if m.noSum[p+" "+req[p]+"/go.mod"] {
					graphErr = fmt.Sprintf("go.sum has no hash of %s %s/go.mod; run ergo get %s@%s", p, req[p], p, req[p])
					return
				}
				c = m.mods[p][req[p]]
				if c.modEdit {
					text := c.goMod(p)
					graphErr = fmt.Sprintf("the module cache's go.mod of %s %s does not match go.sum (go.sum holds %s, the copy hashes %s); go mod verify reports it",
						p, req[p], refHash("", map[string]string{"go.mod": text}), refHash("", map[string]string{"go.mod": text + "// changed\n"}))
					return
				}
			}
			for _, q := range c.requires {
				if v, ok := selected[q[0]]; ok && compareRef(q[1], v) > 0 {
					selected[q[0]] = q[1]
				}
			}
		}
	}
	// resolve is an import path's package, or the reason it has none.
	resolve := func(path string) (refPkg, string) {
		if path == "app/lib" {
			if m.lib == nil {
				return refPkg{}, "no folder lib in module app"
			}
			return refPkg{path: path, dir: "$T/app/lib", file: "lib/x.ergo", pkg: *m.lib}, ""
		}
		cut := strings.LastIndex(path, "/")
		mod, folder := path[:cut], path[cut+1:]
		if _, ok := req[mod]; !ok {
			return refPkg{}, "it is not std, not in module app, and in no module that go.mod requires; require the module that holds it: ergo get MODULE@VERSION"
		}
		readGraph()
		if graphErr != "" {
			return refPkg{}, "the module graph of go.mod cannot be read: " + graphErr
		}
		v := selected[mod]
		name := mod + "@" + v
		var c *genVersion
		var root string
		if l := m.local[mod]; l != nil {
			c, root = l, "$T/local/"+fmt.Sprint(slices.Index(m.paths, mod))
		} else {
			c, root = m.mods[mod][v], cache+"/"+escapeRef(mod)+"@"+v
			switch {
			case m.noSum[mod+" "+v]:
				return refPkg{}, fmt.Sprintf("go.sum has no hash of %s %s; run ergo get %s", mod, v, name)
			case m.noCopy[name]:
				return refPkg{}, fmt.Sprintf("%s %s is not in the module cache (no %s); run ergo get %s", mod, v, root, name)
			case c.tamper:
				files := c.files(mod)
				return refPkg{}, fmt.Sprintf("the module cache's copy of %s %s does not match go.sum (go.sum holds %s, the copy hashes %s); go mod verify reports it",
					mod, v, refHash(name+"/", files), refHash(name+"/", tampered(files)))
			}
		}
		switch c.ergoMod {
		case "":
			return refPkg{}, fmt.Sprintf("module %s is a Go module, not an Ergo module: its root holds no ergo.mod", name)
		case "module example.com/other\n":
			return refPkg{}, fmt.Sprintf("the ergo.mod of module %s names module example.com/other, not %s", name, mod)
		}
		p, ok := c.pkgs[folder]
		if !ok {
			return refPkg{}, fmt.Sprintf("no folder %s in module %s", folder, name)
		}
		return refPkg{path: path, dir: root + "/" + folder, file: name + "/" + folder + "/x.ergo", pkg: p, mod: mod, name: name}, ""
	}

	var out strings.Builder
	var diags []string
	at := func(p refPkg, i int) string { // the import's path position
		return fmt.Sprintf("%d:%d", i+1, len("import ")+1)
	}
	state, pkgs, missing := map[string]int{}, map[string]refPkg{}, map[string]string{}
	broken := map[string]bool{}
	var order []refPkg
	var visit func(p refPkg, stack []string)
	visit = func(p refPkg, stack []string) {
		state[p.path] = visiting
		stack = append(stack, p.path)
		for i, path := range p.pkg.imports {
			q, ok := pkgs[path]
			if !ok {
				if _, failed := missing[path]; failed {
					continue
				}
				var why string
				if q, why = resolve(path); why != "" {
					missing[path] = why
					continue
				}
				pkgs[path] = q
				if q.mod != "" && q.pkg.goUse > 0 && !slices.Contains(m.trust, q.mod) {
					src := q.pkg.source()
					use, line, col := "import go strings", lineOf(src, "import go"), len("import ")+1
					if q.pkg.goUse == 2 {
						use, line, col = "extern go strings.ToUpper", lineOf(src, "extern go"), 1
					}
					diags = append(diags, fmt.Sprintf("%s S108 %d:%d module %s uses Go (%s), and the ergo.mod of module app does not trust its Go: Ergo does not check Go code; after review, add the line \"trust go %s\" to ergo.mod",
						q.file, line, col, q.name, use, q.mod))
					broken[q.path] = true
				}
			}
			switch state[path] {
			case visiting:
				cycle := append(slices.Clone(stack[slices.Index(stack, path):]), path)
				diags = append(diags, fmt.Sprintf("%s S108 %s import cycle: %s", p.file, at(p, i), strings.Join(cycle, " imports ")))
				broken[p.path] = true
			case unvisited:
				visit(q, stack)
			}
		}
		state[p.path] = visited
		order = append(order, p)
	}
	visit(refPkg{path: "app/cmd", dir: "$T/app/cmd", file: "cmd/x.ergo", pkg: m.root}, nil)
	if len(diags) == 0 {
		for _, p := range order {
			for _, path := range p.pkg.imports {
				if broken[path] {
					broken[p.path] = true
				}
			}
			if broken[p.path] {
				continue
			}
			for i, path := range p.pkg.imports {
				if why, ok := missing[path]; ok {
					diags = append(diags, fmt.Sprintf("%s S104 %s unknown package %q: %s", p.file, at(p, i), path, why))
					broken[p.path] = true
				}
			}
			if _, failed := missing[p.pkg.secret]; p.pkg.secret != "" && !failed {
				i := slices.Index(p.pkg.imports, p.pkg.secret)
				col := len("\treturn 1") + 1
				for _, path := range p.pkg.imports[:i] {
					col += len(" + " + qualifier(path) + ".F()")
				}
				q := qualifier(p.pkg.secret)
				col += len(" + " + q + ".")
				diags = append(diags, fmt.Sprintf("%s S104 %d:%d %s.f is not an exported declaration of package %s", p.file, lineOf(p.pkg.source(), "\treturn 1"), col, q, p.pkg.secret))
				broken[p.path] = true
			}
			if p.pkg.leak {
				for _, d := range leak {
					line := lineOf(p.pkg.source(), "func H") + d.Span.Line - 1
					diags = append(diags, fmt.Sprintf("%s %s %d:%d %s", p.file, d.Code, line, d.Span.Column, d.Message))
				}
				broken[p.path] = true
			}
		}
	}
	for _, p := range order {
		fmt.Fprintf(&out, "package %s %s\n", p.path, p.dir)
	}
	for _, d := range diags {
		out.WriteString(d + "\n")
	}
	return out.String()
}

// compareRef orders the model's versions, v1.N.0, by N.
func compareRef(a, b string) int {
	var x, y int
	fmt.Sscanf(a, "v1.%d.0", &x)
	fmt.Sscanf(b, "v1.%d.0", &y)
	return x - y
}
