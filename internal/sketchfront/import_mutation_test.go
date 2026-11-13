package sketchfront

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestImporterLeavesImportUnchanged (CS5b-1, compile-speed/cs5b.md §5):
// checking a package changes nothing in the packages it imports, so a
// cached check of an importer can be reused beside a fresh check of its
// imports, and skipping an importer's check leaves its imports as the
// check would have. For every package of each multi-package example, the
// test records every object reachable from the checked package right
// after its own check, field by field, and again after the whole program
// is checked, and requires the two records to be equal.
//
// Objects are named by type and address (both records are taken in one
// process, and the package keeps every object alive), so a map is read in
// a fixed order and a change names the object and field that changed. The
// importer (the loader, std's importer) is where the walk stops: the
// codec names it as an external.
//
// A failure groups the changed fields by type and field (the census) and
// prints the first few, each with where the object was first reached.
// ERGO_CENSUS_ONLY=<name> runs the examples whose name has it.
//
// Mutant (sketchfront ledger): the loader shows the test every checked
// package (cs5b1-hook-skipped). The census found no write outside
// lazyState, so no product mutant is unique to it yet: it is the guard
// CS5b-2 and CS6 build on (cs5b.md §5, §7).
func TestImporterLeavesImportUnchanged(t *testing.T) {
	programs := multiPackageExamples(t)
	if testing.Short() && len(programs) > 4 {
		programs = programs[:4]
	}
	for _, dir := range programs {
		if only := os.Getenv("ERGO_CENSUS_ONLY"); only != "" && !strings.Contains(dir.name, only) {
			continue
		}
		t.Run(dir.name, func(t *testing.T) {
			var order []string
			pkgs := map[string]*check.Package{}
			before := map[string]*graph{}
			checkedHook = func(path string, pkg *check.Package) {
				if pkg == nil {
					return
				}
				order = append(order, path)
				pkgs[path] = pkg
				before[path] = recordGraph(pkg)
			}
			defer func() { checkedHook = nil }()
			a, err := CheckDir(dir.dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(a.Diagnostics) > 0 {
				t.Fatalf("the example does not check: %v", a.Diagnostics[0])
			}
			if len(order) < 2 {
				t.Fatalf("the hook saw %d checked packages; the example has a cmd and a package beside it", len(order))
			}
			// The last package is main: nothing is checked after it.
			for _, path := range order[:len(order)-1] {
				after := recordGraph(pkgs[path])
				diffs := lazyFree(graphDiff(before[path], after))
				if len(diffs) == 0 {
					continue
				}
				limit := min(8, len(diffs))
				t.Errorf("checking the importers of %s changed %d fields of its graph; by type and field:\n%s\nfirst:\n%s", path, len(diffs), fieldCounts(diffs), strings.Join(diffs[:limit], "\n"))
			}
		})
	}
}

type exampleDir struct{ name, dir string }

// multiPackageExamples are the examples whose program has more than one
// package of its own (a module with a cmd folder and a package beside it).
func multiPackageExamples(t *testing.T) []exampleDir {
	t.Helper()
	root := "../../examples"
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var out []exampleDir
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		cmds, _ := os.ReadDir(filepath.Join(root, e.Name(), "cmd"))
		for _, c := range cmds {
			if c.IsDir() {
				abs, err := filepath.Abs(filepath.Join(root, e.Name(), "cmd", c.Name()))
				if err != nil {
					t.Fatal(err)
				}
				out = append(out, exampleDir{name: e.Name() + "/cmd/" + c.Name(), dir: abs})
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("no example with a cmd folder")
	}
	return out
}

// graph is a record of the objects reachable from a root: fields maps
// "object.field" to its text, where says where each object was first
// reached from the root.
type graph struct {
	fields map[string]string
	where  map[string]string
}

// lazyState are the fields an importer's check may write in an object of
// its import, each with its reason (a counter's starts "counter:"):
// state computed on first use from the object's final state, which no output reads and which a later use
// computes the same, so an importer that is reused rather than checked
// leaves nothing missing that matters (compile-speed/cs5b.md §5).
var lazyState = map[string]string{
	"*check.Struct.index":    "the field lookup's cache, built from the final Fields (types.go, Struct.Lookup)",
	"*check.Interface.index": "the method lookup's cache, built from the final Methods (types.go, Interface.Lookup)",
	// The identity table interns on first use; ids are compared within one
	// table only (intern.go), and its counters are the tests' measures.
	"*check.typeTable.of":           "the identity table's memo by type object (intern.go)",
	"*check.typeTable.nodes":        "the identity table's structure → id (intern.go)",
	"*check.typeTable.sizes":        "the identity table's sizes by id (intern.go)",
	"*check.typeTable.foreign":      "the identity table's re-interned foreign rows (intern.go)",
	"*check.typeTable.resolved":     "the identity table's resolved rows (intern.go)",
	"*check.typeTable.work":         "counter: the identity work counter (intern.go)",
	"*check.typeTable.lookupBuilds": "counter: a test counter (intern.go)",
	"*check.typeTable.sigBuilds":    "counter: a test counter (intern.go)",
	"*check.typeTable.sortCmps":     "counter: a test counter (intern.go)",
	// An instance belongs to its origin's checker (cs5b.md §4), which
	// makes it on first use and expands its underlying type on demand.
	"*check.checker.instance":      "the origin's instance table: an instance is made once, on first use",
	"*check.checker.instancesMade": "counter: the count of instances the origin's table made",
	"*check.Named.under":           "an instance's underlying type, expanded on first use from its origin and type arguments",
}

// lazyFree drops the changes lazyState allows, and the fields of objects
// the importers made (an object is new when no field of it was recorded
// before; the field that reaches it is a change of an older object).
func lazyFree(diffs []string) []string {
	var out []string
	for _, d := range diffs {
		if strings.Contains(d, "(new, at ") {
			continue
		}
		typ, rest, _ := strings.Cut(d, "#")
		_, field, _ := strings.Cut(rest, ".")
		if i := strings.IndexAny(field, ".[{<: #"); i >= 0 {
			field = field[:i]
		}
		reason := lazyState[typ+"."+field]
		// A counter may change; other lazy state is only filled: a nil
		// becomes a value, a map or slice gains entries, and no value
		// that was there changes.
		filled := strings.Contains(d, ": nil -> (gone)") || strings.Contains(d, ": (new) ") || strings.Contains(d, "#len: ")
		if reason != "" && (strings.HasPrefix(reason, "counter:") || filled) {
			continue
		}
		out = append(out, d)
	}
	return out
}

// graphDiff is the sorted changed fields of b against a.
func graphDiff(a, b *graph) []string {
	var out []string
	at := func(k string) string {
		typ, rest, _ := strings.Cut(k, "#")
		addr, _, _ := strings.Cut(rest, ".")
		obj := typ + "#" + addr
		if w, ok := a.where[obj]; ok {
			return " (at " + w + ")"
		}
		if w, ok := b.where[obj]; ok {
			return " (new, at " + w + ")"
		}
		return ""
	}
	for k, v := range a.fields {
		if w, ok := b.fields[k]; !ok {
			out = append(out, k+": "+v+" -> (gone)"+at(k))
		} else if v != w {
			out = append(out, k+": "+v+" -> "+w+at(k))
		}
	}
	for k, w := range b.fields {
		if _, ok := a.fields[k]; !ok {
			out = append(out, k+": (new) "+w+at(k))
		}
	}
	sort.Strings(out)
	return out
}

// fieldCounts groups changed fields by object type and first field name,
// the census's rows, most first.
func fieldCounts(diffs []string) string {
	n := map[string]int{}
	for _, d := range diffs {
		typ, rest, _ := strings.Cut(d, "#")
		_, field, _ := strings.Cut(rest, ".")
		if i := strings.IndexAny(field, ".[{<: #"); i >= 0 {
			field = field[:i]
		}
		n[typ+"."+field]++
	}
	type row struct {
		k string
		n int
	}
	var rows []row
	for k, v := range n {
		rows = append(rows, row{k, v})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].n > rows[j].n || rows[i].n == rows[j].n && rows[i].k < rows[j].k })
	var out []string
	for _, r := range rows {
		out = append(out, fmt.Sprintf("  %6d %s", r.n, r.k))
	}
	return strings.Join(out, "\n")
}

// recordGraph records every object reachable from root. Syntax nodes,
// which no check writes, and the importers are named, not walked.
func recordGraph(root any) *graph {
	d := &grapher{g: &graph{fields: map[string]string{}, where: map[string]string{}}, seen: map[string]bool{}}
	d.walk(reflect.ValueOf(root), "root", "pkg")
	return d.g
}

type grapher struct {
	g    *graph
	seen map[string]bool
}

const syntaxPkg = "ergo.local/prototype/internal/sketch/syntax"

// name is a pointer's object name: its type and address.
func name(v reflect.Value) string {
	return v.Type().String() + "#" + strconv.FormatUint(uint64(v.Pointer()), 16)
}

// opaque reports the pointer types the walk names and does not enter.
func opaque(t reflect.Type) bool {
	el := t.Elem()
	return el.PkgPath() == syntaxPkg || el.PkgPath() == "sync" || el.Name() == "stdImporter" || el.Name() == "loader"
}

// walk records v at path (an object's field path), and where is the path
// from the root that reached it, for messages.
func (d *grapher) walk(v reflect.Value, path, where string) {
	out := d.g.fields
	switch v.Kind() {
	case reflect.Invalid:
		out[path] = "invalid"
	case reflect.Pointer:
		if v.IsNil() {
			out[path] = "nil"
			return
		}
		n := name(v)
		out[path] = "&" + n
		if d.seen[n] || opaque(v.Type()) {
			return
		}
		d.seen[n] = true
		d.g.where[n] = where
		d.walk(v.Elem(), n, where)
	case reflect.Interface:
		if v.IsNil() {
			out[path] = "nil"
			return
		}
		d.walk(v.Elem(), path+"<"+v.Elem().Type().String()+">", where)
	case reflect.Struct:
		t := v.Type()
		if t.PkgPath() == "sync" || t.PkgPath() == "sync/atomic" {
			return
		}
		for i := range t.NumField() {
			f := t.Field(i).Name
			d.walk(v.Field(i), path+"."+f, where+"."+f)
		}
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.IsNil() {
			out[path] = "nil"
			return
		}
		out[path+"#len"] = strconv.Itoa(v.Len())
		for i := range v.Len() {
			s := "[" + strconv.Itoa(i) + "]"
			d.walk(v.Index(i), path+s, where+s)
		}
	case reflect.Map:
		if v.IsNil() {
			out[path] = "nil"
			return
		}
		out[path+"#len"] = strconv.Itoa(v.Len())
		type entry struct {
			label string
			k     reflect.Value
		}
		var es []entry
		for _, k := range v.MapKeys() {
			es = append(es, entry{keyLabel(k), k})
		}
		sort.Slice(es, func(i, j int) bool { return es[i].label < es[j].label })
		for _, e := range es {
			s := "{" + e.label + "}"
			d.walk(v.MapIndex(e.k), path+s, where+s)
			// A pointer key is an object reached too.
			if e.k.Kind() == reflect.Pointer || e.k.Kind() == reflect.Interface {
				d.walk(e.k, path+s+"#key", where+s+"#key")
			}
		}
	case reflect.Func:
		if v.IsNil() {
			out[path] = "nil"
		} else {
			out[path] = "func"
		}
	case reflect.Chan, reflect.UnsafePointer:
		out[path] = v.Kind().String()
	case reflect.Bool:
		out[path] = strconv.FormatBool(v.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		out[path] = strconv.FormatInt(v.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		out[path] = strconv.FormatUint(v.Uint(), 10)
	case reflect.Float32, reflect.Float64:
		out[path] = strconv.FormatFloat(v.Float(), 'g', -1, 64)
	case reflect.Complex64, reflect.Complex128:
		out[path] = fmt.Sprint(v.Complex())
	case reflect.String:
		out[path] = strconv.Quote(v.String())
	}
}

// keyLabel names a map key: a pointer by its object name, any other key
// by its text.
func keyLabel(k reflect.Value) string {
	switch k.Kind() {
	case reflect.Interface:
		if k.IsNil() {
			return "nil"
		}
		return keyLabel(k.Elem())
	case reflect.Pointer:
		if k.IsNil() {
			return "nil"
		}
		return name(k)
	case reflect.Struct:
		var parts []string
		for i := range k.NumField() {
			parts = append(parts, keyLabel(k.Field(i)))
		}
		return k.Type().Name() + "(" + strings.Join(parts, ",") + ")"
	case reflect.Array:
		var parts []string
		for i := range k.Len() {
			parts = append(parts, keyLabel(k.Index(i)))
		}
		return "[" + strings.Join(parts, ",") + "]"
	case reflect.String:
		return strconv.Quote(k.String())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(k.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return strconv.FormatUint(k.Uint(), 10)
	case reflect.Bool:
		return strconv.FormatBool(k.Bool())
	case reflect.Float32, reflect.Float64:
		return strconv.FormatFloat(k.Float(), 'g', -1, 64)
	}
	return k.Type().String()
}
