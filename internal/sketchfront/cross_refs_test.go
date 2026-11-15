package sketchfront

import (
	"fmt"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestCrossRefsHavePaths (CS5b-2 step 1, compile-speed/cs5b.md §3, §7):
// every object a user package's export segment names in an earlier user
// package is reached by a path from that package's public state, so the
// reference can be written as a path and resolved in whichever check of
// the import is current. The export is CHK1b's encoder over std and the
// program's packages (check.UserSegmentRefs); the paths are the walk of
// pathWalk, which leaves out the fields no path may step through
// (notPath).
//
// ERGO_CENSUS_ONLY=<name> runs the examples whose name has it.
func TestCrossRefsHavePaths(t *testing.T) {
	programs := multiPackageExamples(t)
	if testing.Short() {
		var some []exampleDir
		for _, p := range programs {
			switch p.name {
			case "adventure/cmd/adventure", "backups/cmd/backups", "bookmarks/cmd/bookmarks", "jobs/cmd/jobs":
				some = append(some, p)
			}
		}
		programs = some
	}
	total := map[string]int{}
	unexported, nodes := 0, 0
	keyed, indexed, heads := map[string]int{}, map[string]int{}, map[string]int{}
	missing := map[string]int{}
	var examples []string
	for _, dir := range programs {
		if only := os.Getenv("ERGO_CENSUS_ONLY"); only != "" && !strings.Contains(dir.name, only) {
			continue
		}
		var pkgs []*check.Package
		var u *check.UserExport
		var exportErr error
		// Each package's segment is written right after its own check, as
		// CS5b-3 writes it.
		checkedHook = func(imp check.Importer, _ string, pkg *check.Package) {
			if pkg == nil || exportErr != nil {
				return
			}
			pkgs = append(pkgs, pkg)
			if u == nil {
				u, exportErr = check.NewUserExport(imp)
				if exportErr != nil {
					return
				}
			}
			exportErr = u.Add(pkg)
		}
		a, err := CheckDir(dir.dir)
		checkedHook = nil
		if err != nil {
			t.Fatal(err)
		}
		if len(a.Diagnostics) > 0 {
			t.Fatalf("%s does not check: %v", dir.name, a.Diagnostics[0])
		}
		if exportErr != nil {
			// The cache misses on a package the codec cannot carry: no
			// reference of it needs a path.
			t.Logf("%s: not exported: %v", dir.name, exportErr)
			unexported++
			continue
		}
		refs := u.Refs
		byPath := map[string]*check.Package{}
		for _, p := range pkgs {
			byPath[p.Path] = p
		}
		paths := map[string]map[uintptr]string{}
		var lines []string
		for _, r := range refs {
			if r.Obj == nil {
				nodes++
				continue
			}
			v := reflect.ValueOf(r.Obj)
			typ := v.Type().String()
			total[typ]++
			ps, ok := paths[r.To]
			if !ok {
				ps = pathWalk(byPath[r.To])
				paths[r.To] = ps
			}
			if v.Kind() != reflect.Pointer {
				continue
			}
			if p, ok := ps[v.Pointer()]; ok {
				// A path through a map keyed by an object names that key by its
				// address, which another process does not share: CS5b-2 names
				// such a key by its own path.
				if strings.Contains(p, "#") {
					keyed[typ]++
				}
				if strings.Contains(p, "[") {
					indexed[typ]++
				}
				// The package field a path starts at: Scope names a
				// declaration; any other is a list in source order.
				head := strings.TrimPrefix(p, "pkg.")
				if i := strings.IndexAny(head, ".[{"); i >= 0 {
					head = head[:i]
				}
				heads[head]++
			} else {
				missing[typ]++
				if len(lines) < 6 {
					lines = append(lines, fmt.Sprintf("  %s -> %s: %s", r.From, r.To, label(v)))
				}
			}
		}
		if len(lines) > 0 {
			examples = append(examples, dir.name+":\n"+strings.Join(lines, "\n"))
		}
	}
	var rows []string
	for typ, n := range total {
		rows = append(rows, fmt.Sprintf("  %-22s %6d refs, %6d with no path, %6d through an object key, %6d through an index", typ, n, missing[typ], keyed[typ], indexed[typ]))
	}
	sort.Strings(rows)
	t.Logf("references into earlier user segments, by type:\n%s\nsyntax nodes: %d; programs not exported: %d; paths by the package field they start at: %v", strings.Join(rows, "\n"), nodes, unexported, heads)
	if len(examples) > 0 {
		t.Errorf("objects named across user segments with no path:\n%s", strings.Join(examples, "\n"))
	}
}

// notPath are the fields a path never steps through: state that no
// importer sees by name (a package's check results, a checker, the
// identity table), and the lazy caches of census.md.
var notPath = map[string]bool{
	"check.Package.Info":      true,
	"check.Named.ctx":         true,
	"check.Struct.index":      true,
	"check.Interface.index":   true,
	"check.Package.LawRuns":   true,
	"check.Package.cellConvs": true,
}

// pathWalk is the set of objects reached from pkg through fields a path
// may step through, by address, with the first path to each. It stops at
// other packages, syntax nodes, checkers, identity tables and importers.
func pathWalk(pkg *check.Package) map[uintptr]string {
	out := map[uintptr]string{}
	root := reflect.ValueOf(pkg).Pointer()
	var walk func(v reflect.Value, path string)
	walk = func(v reflect.Value, path string) {
		switch v.Kind() {
		case reflect.Pointer:
			if v.IsNil() {
				return
			}
			p := v.Pointer()
			if _, ok := out[p]; ok {
				return
			}
			el := v.Type().Elem()
			if el.PkgPath() == syntaxPkg || el.PkgPath() == "sync" || el.Name() == "checker" || el.Name() == "typeTable" || el.Name() == "stdImporter" || el.Name() == "loader" {
				return
			}
			if el.Name() == "Package" && strings.HasSuffix(el.PkgPath(), "sketch/check") && p != root {
				return
			}
			out[p] = path
			walk(v.Elem(), path)
		case reflect.Interface:
			if !v.IsNil() {
				walk(v.Elem(), path)
			}
		case reflect.Struct:
			t := v.Type()
			for i := range t.NumField() {
				name := t.Field(i).Name
				if notPath[t.String()+"."+name] {
					continue
				}
				walk(v.Field(i), path+"."+name)
			}
		case reflect.Slice, reflect.Array:
			for i := range v.Len() {
				walk(v.Index(i), path+"["+strconv.Itoa(i)+"]")
			}
		case reflect.Map:
			keys := v.MapKeys()
			sort.Slice(keys, func(i, j int) bool { return keyLabel(keys[i]) < keyLabel(keys[j]) })
			for _, k := range keys {
				s := "{" + keyLabel(k) + "}"
				walk(k, path+s+"#key")
				walk(v.MapIndex(k), path+s)
			}
		}
	}
	walk(reflect.ValueOf(pkg), "pkg")
	return out
}

// label is a pointer's type, with its Name field when it has one.
func label(p reflect.Value) string {
	s := p.Type().String()
	if el := p.Elem(); el.Kind() == reflect.Struct {
		if f := el.FieldByName("Name"); f.IsValid() && f.Kind() == reflect.String {
			s += "(" + f.String() + ")"
		}
	}
	return s
}
