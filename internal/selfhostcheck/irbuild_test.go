package selfhostcheck

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"ergo.local/prototype/internal/experimental/irspike"
	"ergo.local/prototype/internal/proclimit"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/sketchfront"
	"ergo.local/prototype/internal/testwait"
)

// TestCompilerIRAgreesWithBuilder holds the IR builder written in Ergo
// (compiler/lower, capstone S4c) to the Go IR builder (internal/sketch/
// lower): every function compiler/lower builds over the corpus (each
// folder of the examples and of std, typed by compiler/check as
// TestCompilerExprAgreesWithChecker types them) must be, byte for byte,
// the function the Go builder writes at the same declaration. The Go side
// is every example program lowered with its own package functions
// seeded (lower.LowerIRSplitReaching), the test build of each example
// package no program imports, and one empty program that reaches every
// std function, as the IR census reaches them. The Go builder lowers what
// the Ergo side can build. A body the programs lower differently agrees
// with one of them. If a production root has no Go body at the
// declaration, its test build can supply one: production can prune a
// declaration reachable only from tests. This never replaces a Go body
// that was already indexed. A test build a package relies on but the Go
// lowerer refuses is logged and its functions are not compared. The two
// sides' file indices are mapped to the files' paths first; the bytes are otherwise the IR file's
// (ir.WriteFunc against encoding/json).
//
// compiler/lower stops at a function it cannot build yet: an expression
// check/expr left untyped, or a form or a decider it has not ported (the
// S4c plan's slices). The reach is logged by cause, never pinned: it
// grows as compiler/check types more and compiler/lower ports more, with
// no change here. Every emission is still compared, but equal reach
// counts distinct Go identities (source position and split-normalized
// name), not repeated emissions. A function built where the Go builder
// wrote no IR body (it left the body to the emitter) fails:
// the Ergo builder builds only what the Go builder builds. A Go function
// is named per declaration and package (NAME-LOCAL, lower's pkgnames.go):
// bare in a build's root package, alias__name in any other, so irdump is
// told the roots (-roots). A Go function whose name differs only by the
// split's X export (irSplitName) is compared under its unexported name;
// that export is the program's, not the function's (NL-3).
//
// Set ERGO_IR_AGREE_STOPS to a receipt path to save this run's raw stop
// lines (including duplicate sites), even if IR comparisons differ.
// Set ERGO_IR_AGREE_MATCHES to a separate path outside the repository
// for the byte-equal emissions: F PATH OFFSET split-normalized-symbol.
// Both receipts are in irdump order and retain repeat emissions.
//
// It builds compiler/cmd/irdump and runs Go binaries because the Ergo
// side is a whole compiled program over the corpus (as the other
// agreement tests of this package); full tier only.
//
// Mutants it alone catches: testdata/mutants.json, rows lower-*.
func TestCompilerIRAgreesWithBuilder(t *testing.T) {
	needsBuild(t)
	phases := newIRPhases(t)
	witnesses := readIRWitnesses(t)
	if os.Getenv("ERGO_IR_AGREE_SLICE") != "" {
		for k := range witnesses {
			if len(irSlice([]string{filepath.Dir(k.path)})) == 0 {
				delete(witnesses, k)
			}
		}
	}
	required := len(witnesses)
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	bin := filepath.Join(t.TempDir(), "irdump")
	phases.next("cli-build")
	cli := ergoCLI(t, ctx)
	limit, err := proclimit.Limit(os.Environ(), proclimit.DefaultLimit)
	if err != nil {
		t.Fatal(err)
	}
	requests := make(chan irDumpRequest, 1)
	sent := false
	defer func() {
		if !sent {
			close(requests)
		}
	}()
	dump := startIRDump(ctx, cli, bin, limit, requests)
	phases.next("go-load")
	importFixture := irImportFixtureDirs(t, filepath.Join(repoRoot(), "internal", "selfhostcheck", "testdata", "bind", "port_calls_import"))
	importFixture = append(importFixture, irImportFixtureDirs(t, filepath.Join(repoRoot(), "internal", "selfhostcheck", "testdata", "bind", "port_shapes_import"), "leaf")...)
	plan := goIRPrograms(t, importFixture)
	rootDirs, testDirs := plan.roots, plan.tests

	// The Ergo side: exprdump's manifest, one line per corpus folder.
	phases.next("go-check")
	tmp := t.TempDir()
	dirs := append(irSlice(corpusDirs(t)), importFixture...)
	type side struct {
		ok           bool
		path         string
		ext, foreign []string
	}
	sides := make([]side, len(dirs))
	eachGoPackage(dirs, func(i int, pkg *check.Package) {
		_, ext := goKinds(pkg)
		_, foreign := goTypes(pkg)
		sides[i] = side{ok: true, path: importPath(dirs[i], pkg), ext: ext, foreign: foreign}
	})
	var manifest, roots, tests []string
	for i, s := range sides {
		if !s.ok {
			continue
		}
		if rootDirs[dirs[i]] {
			roots = append(roots, s.path)
		}
		if testDirs[dirs[i]] {
			tests = append(tests, s.path)
		}
		e, f := filepath.Join(tmp, fmt.Sprintf("ext%d", i)), filepath.Join(tmp, fmt.Sprintf("foreign%d", i))
		writeLines(t, e, s.ext)
		writeLines(t, f, s.foreign)
		manifest = append(manifest, strings.Join(append([]string{s.path, e, f}, ergoFiles(t, dirs[i])...), " "))
	}
	m := filepath.Join(tmp, "manifest")
	writeLines(t, m, manifest)
	rf := filepath.Join(tmp, "roots")
	writeLines(t, rf, roots)
	tf := filepath.Join(tmp, "tests")
	writeLines(t, tf, tests)
	requests <- irDumpRequest{args: []string{"-std", stdDir(), "-roots", rf, "-tests", tf, "-corpus", m}, key: irDumpKey(t, manifest, roots, tests), tmp: tmp}
	sent = true

	phases.next("go-lower")
	paths := irPathIDs{}
	goFuncs := goIRFuncs(t, paths, plan)
	unlowered, testOracle := plan.unlowered, plan.testOracle
	for dir, err := range unlowered {
		t.Logf("%s: the Go builder does not lower its test build (its functions are not compared): %s", rel(dir), err)
	}
	for i := len(dirs) - len(importFixture); i < len(dirs); i++ {
		if !sides[i].ok || unlowered[dirs[i]] != "" {
			t.Fatalf("imported-call IR fixture did not check and lower: %s (%s)", rel(dirs[i]), unlowered[dirs[i]])
		}
	}

	reach := map[string]int{}
	equal := map[irIdentity]struct{}{}
	equalEvents, differEvents := 0, 0
	const goSkipped = "built, its package's test build the Go builder does not lower"
	stopPath := os.Getenv("ERGO_IR_AGREE_STOPS")
	var stopLines strings.Builder
	matchPath := os.Getenv("ERGO_IR_AGREE_MATCHES")
	var matchLines *strings.Builder
	if matchPath != "" {
		matchLines = new(strings.Builder)
	}
	var files []string
	phases.next("irdump-wait")
	r := <-dump
	if r.err != nil {
		t.Fatal(r.err)
	}
	dumped := r.lines
	if r.stored {
		t.Logf("irdump: its answer read from the store (%s)", irAgreeCacheVariable)
	} else {
		t.Logf("irdump itself: wall %.1fs cpu %.1fs, overlapped with go-lower", r.wall.Seconds(), r.cpu.Seconds())
	}
	t.Logf("go-lower: %d of %d programs read from the store", irLowerCache.hitCount(), len(plan.programs))
	phases.next("compare")
	for _, l := range dumped {
		kind, rest, _ := strings.Cut(l, " ")
		switch kind {
		case "@pkg":
			files = nil
		case "P":
			_, path, _ := strings.Cut(rest, " ")
			files = append(files, path)
		case "S":
			if stopPath != "" {
				stopLines.WriteString(l)
				stopLines.WriteByte('\n')
			}
			f := strings.SplitN(rest, " ", 3)
			if len(f) == 3 {
				reach["stop: "+f[2]]++
			}
		case "F":
			f := strings.SplitN(rest, " ", 3)
			if len(f) != 3 {
				t.Fatalf("irdump line %q", l)
			}
			var fn irFunc
			if err := json.Unmarshal([]byte(f[2]), &fn); err != nil {
				t.Fatalf("%s:%s: irdump wrote no IR function: %v\n%s", rel(f[0]), f[1], err, f[2])
			}
			if err := irMapFiles(&fn, files, paths); err != nil {
				t.Fatalf("%s:%s: %v", rel(f[0]), f[1], err)
			}
			got, err := json.Marshal(&fn)
			if err != nil {
				t.Fatal(err)
			}
			key := irKey{file: fn.Pos[0], line: fn.Pos[1], col: fn.Pos[2]}
			site := irWitnessSite{f[0], f[1], fn.Name}
			testOracle.fill(t, paths, goFuncs, key, site.path)
			goUnavailable := unlowered[filepath.Dir(f[0])] != "" || testOracle.unavailable[filepath.Dir(f[0])] != ""
			comparison := goFuncs.compare(key, site, got, goUnavailable, matchLines)
			if comparison.skipped {
				reach[goSkipped]++
				continue
			}
			if comparison.cause != "" {
				differEvents++
				if differEvents <= 10 {
					t.Errorf("%s:%s %s: compiler/lower builds a body where the Go builder writes none (%s; Go names there: %s)\n ergo: %s", rel(f[0]), f[1], fn.Name, comparison.cause, goFuncs.names(key), got)
				}
				continue
			}
			wants, i := comparison.wants, comparison.match
			if i < 0 {
				differEvents++
				if differEvents <= 10 {
					t.Errorf("%s:%s %s: compiler/lower builds another IR body\n ergo: %s\n   go: %s", rel(f[0]), f[1], fn.Name, got, wants[0].data)
				}
				continue
			}
			if wants[i].raw != fn.Name {
				reach["built, the Go name exported by the split (X)"]++
			}
			if len(wants) > 1 {
				reach["built, one of the Go bodies the programs differ in"]++
			}
			equal[irIdentity{key, wants[i].name}] = struct{}{}
			equalEvents++
			delete(witnesses, irWitnessSite{filepath.Clean(f[0]), f[1], wants[i].name})
		}
	}
	phases.next("report")
	if stopPath != "" {
		if err := os.WriteFile(stopPath, []byte(stopLines.String()), 0o644); err != nil {
			t.Fatalf("write IR agreement stops: %v", err)
		}
		t.Logf("raw IR stop lines: %s", stopPath)
	}
	if matchLines != nil {
		if err := os.WriteFile(matchPath, []byte(matchLines.String()), 0o644); err != nil {
			t.Logf("could not write IR agreement matches: %v", err)
		} else {
			t.Logf("raw IR match lines: %s", matchPath)
		}
	}
	var lines []string
	for cause, n := range reach {
		lines = append(lines, fmt.Sprintf("%7d %s", n, cause))
	}
	sort.Strings(lines)
	t.Logf("compiler/lower built %d distinct functions equal to the Go builder's (%d equal comparison events); %d comparison events differ:\n%s", len(equal), equalEvents, differEvents, strings.Join(lines, "\n"))
	t.Logf("Go comparison corpus: %d distinct IR function identities (source position and split-normalized name)", goFuncs.functionCount())
	t.Logf("Go comparison skipped: %d function emissions (Go did not lower their package)", reach[goSkipped])
	t.Logf("IR witnesses: %d/%d reached matching bodies", required-len(witnesses), required)
	phases.next("done")
	var missing []string
	for k, group := range witnesses {
		missing = append(missing, fmt.Sprintf("%s: %s:%s %s did not reach a matching IR body", group, rel(k.path), k.offset, k.symbol))
	}
	slices.Sort(missing)
	for _, msg := range missing {
		t.Error(msg)
	}
	if differEvents > 0 {
		t.Fatalf("%d comparison events differ", differEvents)
	}
}

// irImportFixtureDirs enrolls the CALLS-owned two-package fixture in IR
// agreement only. Once present it must be complete. Reader is first:
// its test build reaches producer as an import, not a second root build.
func irImportFixtureDirs(t *testing.T, root string, extra ...string) []string {
	t.Helper()
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(root, "ergo.mod")); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("imported-call IR fixture needs ergo.mod: %v", err)
	}
	dirs := []string{filepath.Join(root, "reader"), filepath.Join(root, "producer")}
	for _, dir := range extra {
		dirs = append(dirs, filepath.Join(root, dir))
	}
	for _, dir := range dirs {
		if len(ergoFiles(t, dir)) == 0 {
			t.Fatalf("imported-call IR fixture has no Ergo sources in %s", rel(dir))
		}
	}
	return dirs
}

// irWitnessSite uses irdump's declaration byte offset, not the function
// body's position, so a generated symbol can be tied to its declaration.
type irWitnessSite struct{ path, offset, symbol string }

// readIRWitnesses loads one JSON file per group. A declaration is the
// exact prefix of a unique source line, ignoring indentation. It stays
// stable when unrelated lines move and resolves to irdump's byte offset.
func readIRWitnesses(t *testing.T) map[irWitnessSite]string {
	t.Helper()
	dir := filepath.Join(repoRoot(), "internal", "selfhostcheck", "testdata")
	groups, err := filepath.Glob(filepath.Join(dir, "ir-witnesses", "*.json"))
	if err != nil || len(groups) == 0 {
		t.Fatalf("IR witness registry: expected group files, got %v (%v)", groups, err)
	}
	witnesses := map[irWitnessSite]string{}
	for _, path := range groups {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		group := filepath.Base(path)
		var rows []struct {
			Fixture     string `json:"fixture"`
			Declaration string `json:"declaration"`
			Symbol      string `json:"symbol"`
		}
		if err := json.Unmarshal(data, &rows); err != nil || len(rows) == 0 {
			t.Fatalf("%s: expected an array of IR witnesses (%v)", group, err)
		}
		for _, row := range rows {
			file, err := filepath.Localize(row.Fixture)
			if err != nil || filepath.Ext(file) != ".ergo" || row.Symbol == "" || row.Declaration == "" || row.Declaration != strings.TrimSpace(row.Declaration) || strings.ContainsAny(row.Declaration, "\r\n") {
				t.Fatalf("%s: invalid IR witness: %+v", group, row)
			}
			file = filepath.Join(dir, "bind", file)
			src, err := os.ReadFile(file)
			if err != nil {
				t.Fatalf("%s: %v", group, err)
			}
			at, start := -1, 0
			for line := range strings.SplitSeq(string(src), "\n") {
				decl := strings.TrimLeft(line, " \t")
				if strings.HasPrefix(decl, row.Declaration) {
					if at >= 0 {
						t.Fatalf("%s: %s: declaration prefix %q matches more than one line", group, rel(file), row.Declaration)
					}
					at = start + len(line) - len(decl)
				}
				start += len(line) + 1
			}
			if at < 0 {
				t.Fatalf("%s: %s: declaration prefix %q not found", group, rel(file), row.Declaration)
			}
			k := irWitnessSite{file, strconv.Itoa(at), row.Symbol}
			if existing, ok := witnesses[k]; ok {
				t.Fatalf("%s: duplicate witness %s:%d %s (already in %s)", group, rel(file), at, row.Symbol, existing)
			}
			witnesses[k] = group
		}
	}
	return witnesses
}

// irPathIDs gives each source path one id, the file index both sides'
// positions are mapped to.
type irPathIDs map[string]int

func (p irPathIDs) id(path string) int {
	if i, ok := p[path]; ok {
		return i
	}
	p[path] = len(p)
	return p[path]
}

type irKey struct{ file, line, col int }

type irIdentity struct {
	at   irKey
	name string
}

// irGoFunc is one Go function: its name unexported (irSplitName), its
// name in the program, and its text under the unexported names.
type irGoFunc struct {
	name, raw string
	data      []byte
}

// irGoIndex is the Go builder's functions by declaration position, each
// the distinct texts written there across the programs.
type irGoIndex map[irKey][]irGoFunc

func (x irGoIndex) functionCount() int {
	n := 0
	names := map[string]struct{}{}
	for _, fs := range x {
		if len(fs) == 1 {
			n++
			continue
		}
		clear(names)
		for _, f := range fs {
			names[f.name] = struct{}{}
		}
		n += len(names)
	}
	return n
}

// irProgramName is the Go builder's names that are program-wide rather
// than the function's: the split's export of a name two Go packages use
// (pkgsplit.go: X before an unexported name, again while taken) and a
// collision suffix (_N) of a field or a literal's key (irBaseFields).
// Declarations' names follow NAME-LOCAL (S4c/naming.md) and compare
// under irSplitName alone.
var irProgramName = regexp.MustCompile(`^X+([a-z_])|_[0-9]+$|_[0-9]+(\.)`)
var irCollisionSuffix = regexp.MustCompile(`_[0-9]+$`)

var irMethodExport = regexp.MustCompile(`\.X+([a-z_])`)

// irBaseName is name without its program-wide parts.
func irBaseName(name string) string {
	return irMethodExport.ReplaceAllString(irProgramName.ReplaceAllString(name, "${1}${2}"), ".${1}")
}

var irSplitExport = regexp.MustCompile(`^X+([a-z_])`)

// irSplitName is a declaration's name without the split's export (X
// before a name two Go packages use, pkgsplit.go), the one program-wide
// part of a declaration's Go name NAME-LOCAL leaves (NL-3).
func irSplitName(name string) string {
	return irMethodExport.ReplaceAllString(irSplitExport.ReplaceAllString(name, "${1}"), ".${1}")
}

// irBaseFields removes program-wide spelling from the Go side. Field
// and method exports come from this split; qualified type heads come
// from this IR file's split declarations, never a host import.
func irBaseFields(fn *irFunc, originals, typeHeads map[string]string) {
	var walk func(v reflect.Value)
	walk = func(v reflect.Value) {
		switch v.Kind() {
		case reflect.Pointer:
			if !v.IsNil() {
				walk(v.Elem())
			}
		case reflect.Slice:
			for i := range v.Len() {
				walk(v.Index(i))
			}
		case reflect.Struct:
			if loc, ok := v.Addr().Interface().(*irLocal); ok {
				elem := loc.Type
				for {
					if strings.HasPrefix(elem, "[]") {
						elem = elem[2:]
					} else if strings.HasPrefix(elem, "[") {
						if close := strings.IndexByte(elem, ']'); close > 1 {
							elem = elem[close+1:]
						} else {
							break
						}
					} else if strings.HasPrefix(elem, "map[") {
						if close := strings.IndexByte(elem, ']'); close > 4 {
							elem = elem[close+1:]
						} else {
							break
						}
					} else {
						break
					}
				}
				if head, args, generic := strings.Cut(elem, "["); generic {
					if name, ok := typeHeads[head]; ok {
						loc.Type = loc.Type[:len(loc.Type)-len(elem)] + name + "[" + args
					}
				} else if name, ok := originals[elem]; ok {
					loc.Type = loc.Type[:len(loc.Type)-len(elem)] + name
				}
				loc.Type = irBaseType(loc.Type, originals, typeHeads)
			}
			if c, ok := v.Addr().Interface().(*irCase); ok {
				for i, typ := range c.Types {
					c.Types[i] = irBaseType(typ, originals, typeHeads)
				}
			}
			if e, ok := v.Addr().Interface().(*irExpr); ok {
				switch e.K {
				case "field":
					if name, ok := originals[e.Name]; ok {
						e.Name = name
					}
					e.Name = irCollisionSuffix.ReplaceAllString(e.Name, "")
				case "call", "global":
					if orig, ok := originals[e.Name]; ok {
						e.Name = orig
					}
				case "mcall":
					if name, ok := originals[e.Name]; ok {
						e.Name = name
					}
				case "pcall":
					e.V = irBaseType(e.V, originals, typeHeads)
				case "of", "as", "tag", "assert", "conv", "cast", "make", "func", "type":
					e.T = irBaseType(e.T, originals, typeHeads)
				case "lit", "zero":
					elem := e.T
					for strings.HasPrefix(elem, "[]") {
						elem = elem[2:]
					}
					prefixLen := len(e.T) - len(elem)
					if head, args, generic := strings.Cut(elem, "["); generic {
						if name, ok := typeHeads[head]; ok {
							e.T = e.T[:len(e.T)-len(elem)] + name + "[" + args
						} else if name, ok := originals[head]; ok {
							e.T = e.T[:len(e.T)-len(elem)] + name + "[" + args
						}
						if restHead, restArgs, hasBracket := strings.Cut(e.T[prefixLen:], "["); hasBracket {
							e.T = e.T[:prefixLen] + restHead + "[" + irBaseType(restArgs, originals, typeHeads)
						}
					} else if name, ok := originals[elem]; ok {
						e.T = e.T[:len(e.T)-len(elem)] + name
					}
					for i, k := range e.Keys {
						e.Keys[i] = irBaseName(k)
					}
				}
			}
			for i := range v.NumField() {
				walk(v.Field(i))
			}
		}
	}
	walk(reflect.ValueOf(fn))
}

func irBaseType(s string, originals, typeHeads map[string]string) string {
	isSpell := func(c byte) bool {
		return c == '_' || c == '.' || c == '/' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
	}
	var out strings.Builder
	changed := false
	for i := 0; i < len(s); {
		j := i
		for j < len(s) && isSpell(s[j]) {
			j++
		}
		if j == i {
			out.WriteByte(s[i])
			i++
			continue
		}
		tok := s[i:j]
		if j < len(s) && s[j] == '[' {
			if name, ok := typeHeads[tok]; ok {
				out.WriteString(name)
				changed = true
				i = j
				continue
			}
		}
		if name, ok := originals[tok]; ok {
			out.WriteString(name)
			changed = true
		} else {
			out.WriteString(tok)
		}
		i = j
	}
	if !changed {
		return s
	}
	return out.String()
}

// TestIRAgreementKeepsUnrenamedOperands kills lower-agree-guess-split-export
// and lower-agree-zero-keeps-split-export: a difference in either operand,
// or in a zero row's type, is real unless that build exported it.
func TestIRAgreementKeepsUnrenamedOperands(t *testing.T) {
	body := func(typ, member string) *irFunc {
		return &irFunc{Body: []*irStmt{{K: "ret", X: &irExpr{
			K: "mcall", Name: member, X: &irExpr{K: "lit", T: typ},
		}}}}
	}
	want := body("[][]pkg__cell", "read")
	exports := map[string]string{"XXpkg__cell": "pkg__cell", "Xread": "read"}
	for _, tc := range []struct {
		name, typ, member string
		equal             bool
	}{
		{"proven exports", "[][]XXpkg__cell", "Xread", true},
		{"real element difference", "[][]Xpkg__cell", "Xread", false},
		{"real method difference", "[][]XXpkg__cell", "XXread", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := body(tc.typ, tc.member)
			irBaseFields(got, exports, nil)
			if equal := reflect.DeepEqual(got, want); equal != tc.equal {
				call := got.Body[0].X
				t.Errorf("normalized %s.%s, equality = %v, want %v", call.X.T, call.Name, equal, tc.equal)
			}
		})
	}
	zero := func(typ string) *irFunc {
		return &irFunc{Body: []*irStmt{{K: "ret", X: &irExpr{K: "zero", T: typ}}}}
	}
	for _, tc := range []struct {
		name, typ string
		equal     bool
	}{
		{"proven zero export", "XXpkg__cell", true},
		{"real zero difference", "Xpkg__cell", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := zero(tc.typ)
			irBaseFields(got, exports, nil)
			if equal := reflect.DeepEqual(got, zero("pkg__cell")); equal != tc.equal {
				t.Errorf("normalized zero %s, equality = %v, want %v", got.Body[0].X.T, equal, tc.equal)
			}
		})
	}
}

// TestIRAgreementKeepsUnprovenSplitNames kills lower-agree-guess-field-export
// and lower-agree-guess-qualified-type: a different field or generic type
// must not compare equal just because it has the same unqualified spelling.
func TestIRAgreementKeepsUnprovenSplitNames(t *testing.T) {
	body := func(field, typ string) *irFunc {
		return &irFunc{Body: []*irStmt{{K: "ret", X: &irExpr{
			K: "field", Name: field, X: &irExpr{K: "lit", T: typ},
		}}}}
	}
	want := body("tag", "[]Choice[int, bool]")
	members := map[string]string{"Xtag": "tag"}
	heads := map[string]string{"example.com/generated/model.Choice": "Choice"}
	for _, tc := range []struct {
		name, field, typ string
		equal            bool
	}{
		{"proven field and type", "Xtag", "[]example.com/generated/model.Choice[int, bool]", true},
		{"unrenamed field", "XXtag", "[]example.com/generated/model.Choice[int, bool]", false},
		{"host type with same suffix", "Xtag", "[]example.com/host/model.Choice[int, bool]", false},
		{"different type arguments", "Xtag", "[]example.com/generated/model.Choice[bool, int]", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := body(tc.field, tc.typ)
			irBaseFields(got, members, heads)
			if equal := reflect.DeepEqual(got, want); equal != tc.equal {
				x := got.Body[0].X
				t.Errorf("normalized field %q, type %q; equality = %v, want %v", x.Name, x.X.T, equal, tc.equal)
			}
		})
	}
}

// names is the names of the Go functions at k.
func (x irGoIndex) names(k irKey) string {
	var ns []string
	for _, f := range x[k] {
		ns = append(ns, f.raw)
	}
	return strings.Join(ns, ", ")
}

// at is the Go functions at k of the Ergo side's name (its base name),
// one per distinct text the programs gave it; cause says why there is
// none to compare.
func (x irGoIndex) at(k irKey, name string) ([]irGoFunc, string) {
	fs := x[k]
	if len(fs) == 0 {
		return nil, "no Go IR body at the position"
	}
	var same []irGoFunc
	for _, f := range fs {
		if f.name == name {
			same = append(same, f)
		}
	}
	if len(same) == 0 {
		return nil, "Go IR bodies of another name at the position"
	}
	return same, ""
}

type irBodyComparison struct {
	wants   []irGoFunc
	cause   string
	match   int
	skipped bool
}

func (x irGoIndex) compare(k irKey, site irWitnessSite, got []byte, goUnlowered bool, receipt *strings.Builder) irBodyComparison {
	wants, cause := x.at(k, site.symbol)
	if cause != "" {
		return irBodyComparison{cause: cause, match: -1, skipped: goUnlowered}
	}
	// A body the programs lower differently agrees with one of them.
	match := slices.IndexFunc(wants, func(w irGoFunc) bool { return bytes.Equal(got, w.data) })
	if match >= 0 && receipt != nil {
		fmt.Fprintf(receipt, "F %s %s %s\n", site.path, site.offset, wants[match].name)
	}
	return irBodyComparison{wants: wants, match: match}
}

// TestIRAgreementMatchesReceipt kills lower-agree-record-differing-body:
// the receipt proves equality, not just the existence of a Go body.
func TestIRAgreementMatchesReceipt(t *testing.T) {
	start := irKey{line: 1}
	idx := irGoIndex{
		start: {
			{name: "read", raw: "Xread", data: []byte("different variant")},
			{name: "read", raw: "Xread", data: []byte("equal variant")},
		},
	}
	var receipt strings.Builder
	for _, tc := range []struct {
		key     irKey
		site    irWitnessSite
		body    string
		noGo    bool
		match   int
		skipped bool
	}{
		{start, irWitnessSite{"/corpus/reader.ergo", "12", "read"}, "equal variant", false, 1, false},
		{start, irWitnessSite{"/corpus/reader.ergo", "12", "read"}, "not an equal body", false, -1, false},
		{irKey{line: 2}, irWitnessSite{"/corpus/skipped.ergo", "20", "skipped"}, "no Go body", true, -1, true},
	} {
		result := idx.compare(tc.key, tc.site, []byte(tc.body), tc.noGo, &receipt)
		if result.match != tc.match || result.skipped != tc.skipped {
			t.Errorf("%s: match=%d skipped=%v, want %d/%v", tc.body, result.match, result.skipped, tc.match, tc.skipped)
		}
	}
	if want := "F /corpus/reader.ergo 12 read\n"; receipt.String() != want {
		t.Errorf("matches receipt = %q, want %q", receipt.String(), want)
	}
}

type irGoProgram struct {
	u      lower.Unit
	tests  bool
	dir    string
	module string
	reach  []*check.Func
}

// irGoTestOracle reads a production root's test build only when needed.
// Keep the test index separate: a test variant must not mask a real
// disagreement at a declaration for which production already wrote IR.
type irGoTestOracle struct {
	roots       map[string]irGoProgram
	cache       map[string]irGoIndex
	unavailable map[string]string
}

func (o irGoTestOracle) fill(t *testing.T, paths irPathIDs, idx irGoIndex, key irKey, source string) {
	t.Helper()
	if len(idx[key]) != 0 {
		return
	}
	dir := filepath.Dir(source)
	p, ok := o.roots[dir]
	if !ok {
		return
	}
	bodies, ready := o.cache[dir]
	if !ready {
		p.tests = true
		bodies = irGoIndex{}
		if err := indexGoIR(t, paths, bodies, p); err != nil {
			t.Logf("%s: Go test-build oracle unavailable: %v", rel(dir), err)
			if o.unavailable != nil {
				o.unavailable[dir] = err.Error()
			}
			bodies = nil
		}
		o.cache[dir] = bodies
	}
	if len(bodies[key]) != 0 {
		idx[key] = bodies[key]
	}
}

func TestIRTestBuildAndProductionControl(t *testing.T) {
	needsBuild(t)
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	bin := filepath.Join(t.TempDir(), "irdump")
	if o, err := runCompiler(t, ctx, ergoCLI(t, ctx), "build", "-o", bin, "./cmd/irdump"); err != nil {
		t.Fatalf("ergo build ./cmd/irdump: %v\n%s", err, o)
	}
	const src = `func main() {}

func Helper(n int) int { n + 1 }

test Check {
	let n = 2
	let ok = n > 0
	assert ok
}

test SkipBranch {
	let done = false
	if done {
		skip "already done"
	}
	assert !done
}

test ParamSlice(xs []int, n int) {
	let ok = len(xs)+n >= n
	assert ok
}
`
	dir := t.TempDir()
	file := filepath.Join(dir, "main.ergo")
	if err := os.WriteFile(file, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	u, err := irspike.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, ext := goKinds(u.Package)
	_, foreign := goTypes(u.Package)
	pkgPath := importPath(dir, u.Package)
	tmp := t.TempDir()
	ef, ff := filepath.Join(tmp, "ext"), filepath.Join(tmp, "foreign")
	writeLines(t, ef, ext)
	writeLines(t, ff, foreign)
	manifest := filepath.Join(tmp, "manifest")
	writeLines(t, manifest, []string{strings.Join([]string{pkgPath, ef, ff, file}, " ")})
	roots := filepath.Join(tmp, "roots")
	writeLines(t, roots, []string{pkgPath})
	empty := filepath.Join(tmp, "empty")
	writeLines(t, empty, nil)

	parseDump := func(lines []string, paths irPathIDs) (map[string]struct {
		key  irKey
		data []byte
	}, []string) {
		emitted := map[string]struct {
			key  irKey
			data []byte
		}{}
		var files, stops []string
		for _, l := range lines {
			kind, rest, _ := strings.Cut(l, " ")
			switch kind {
			case "@pkg":
				files = nil
			case "P":
				_, path, _ := strings.Cut(rest, " ")
				files = append(files, path)
			case "S":
				stops = append(stops, l)
			case "F":
				f := strings.SplitN(rest, " ", 3)
				var fn irFunc
				if err := json.Unmarshal([]byte(f[2]), &fn); err != nil {
					t.Fatalf("unmarshal IR: %v", err)
				}
				if err := irMapFiles(&fn, files, paths); err != nil {
					t.Fatal(err)
				}
				got, err := json.Marshal(&fn)
				if err != nil {
					t.Fatal(err)
				}
				emitted[fn.Name] = struct {
					key  irKey
					data []byte
				}{irKey{fn.Pos[0], fn.Pos[1], fn.Pos[2]}, got}
			}
		}
		return emitted, stops
	}

	paths := irPathIDs{}
	prodGo := irGoIndex{}
	if err := indexGoIR(t, paths, prodGo, irGoProgram{u: u, tests: false, dir: dir, module: dir, reach: ownFuncs(u, map[string]bool{})}); err != nil {
		t.Fatal(err)
	}
	testGo := irGoIndex{}
	if err := indexGoIR(t, paths, testGo, irGoProgram{u: u, tests: true, dir: dir, module: dir, reach: ownFuncs(u, map[string]bool{})}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ergo__TestCheck", "ergo__TestSkipBranch", "ergo__TestParamSlice"} {
		for _, fs := range prodGo {
			for _, f := range fs {
				if f.name == want {
					t.Fatalf("Go production build unexpectedly emitted %s", want)
				}
			}
		}
	}

	unexpectedStops := func(stops []string) []string {
		var out []string
		for _, s := range stops {
			if !strings.HasSuffix(s, " main or Close") {
				out = append(out, s)
			}
		}
		return out
	}

	testEmitted, testStops := parseDump(runDump(t, ctx, bin, "-std", stdDir(), "-roots", roots, "-tests", roots, "-corpus", manifest), paths)
	if bad := unexpectedStops(testStops); len(bad) != 0 {
		t.Fatalf("test build had unexpected stops: %v", bad)
	}
	for _, want := range []string{"Helper", "ergo__TestCheck", "ergo__TestSkipBranch", "ergo__TestParamSlice"} {
		got, ok := testEmitted[want]
		if !ok {
			t.Fatalf("test build did not emit %s (got %v)", want, testEmitted)
		}
		res := testGo.compare(got.key, irWitnessSite{file, "0", want}, got.data, false, nil)
		if res.match < 0 {
			t.Fatalf("test build %s differed from Go (%s)\nergo: %s\n  go: %v", want, res.cause, got.data, res.wants)
		}
	}

	prodEmitted, prodStops := parseDump(runDump(t, ctx, bin, "-std", stdDir(), "-roots", roots, "-corpus", manifest), paths)
	if bad := unexpectedStops(prodStops); len(bad) != 0 {
		t.Fatalf("production build had unexpected stops: %v", bad)
	}
	if _, ok := prodEmitted["Helper"]; !ok {
		t.Fatal("production build omitted ordinary Helper")
	}
	for _, forbidden := range []string{"ergo__TestCheck", "ergo__TestSkipBranch", "ergo__TestParamSlice"} {
		if _, ok := prodEmitted[forbidden]; ok {
			t.Fatalf("production build emitted test body %s", forbidden)
		}
	}

	nonRootEmitted, nonRootStops := parseDump(runDump(t, ctx, bin, "-std", stdDir(), "-roots", empty, "-tests", roots, "-corpus", manifest), paths)
	if bad := unexpectedStops(nonRootStops); len(bad) != 0 {
		t.Fatalf("non-root build had unexpected stops: %v", bad)
	}
	for _, forbidden := range []string{"ergo__TestCheck", "ergo__TestSkipBranch", "ergo__TestParamSlice"} {
		if _, ok := nonRootEmitted[forbidden]; ok {
			t.Fatalf("non-root build emitted test body %s", forbidden)
		}
	}
}

func TestIRLawBuildAndProductionControl(t *testing.T) {
	needsBuild(t)
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	bin := filepath.Join(t.TempDir(), "irdump")
	if o, err := runCompiler(t, ctx, ergoCLI(t, ctx), "build", "-o", bin, "./cmd/irdump"); err != nil {
		t.Fatalf("ergo build ./cmd/irdump: %v\n%s", err, o)
	}
	const src = `func main() {}

func Double(n int) int { n + n }

type Pair { A int, B int }

law Trivial() { true }

law DoubleEven(n int) { Double(n) == n + n }

law EarlyReturn(n int) {
	if n < 0 {
		return true
	}
	n >= 0
}

law SliceLen(xs []int) { len(xs) >= 0 }

law PairCommutes(p Pair) { p.A + p.B == p.B + p.A }

law SliceAndScalar(xs []int, n int) { len(xs) + n >= n }

law OptionSliceLen(o Option[int]) { len([]Option[int]{o}) == 1 }

law RefusedTuple(p (int, int)) { true }
`
	dir := t.TempDir()
	file := filepath.Join(dir, "main.ergo")
	if err := os.WriteFile(file, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	u, err := irspike.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, ext := goKinds(u.Package)
	_, foreign := goTypes(u.Package)
	pkgPath := importPath(dir, u.Package)
	tmp := t.TempDir()
	ef, ff := filepath.Join(tmp, "ext"), filepath.Join(tmp, "foreign")
	writeLines(t, ef, ext)
	writeLines(t, ff, foreign)
	manifest := filepath.Join(tmp, "manifest")
	writeLines(t, manifest, []string{strings.Join([]string{pkgPath, ef, ff, file}, " ")})
	roots := filepath.Join(tmp, "roots")
	writeLines(t, roots, []string{pkgPath})
	empty := filepath.Join(tmp, "empty")
	writeLines(t, empty, nil)

	parseDump := func(lines []string, paths irPathIDs) (map[string]struct {
		key  irKey
		data []byte
	}, []string) {
		emitted := map[string]struct {
			key  irKey
			data []byte
		}{}
		var files, stops []string
		for _, l := range lines {
			kind, rest, _ := strings.Cut(l, " ")
			switch kind {
			case "@pkg":
				files = nil
			case "P":
				_, path, _ := strings.Cut(rest, " ")
				files = append(files, path)
			case "S":
				stops = append(stops, l)
			case "F":
				f := strings.SplitN(rest, " ", 3)
				var fn irFunc
				if err := json.Unmarshal([]byte(f[2]), &fn); err != nil {
					t.Fatalf("unmarshal IR: %v", err)
				}
				if err := irMapFiles(&fn, files, paths); err != nil {
					t.Fatal(err)
				}
				got, err := json.Marshal(&fn)
				if err != nil {
					t.Fatal(err)
				}
				emitted[fn.Name] = struct {
					key  irKey
					data []byte
				}{irKey{fn.Pos[0], fn.Pos[1], fn.Pos[2]}, got}
			}
		}
		return emitted, stops
	}

	paths := irPathIDs{}
	prodGo := irGoIndex{}
	if err := indexGoIR(t, paths, prodGo, irGoProgram{u: u, tests: false, dir: dir, module: dir, reach: ownFuncs(u, map[string]bool{})}); err != nil {
		t.Fatal(err)
	}
	testGo := irGoIndex{}
	if err := indexGoIR(t, paths, testGo, irGoProgram{u: u, tests: true, dir: dir, module: dir, reach: ownFuncs(u, map[string]bool{})}); err != nil {
		t.Fatal(err)
	}
	lawNames := []string{"ergo__LawTrivial", "ergo__LawDoubleEven", "ergo__LawEarlyReturn", "ergo__LawSliceLen", "ergo__LawPairCommutes", "ergo__LawSliceAndScalar", "ergo__LawOptionSliceLen"}
	for _, want := range lawNames {
		for _, fs := range prodGo {
			for _, f := range fs {
				if f.name == want {
					t.Fatalf("Go production build unexpectedly emitted %s", want)
				}
			}
		}
	}

	unexpectedStops := func(stops []string, allowTuple bool) []string {
		var out []string
		for _, s := range stops {
			if strings.HasSuffix(s, " main or Close") {
				continue
			}
			if allowTuple && strings.HasSuffix(s, " law: parameter tuple") {
				continue
			}
			out = append(out, s)
		}
		return out
	}

	testEmitted, testStops := parseDump(runDump(t, ctx, bin, "-std", stdDir(), "-roots", roots, "-tests", roots, "-corpus", manifest), paths)
	if bad := unexpectedStops(testStops, true); len(bad) != 0 {
		t.Fatalf("test build had unexpected stops: %v", bad)
	}
	if _, ok := testEmitted["ergo__LawRefusedTuple"]; ok {
		t.Fatal("unshaped tuple parameter law must stop honestly and not emit")
	}
	foundTupleStop := false
	for _, s := range testStops {
		if strings.HasSuffix(s, " law: parameter tuple") {
			foundTupleStop = true
		}
	}
	if !foundTupleStop {
		t.Fatalf("expected honest tuple parameter stop for RefusedTuple, got stops: %v", testStops)
	}
	for _, want := range append([]string{"Double"}, lawNames...) {
		got, ok := testEmitted[want]
		if !ok {
			t.Fatalf("test build did not emit %s (got %v)", want, testEmitted)
		}
		res := testGo.compare(got.key, irWitnessSite{file, "0", want}, got.data, false, nil)
		if res.match < 0 {
			t.Fatalf("test build %s differed from Go (%s)\nergo: %s\n  go: %v", want, res.cause, got.data, res.wants)
		}
	}

	prodEmitted, prodStops := parseDump(runDump(t, ctx, bin, "-std", stdDir(), "-roots", roots, "-corpus", manifest), paths)
	if bad := unexpectedStops(prodStops, false); len(bad) != 0 {
		t.Fatalf("production build had unexpected stops: %v", bad)
	}
	if _, ok := prodEmitted["Double"]; !ok {
		t.Fatal("production build omitted ordinary Double")
	}
	for _, forbidden := range lawNames {
		if _, ok := prodEmitted[forbidden]; ok {
			t.Fatalf("production build emitted law body %s", forbidden)
		}
	}

	nonRootEmitted, nonRootStops := parseDump(runDump(t, ctx, bin, "-std", stdDir(), "-roots", empty, "-tests", roots, "-corpus", manifest), paths)
	if bad := unexpectedStops(nonRootStops, false); len(bad) != 0 {
		t.Fatalf("non-root build had unexpected stops: %v", bad)
	}
	for _, forbidden := range lawNames {
		if _, ok := nonRootEmitted[forbidden]; ok {
			t.Fatalf("non-root build emitted law body %s", forbidden)
		}
	}
}

func TestIRGenericHeaderAndOrder(t *testing.T) {
	needsBuild(t)
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	bin := filepath.Join(t.TempDir(), "irdump")
	if o, err := runCompiler(t, ctx, ergoCLI(t, ctx), "build", "-o", bin, "./cmd/irdump"); err != nil {
		t.Fatalf("ergo build ./cmd/irdump: %v\n%s", err, o)
	}
	const src = `func main() {
	let _ = UseInt(1)
	let _ = UseString("a")
	let _ = First(1, "a")
	let _ = Second(1, "a")
}

func Identity[T](x T) T {
	let y = x
	y
}

func First[A, B](a A, b B) A {
	let x = a
	x
}

func Second[A, B](a A, b B) B {
	let y = b
	y
}

func UseInt(n int) int { Identity(n) }

func UseString(s string) string { Identity(s) }

type Boxed { Read() int }

func ReadOne[T Boxed](x T) int { 1 }
`
	dir := t.TempDir()
	file := filepath.Join(dir, "main.ergo")
	if err := os.WriteFile(file, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	u, err := irspike.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, ext := goKinds(u.Package)
	_, foreign := goTypes(u.Package)
	pkgPath := importPath(dir, u.Package)
	tmp := t.TempDir()
	ef, ff := filepath.Join(tmp, "ext"), filepath.Join(tmp, "foreign")
	writeLines(t, ef, ext)
	writeLines(t, ff, foreign)
	manifest := filepath.Join(tmp, "manifest")
	writeLines(t, manifest, []string{strings.Join([]string{pkgPath, ef, ff, file}, " ")})
	roots := filepath.Join(tmp, "roots")
	writeLines(t, roots, []string{pkgPath})

	paths := irPathIDs{}
	goIdx := irGoIndex{}
	if err := indexGoIR(t, paths, goIdx, irGoProgram{u: u, tests: false, dir: dir, module: dir, reach: ownFuncs(u, map[string]bool{})}); err != nil {
		t.Fatal(err)
	}

	emitted := map[string]struct {
		key  irKey
		data []byte
	}{}
	var files, stops []string
	for _, l := range runDump(t, ctx, bin, "-std", stdDir(), "-roots", roots, "-corpus", manifest) {
		kind, rest, _ := strings.Cut(l, " ")
		switch kind {
		case "@pkg":
			files = nil
		case "P":
			_, path, _ := strings.Cut(rest, " ")
			files = append(files, path)
		case "S":
			stops = append(stops, l)
		case "F":
			f := strings.SplitN(rest, " ", 3)
			var fn irFunc
			if err := json.Unmarshal([]byte(f[2]), &fn); err != nil {
				t.Fatalf("unmarshal IR: %v", err)
			}
			if err := irMapFiles(&fn, files, paths); err != nil {
				t.Fatal(err)
			}
			got, err := json.Marshal(&fn)
			if err != nil {
				t.Fatal(err)
			}
			emitted[fn.Name] = struct {
				key  irKey
				data []byte
			}{irKey{fn.Pos[0], fn.Pos[1], fn.Pos[2]}, got}
		}
	}

	for _, want := range []string{"Identity", "First", "Second"} {
		got, ok := emitted[want]
		if !ok {
			t.Fatalf("generic build did not emit %s (stops: %v)", want, stops)
		}
		res := goIdx.compare(got.key, irWitnessSite{file, "0", want}, got.data, false, nil)
		if res.match < 0 {
			t.Fatalf("generic %s differed from Go (%s)\nergo: %s\n  go: %v", want, res.cause, got.data, res.wants)
		}
	}
	if _, ok := emitted["ReadOne"]; ok {
		t.Fatal("constrained operation generic ReadOne must not emit a witness-less body")
	}
	foundStop := false
	for _, s := range stops {
		if strings.HasSuffix(s, " generic: constrained type parameter") {
			foundStop = true
		}
	}
	if !foundStop {
		t.Fatalf("expected honest constrained type parameter stop for ReadOne, got stops: %v", stops)
	}
}

func TestIRLetShapes(t *testing.T) {
	needsBuild(t)
	const src = `func main() {
	let _ = SliceLetLen([]int{1, 2})
	let _ = SliceLitLet(1, 2)
	let _ = RecordLet(Pair{A: 3, B: 4})
	let _ = BagLetLen(Bag{Items: []int{5, 6}})
	let _ = OptionLet(None)
	let _ = UnionLet(FlagOff)
	let _ = MutSliceStop([]int{1})
	let _ = ExplicitSliceStop([]int{1})
	let _ = TupleStop((1, 2))
}

type Pair { A int, B int }

type Bag { Items []int }

type Flag = FlagOff{} | FlagOn{}

func SliceLetLen(xs []int) int {
	let ys = xs
	len(ys)
}

func SliceLitLet(a int, b int) []int {
	let ys = []int{a, b}
	ys
}

func RecordLet(p Pair) int {
	let q = p
	q.A + q.B
}

func BagLetLen(b Bag) int {
	let xs = b.Items
	len(xs)
}

func OptionLet(o Option[int]) []Option[int] {
	let p = o
	[]Option[int]{p}
}

func UnionLet(f Flag) Flag {
	let g = f
	g
}

func MutSliceStop(xs []int) int {
	let mut ys = xs
	ys = xs
	len(ys)
}

func ExplicitSliceStop(xs []int) int {
	let ys []int = xs
	len(ys)
}

func TupleStop(p (int, int)) int {
	let q = p
	let _ = q
	1
}
`
	d := irDumpFixture(t, src)
	d.agree(t, "let-shapes", "SliceLetLen", "SliceLitLet", "Pair.RecordLet", "Bag.BagLetLen", "OptionLet", "UnionLet")
	d.stopsHonestly(t, []string{"MutSliceStop", "ExplicitSliceStop", "TupleStop"}, " let of slice", " let with explicit type annotation", " let of tuple")
}

// TestIRFailFrameShapes holds compiler/lower's fallible frames to the Go
// builder over the forms FAIL-FRAME-SHAPES admits: several failure exits,
// a unit success result, a zero-field failure record, a scalar field read
// in a failure field, and an if/else whose branches are the value tail. A
// frame whose body calls a function stops (CALLS). It builds irdump and
// runs it, as TestIRLetShapes does: the Ergo side is a compiled program.
//
// Mutants it alone catches: testdata/mutants.json, rows lower-fail-frame-*.
func TestIRFailFrameShapes(t *testing.T) {
	needsBuild(t)
	const src = `func main() {
	let _ = FailFrameTwoExits(FailFrameIn{A: 3, B: 4}, 1) catch {
	case FailFrameBad:
		0
	}
	FailFrameUnit(FailFrameIn{A: 3, B: 4}, 1) catch {
	case FailFrameBad:
	}
	let _ = FailFrameEmpty(FailFrameIn{A: 3, B: 4}, 1) catch {
	case FailFrameGone:
		0
	}
	let _ = FailFrameElse(FailFrameIn{A: 3, B: 4}, 1) catch {
	case FailFrameBad:
		0
	}
	let _ = FailFrameCallStop(FailFrameIn{A: 3, B: 4}, 1) catch {
	case FailFrameBad:
		0
	}
}

type FailFrameIn { A int, B int }

type FailFrameBad { Code int }

type FailFrameGone {}

func FailFrameTwoExits(input FailFrameIn, n int) int fails FailFrameBad {
	if n < 0 {
		return FailFrameBad{Code: n}
	}
	if n > input.A {
		return FailFrameBad{Code: input.A}
	}
	n + input.B
}

func FailFrameUnit(input FailFrameIn, n int) fails FailFrameBad {
	if n < 0 {
		return FailFrameBad{Code: n}
	}
}

func FailFrameEmpty(input FailFrameIn, n int) int fails FailFrameGone {
	if n == 0 {
		return FailFrameGone
	}
	n
}

func FailFrameElse(input FailFrameIn, n int) int fails FailFrameBad {
	if n < 0 {
		FailFrameBad{Code: n}
	} else {
		n
	}
}

func failFrameHelper(n int) int { n + 1 }

func FailFrameCallStop(input FailFrameIn, n int) int fails FailFrameBad {
	if n < 0 {
		return FailFrameBad{Code: failFrameHelper(n)}
	}
	n
}
`
	d := irDumpFixture(t, src)
	d.agree(t, "fail-frame-shapes", "FailFrameIn.FailFrameTwoExits", "FailFrameIn.FailFrameUnit", "FailFrameIn.FailFrameEmpty", "FailFrameIn.FailFrameElse")
	d.stopsHonestly(t, []string{"FailFrameIn.FailFrameCallStop"}, " CALLS: fallible frame contains a call")
}

// irFixture is what irdump built for one root package of one file: the
// Ergo side's functions by name, its stop lines, and the Go builder's
// functions of the same package.
type irFixture struct {
	file    string
	emitted map[string]irFixtureFunc
	stops   []string
	goIdx   irGoIndex
}

type irFixtureFunc struct {
	key  irKey
	data []byte
}

// irDumpFixture builds irdump and lowers src, the one file of a root
// package, with both builders.
func irDumpFixture(t *testing.T, src string) irFixture {
	t.Helper()
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	bin := filepath.Join(t.TempDir(), "irdump")
	if o, err := runCompiler(t, ctx, ergoCLI(t, ctx), "build", "-o", bin, "./cmd/irdump"); err != nil {
		t.Fatalf("ergo build ./cmd/irdump: %v\n%s", err, o)
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "main.ergo")
	if err := os.WriteFile(file, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	u, err := irspike.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, ext := goKinds(u.Package)
	_, foreign := goTypes(u.Package)
	pkgPath := importPath(dir, u.Package)
	tmp := t.TempDir()
	ef, ff := filepath.Join(tmp, "ext"), filepath.Join(tmp, "foreign")
	writeLines(t, ef, ext)
	writeLines(t, ff, foreign)
	manifest := filepath.Join(tmp, "manifest")
	writeLines(t, manifest, []string{strings.Join([]string{pkgPath, ef, ff, file}, " ")})
	roots := filepath.Join(tmp, "roots")
	writeLines(t, roots, []string{pkgPath})

	paths := irPathIDs{}
	d := irFixture{file: file, emitted: map[string]irFixtureFunc{}, goIdx: irGoIndex{}}
	if err := indexGoIR(t, paths, d.goIdx, irGoProgram{u: u, tests: false, dir: dir, module: dir, reach: ownFuncs(u, map[string]bool{})}); err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, l := range runDump(t, ctx, bin, "-std", stdDir(), "-roots", roots, "-corpus", manifest) {
		kind, rest, _ := strings.Cut(l, " ")
		switch kind {
		case "@pkg":
			files = nil
		case "P":
			_, path, _ := strings.Cut(rest, " ")
			files = append(files, path)
		case "S":
			d.stops = append(d.stops, l)
		case "F":
			f := strings.SplitN(rest, " ", 3)
			var fn irFunc
			if err := json.Unmarshal([]byte(f[2]), &fn); err != nil {
				t.Fatalf("unmarshal IR: %v", err)
			}
			if err := irMapFiles(&fn, files, paths); err != nil {
				t.Fatal(err)
			}
			got, err := json.Marshal(&fn)
			if err != nil {
				t.Fatal(err)
			}
			d.emitted[fn.Name] = irFixtureFunc{irKey{fn.Pos[0], fn.Pos[1], fn.Pos[2]}, got}
		}
	}
	return d
}

// agree fails unless compiler/lower built each named function byte for
// byte as the Go builder did.
func (d irFixture) agree(t *testing.T, label string, names ...string) {
	t.Helper()
	for _, want := range names {
		got, ok := d.emitted[want]
		if !ok {
			t.Fatalf("%s build did not emit %s (stops: %v)", label, want, d.stops)
		}
		res := d.goIdx.compare(got.key, irWitnessSite{d.file, "0", want}, got.data, false, nil)
		if res.match < 0 {
			t.Fatalf("%s %s differed from Go (%s)\nergo: %s\n  go: %v", label, want, res.cause, got.data, res.wants)
		}
	}
}

// stopsHonestly fails if compiler/lower emitted any forbidden function, or
// if a wanted stop cause ends no stop line.
func (d irFixture) stopsHonestly(t *testing.T, forbidden []string, causes ...string) {
	t.Helper()
	for _, name := range forbidden {
		if _, ok := d.emitted[name]; ok {
			t.Fatalf("%s must stop honestly and not emit", name)
		}
	}
	for _, cause := range causes {
		found := false
		for _, s := range d.stops {
			if strings.HasSuffix(s, cause) {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected honest stop %q, got stops: %v", cause, d.stops)
		}
	}
}

// TestIRAgreementTestOnlyOracle kills lower-agree-replace-production-oracle:
// a test build fills a genuine production hole, not a disagreement.
func TestIRAgreementTestOnlyOracle(t *testing.T) {
	const src = `func main() {}
func InTest(n int) int { n + 1 }
test ReachesInTest { assert InTest(2) == 3 }
`
	dir := t.TempDir()
	file := filepath.Join(dir, "main.ergo")
	if err := os.WriteFile(file, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	u, err := irspike.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := irGoProgram{u: u, dir: dir, module: dir, reach: ownFuncs(u, map[string]bool{})}
	paths := irPathIDs{}
	idx := irGoIndex{}
	if err := indexGoIR(t, paths, idx, p); err != nil {
		t.Fatal(err)
	}
	key := irKey{paths.id(file), 2, 1}
	if len(idx[key]) != 0 {
		t.Fatal("fixture must be absent from production IR")
	}
	o := irGoTestOracle{roots: map[string]irGoProgram{dir: p}, cache: map[string]irGoIndex{}}
	o.fill(t, paths, idx, key, file)
	wants, cause := idx.at(key, "InTest")
	if cause != "" {
		t.Fatalf("test build did not supply InTest: %s (at %v; files %v)", cause, key, paths)
	}
	got := wants[0].data
	site := irWitnessSite{path: file, symbol: "InTest"}
	if result := idx.compare(key, site, got, false, nil); result.match < 0 {
		t.Fatal("test-only body was not compared")
	}
	idx[key] = []irGoFunc{{name: site.symbol, data: []byte("different production body")}}
	o.fill(t, paths, idx, key, file)
	if result := idx.compare(key, site, got, false, nil); result.match >= 0 {
		t.Fatal("test-only body hid a production difference")
	}
}

// goIRPlan is the Go side's builds before any is lowered: the programs,
// the folders of their root packages and of the packages built as tests,
// the folders the Go front end does not load, and the test-build oracle.
type goIRPlan struct {
	programs     []irGoProgram
	unlowered    map[string]string
	roots, tests map[string]bool
	testOracle   irGoTestOracle
}

type irLoadedUnit struct {
	u   lower.Unit
	err error
}

func loadIRUnits(dirs []string) []irLoadedUnit {
	out := make([]irLoadedUnit, len(dirs))
	workers := min(max(runtime.GOMAXPROCS(0), 1), len(dirs))
	if workers <= 1 {
		for i, dir := range dirs {
			out[i].u, out[i].err = irspike.Load(dir)
		}
		return out
	}
	var next atomic.Int64
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1) - 1)
				if i >= len(dirs) {
					return
				}
				out[i].u, out[i].err = irspike.Load(dirs[i])
			}
		}()
	}
	wg.Wait()
	return out
}

// goIRPrograms loads each example program, each package no program
// imports (as a test build), the fixtures and the std program for
// goIRFuncs.
func goIRPrograms(t *testing.T, importFixture []string) goIRPlan {
	t.Helper()
	unlowered := map[string]string{}
	var programs []irGoProgram
	testOracle := irGoTestOracle{roots: map[string]irGoProgram{}, cache: map[string]irGoIndex{}, unavailable: map[string]string{}}
	covered := map[string]bool{}
	progDirs := irSlice(irPrograms(t))
	for i, res := range loadIRUnits(progDirs) {
		dir := progDirs[i]
		if res.err != nil {
			t.Fatalf("%s: %v", dir, res.err)
		}
		pr := irGoProgram{u: res.u, dir: dir, module: moduleRoot(dir), reach: ownFuncs(res.u, covered)}
		programs = append(programs, pr)
		testOracle.roots[dir] = pr
	}
	// A package no program imports is lowered in its test build (the IR
	// census's test targets), its functions reached alike.
	_, targets, err := irspike.ExampleTargets(repoRoot())
	if err != nil {
		t.Fatal(err)
	}
	// The corpus's own fixture (testdata/bind) is lowered alike; a folder
	// the Go front end does not load is logged as one it does not lower.
	fixture := filepath.Join(repoRoot(), "internal", "selfhostcheck", "testdata", "bind")
	var targetDirs []string
	for _, dir := range append(append(irSlice(targets), irSlice([]string{fixture})...), importFixture...) {
		if covered[dir] || filepath.Base(filepath.Dir(dir)) == "cmd" {
			continue
		}
		targetDirs = append(targetDirs, dir)
	}
	for i, res := range loadIRUnits(targetDirs) {
		dir := targetDirs[i]
		if covered[dir] {
			continue
		}
		if res.err != nil && dir == fixture {
			unlowered[dir] = res.err.Error()
			continue
		}
		if res.err != nil {
			t.Fatalf("%s: %v", dir, res.err)
		}
		programs = append(programs, irGoProgram{u: res.u, tests: true, dir: dir, module: moduleRoot(dir), reach: ownFuncs(res.u, covered)})
	}
	// Every std body (including the prelude's own Ergo-bodied functions),
	// reached by a program that calls none (the IR census's std domain).
	funcs, err := irspike.StdFuncs()
	if err != nil {
		t.Fatal(err)
	}
	var preludeFuncs []*check.Func
	for _, f := range check.PreludePackage().Funcs {
		if f.Decl != nil && f.Decl.Body != nil {
			preludeFuncs = append(preludeFuncs, f)
		}
	}
	sort.SliceStable(preludeFuncs, func(i, j int) bool {
		return preludeFuncs[i].Name < preludeFuncs[j].Name
	})
	funcs = append(funcs, preludeFuncs...)
	const stdProgramDir = "/ergo-std-program"
	a := sketchfront.Check("func main() {}\n")
	su, err := sketchfront.IRSpikeUnit(a, filepath.Join(stdProgramDir, "std.ergo"))
	if err != nil {
		t.Fatal(err)
	}
	su.Root = stdProgramDir
	programs = append(programs, irGoProgram{u: su, module: stdProgramDir, reach: funcs})
	roots := map[string]bool{}
	tests := map[string]bool{}
	for _, pr := range programs[:len(programs)-1] { // the std program's root is no corpus folder
		dir := filepath.Dir(pr.u.Sources[0].Path)
		roots[dir] = true
		if pr.tests {
			tests[dir] = true
		}
	}
	for dir := range testOracle.roots {
		tests[dir] = true
	}
	return goIRPlan{programs: programs, unlowered: unlowered, roots: roots, tests: tests, testOracle: testOracle}
}

// goIRFuncs lowers each of plan's programs with the Go IR builder and
// indexes its IR files' functions by declaration position, file indices
// mapped to source paths. A test build it refuses is added to
// plan.unlowered.
func goIRFuncs(t *testing.T, paths irPathIDs, plan goIRPlan) irGoIndex {
	t.Helper()
	idx := irGoIndex{}
	unlowered := plan.unlowered
	type irLowerResult struct {
		lowered irLowered
		err     error
	}
	n := len(plan.programs)
	results := make([]irLowerResult, n)
	order := make([]int, 0, n)
	if n > 0 {
		// Schedule the std program first so it overlaps all smaller units.
		order = append(order, n-1)
		for i := 0; i < n-1; i++ {
			order = append(order, i)
		}
	}
	workers := min(max(runtime.GOMAXPROCS(0), 1), n)
	if workers <= 1 {
		for _, k := range order {
			results[k].lowered, results[k].err = irLowerCache.lower(nil, plan.programs[k])
		}
	} else {
		var next atomic.Int64
		var wg sync.WaitGroup
		wg.Add(workers)
		for range workers {
			go func() {
				defer wg.Done()
				for {
					pos := int(next.Add(1) - 1)
					if pos >= len(order) {
						return
					}
					k := order[pos]
					results[k].lowered, results[k].err = irLowerCache.lower(nil, plan.programs[k])
				}
			}()
		}
		wg.Wait()
	}
	if skipped := irLowerCache.unkeptReasons(); len(skipped) > 0 {
		t.Logf("go-lower: %d skipped store: %s", len(skipped), strings.Join(skipped, "; "))
	}
	for i, pr := range plan.programs {
		res := results[i]
		if res.err != nil && pr.tests {
			// A test build the Go lowerer refuses (an internal compiler
			// error of its own) has no IR to compare; it is logged.
			unlowered[pr.dir] = res.err.Error()
			continue
		}
		if res.err != nil {
			t.Fatalf("%s: %v", pr.module, res.err)
		}
		indexLowered(t, paths, idx, pr, res.lowered)
	}
	return idx
}

// irLowered is what the Go builder wrote for one program that the index
// reads: each IR file's bytes and its split's qualified type heads, and
// the split's exported names.
type irLowered struct {
	Files   []irLoweredFile   `json:"files"`
	Exports map[string]string `json:"exports"`
}

type irLoweredFile struct {
	Data  string            `json:"data"`
	Heads map[string]string `json:"heads"`
}

// lowerGoIR lowers pr with the Go IR builder.
func lowerGoIR(pr irGoProgram) (irLowered, error) {
	out, _, split, err := lower.LowerIRSplitReaching(pr.u, pr.tests, pr.reach)
	if err != nil {
		return irLowered{}, err
	}
	l := irLowered{Exports: split.IRExports}
	for _, file := range out {
		if filepath.Base(file.Path) == lower.IRFileName {
			l.Files = append(l.Files, irLoweredFile{Data: string(file.Data), Heads: split.IRTypeHeads[file.Path]})
		}
	}
	return l, nil
}

func indexGoIR(t *testing.T, paths irPathIDs, idx irGoIndex, pr irGoProgram) error {
	t.Helper()
	lowered, err := irLowerCache.lower(t, pr)
	if err != nil {
		return err
	}
	indexLowered(t, paths, idx, pr, lowered)
	return nil
}

// indexLowered adds lowered's functions to idx, positions mapped to paths
// and names to the ones the Ergo side spells.
func indexLowered(t *testing.T, paths irPathIDs, idx irGoIndex, pr irGoProgram, lowered irLowered) {
	t.Helper()
	std := stdDir()
	originals := make(map[string]string, len(lowered.Exports))
	for original, exported := range lowered.Exports {
		originals[exported] = original
	}
	module := pr.module
	for _, file := range lowered.Files {
		var f irFile
		if err := json.Unmarshal([]byte(file.Data), &f); err != nil {
			t.Fatalf("%s: %v", pr.dir, err)
		}
		abs := make([]string, len(f.Files))
		for i, p := range f.Files {
			if rest, ok := strings.CutPrefix(p, "std/"); ok {
				abs[i] = filepath.Join(std, rest)
			} else if filepath.IsAbs(p) {
				abs[i] = p
			} else {
				abs[i] = filepath.Join(module, p)
			}
		}
		for i := range f.Funcs {
			fn := &f.Funcs[i]
			if err := irMapFiles(fn, abs, paths); err != nil {
				t.Fatalf("%s: %s: %v", pr.dir, fn.Name, err)
			}
			raw := fn.Name
			fn.Name = irSplitName(raw)
			irBaseFields(fn, originals, file.Heads)
			data, err := json.Marshal(fn)
			if err != nil {
				t.Fatal(err)
			}
			k := irKey{file: fn.Pos[0], line: fn.Pos[1], col: fn.Pos[2]}
			seen := false
			for _, g := range idx[k] {
				seen = seen || g.name == fn.Name && bytes.Equal(g.data, data)
			}
			if !seen {
				idx[k] = append(idx[k], irGoFunc{name: fn.Name, raw: raw, data: data})
			}
		}
	}
}

// ownFuncs is every function with a body of u's packages and their
// imports that are not std, in import order; covered records their
// folders.
func ownFuncs(u lower.Unit, covered map[string]bool) []*check.Func {
	var out []*check.Func
	seen := map[*check.Package]bool{}
	var walk func(u lower.Unit)
	walk = func(u lower.Unit) {
		for _, i := range u.Imports {
			walk(i)
		}
		p := u.Package
		if p == nil || p.Std() || seen[p] {
			return
		}
		seen[p] = true
		for _, s := range u.Sources {
			covered[filepath.Dir(s.Path)] = true
		}
		for _, f := range p.Funcs {
			if f.Decl != nil && f.Decl.Body != nil {
				out = append(out, f)
			}
		}
	}
	walk(u)
	return out
}

// moduleRoot is the folder of the ergo.mod that holds dir.
func moduleRoot(dir string) string {
	for d := dir; d != filepath.Dir(d); d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "ergo.mod")); err == nil {
			return d
		}
	}
	return dir
}

// irMapFiles sets the file index of every position of fn to its path's id.
func irMapFiles(fn *irFunc, files []string, paths irPathIDs) error {
	var err error
	var walk func(v reflect.Value)
	walk = func(v reflect.Value) {
		switch v.Kind() {
		case reflect.Pointer:
			if !v.IsNil() {
				walk(v.Elem())
			}
		case reflect.Slice:
			for i := range v.Len() {
				walk(v.Index(i))
			}
		case reflect.Struct:
			for i := range v.NumField() {
				walk(v.Field(i))
			}
		case reflect.Array:
			if v.Type() != reflect.TypeFor[irPos]() {
				return
			}
			p := v.Addr().Interface().(*irPos)
			if *p == (irPos{}) {
				return // Go's zero position names no file
			}
			if p[0] < 0 || p[0] >= len(files) {
				err = fmt.Errorf("position %v names no file of %d", *p, len(files))
				return
			}
			p[0] = paths.id(files[p[0]])
		}
	}
	walk(reflect.ValueOf(fn))
	if err != nil {
		return err
	}
	return nil
}
