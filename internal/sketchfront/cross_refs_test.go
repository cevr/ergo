package sketchfront

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/check/snapshot"
)

// TestPathsRoundTrip (CS5b-2, CS5b-4, compile-speed/cs5b.md §3, §7): every
// object a user package's export segment names in an earlier user package
// has an object path, and every syntax node a node path; the path names
// that object or node in the check it was taken from, and the same
// declaration, type or node in a fresh check of the import after an edit
// that leaves the import's interface alone (a new unexported function in
// each imported package, probeFile). So a cached importer's references
// resolve in whichever check of its import is current, and every segment
// decodes.
//
// The export is CHK1b's encoder (snapshot.UserExport), each user segment
// written right after its package's check, as CS5b-3 writes it.
// ERGO_CENSUS_ONLY=<name> runs the examples whose name has it.
//
// Then each user segment is decoded against the fresh check of its
// imports (snapshot.UserExport.Decode), and its interface is the interface of
// the package it was written from.
//
// Mutants (sketchfront ledger): the walk does not follow an interface
// value, so a type held as a Type has no path (cs5b2-walk-skips-interface);
// the resolver steps into the field after the one named
// (cs5b2-resolve-field-off-by-one); a user segment names another user
// segment's objects by id, which a fresh check of that package does not
// hold (cs5b2-encode-ids-not-paths); a user segment names another user
// segment's syntax nodes by id (cs5b4-nodes-by-id); a node path resolves to
// the node after the one named (cs5b4-resolve-index-off-by-one).
func TestPathsRoundTrip(t *testing.T) {
	programs := multiPackageExamples(t)
	if testing.Short() {
		var some []exampleDir
		for _, p := range programs {
			switch p.name {
			case "adventure/cmd/adventure", "backups/cmd/backups", "bookmarks/cmd/bookmarks", "jobs/cmd/jobs", "cron/cmd/cron":
				// cron's main names syntax nodes of cron/schedule (CS5b-4).
				some = append(some, p)
			}
		}
		programs = some
	}
	total := map[string]int{}
	nodes, decoded := 0, 0
	for _, dir := range programs {
		if only := os.Getenv("ERGO_CENSUS_ONLY"); only != "" && !strings.Contains(dir.name, only) {
			continue
		}
		var u *snapshot.UserExport
		var exportErr error
		var order []string
		var loader check.Importer
		first := map[string]*check.Package{}
		checkedHook = func(imp check.Importer, _ string, pkg *check.Package) {
			if pkg == nil || exportErr != nil {
				return
			}
			loader = imp
			order = append(order, pkg.Path)
			first[pkg.Path] = pkg
			if u == nil {
				if u, exportErr = snapshot.NewUserExport(imp); exportErr != nil {
					return
				}
				u.Record = true
			}
			_, exportErr = u.Add(pkg)
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
			// Every program exports, one that imports Go packages too: the
			// export names the program's Go bindings as it names its loader
			// (B7e).
			t.Errorf("%s: %v", dir.name, exportErr)
			continue
		}
		// The fresh check, with the interface-neutral edit.
		second := map[string]*check.Package{}
		checkedHook = func(_ check.Importer, _ string, pkg *check.Package) {
			if pkg != nil {
				second[pkg.Path] = pkg
			}
		}
		b, err := CheckDirFS(probeFS{root: dir.dir}, dir.dir, a.program.platform)
		checkedHook = nil
		if err != nil {
			t.Fatal(err)
		}
		if len(b.Diagnostics) > 0 {
			t.Fatalf("%s with %s does not check: %v", dir.name, probeFile, b.Diagnostics[0])
		}
		for path, pkg := range second {
			if path == "main" {
				continue
			}
			probed := false
			for _, f := range pkg.Funcs {
				probed = probed || f.Name == "aaCs5bProbe"
			}
			if !probed {
				t.Fatalf("%s: the fresh check of %s has no %s", dir.name, path, probeFile)
			}
		}
		var bad []string
		report := func(format string, args ...any) {
			if len(bad) < 8 {
				bad = append(bad, fmt.Sprintf(format, args...))
			}
		}
		for _, r := range u.Refs {
			if r.Obj == nil {
				nodes++
				if !r.HasPath {
					report("%s -> %s: a syntax node with no node path", r.From, r.To)
					continue
				}
				total["node "+reflect.TypeOf(r.Node).String()]++
				if x, ok := snapshot.ResolveNodePath(first[r.To], r.Path); !ok || x != r.Node {
					report("%s -> %s: %s resolves to %s in its own check", r.From, r.To, describeObj(r.Node), describeObj(x))
				} else if y, ok := snapshot.ResolveNodePath(second[r.To], r.Path); !ok || describeObj(y) != describeObj(r.Node) {
					report("%s -> %s: %s names %s in the fresh check", r.From, r.To, describeObj(r.Node), describeObj(y))
				}
				continue
			}
			v := reflect.ValueOf(r.Obj)
			total[v.Type().String()]++
			if !r.HasPath {
				report("%s -> %s: %s has no path", r.From, r.To, describeObj(r.Obj))
				continue
			}
			if x, ok := check.ResolvePath(first[r.To], r.Path); !ok || x != r.Obj {
				report("%s -> %s: %s at %s resolves to %s in its own check", r.From, r.To, describeObj(r.Obj), check.PathString(r.Path), describeObj(x))
				continue
			}
			y, ok := check.ResolvePath(second[r.To], r.Path)
			if !ok {
				report("%s -> %s: %s at %s names nothing in the fresh check", r.From, r.To, describeObj(r.Obj), check.PathString(r.Path))
			} else if describeObj(y) != describeObj(r.Obj) {
				report("%s -> %s: %s at %s names %s in the fresh check", r.From, r.To, describeObj(r.Obj), check.PathString(r.Path), describeObj(y))
			}
		}
		// Each segment, read against the fresh check of its imports, is the
		// package it was written from, as an importer sees it.
		for _, path := range order {
			pkg, err := u.Decode(path, func(p string) *check.Package { return second[p] }, loader)
			if err != nil {
				report("%s: decoding against the fresh check: %v", path, err)
				continue
			}
			decoded++
			if got, want := check.InterfaceText(pkg), check.InterfaceText(first[path]); got != want {
				report("%s: decoded against the fresh check, its interface is\n%s\nwant\n%s", path, got, want)
			}
		}
		if len(bad) > 0 {
			t.Errorf("%s:\n  %s", dir.name, strings.Join(bad, "\n  "))
		}
	}
	var rows []string
	for typ, n := range total {
		rows = append(rows, fmt.Sprintf("  %-22s %6d", typ, n))
	}
	sort.Strings(rows)
	t.Logf("references into earlier user segments, by type:\n%s\nsyntax nodes: %d; segments decoded: %d", strings.Join(rows, "\n"), nodes, decoded)
}

// describeObj names an object for comparison across checks: its type and
// its text (a type's String) or its Name.
func describeObj(x any) string {
	if x == nil {
		return "nothing"
	}
	s := reflect.TypeOf(x).String()
	if st, ok := x.(fmt.Stringer); ok {
		return s + " " + st.String()
	}
	if v := reflect.ValueOf(x); v.Kind() == reflect.Pointer && v.Elem().Kind() == reflect.Struct {
		if f := v.Elem().FieldByName("Name"); f.IsValid() && f.Kind() == reflect.String {
			return s + " " + f.String()
		}
	}
	return s
}

// probeFile is the file probeFS adds to every package folder of the
// program but its main (cmd) folder: an unexported function, which no
// importer sees.
const probeFile = "aa_cs5b_probe.ergo"

const probeText = "func aaCs5bProbe() int {\n\t0\n}\n"

// probeFS is the host file system with probeFile in each folder that
// holds .ergo files, except the program's main folder (root) and cmd
// folders.
type probeFS struct{ root string }

func (p probeFS) probed(dir string) bool {
	return filepath.Clean(dir) != filepath.Clean(p.root) && !strings.Contains(dir, string(filepath.Separator)+"cmd"+string(filepath.Separator))
}

func (p probeFS) ReadDir(dir string) ([]fs.DirEntry, error) {
	es, err := os.ReadDir(dir)
	if err != nil || !p.probed(dir) {
		return es, err
	}
	for _, e := range es {
		if strings.HasSuffix(e.Name(), ".ergo") {
			return append(es, fs.FileInfoToDirEntry(probeInfo{})), nil
		}
	}
	return es, nil
}

func (p probeFS) ReadFile(name string) ([]byte, error) {
	if filepath.Base(name) == probeFile {
		return []byte(probeText), nil
	}
	return hostFS{}.ReadFile(name)
}

func (p probeFS) Stat(name string) (fs.FileInfo, error) {
	if filepath.Base(name) == probeFile {
		return probeInfo{}, nil
	}
	return os.Stat(name)
}

func (p probeFS) Lstat(name string) (fs.FileInfo, error) {
	if filepath.Base(name) == probeFile {
		return probeInfo{}, nil
	}
	return os.Lstat(name)
}

type probeInfo struct{}

func (probeInfo) Name() string       { return probeFile }
func (probeInfo) Size() int64        { return int64(len(probeText)) }
func (probeInfo) Mode() fs.FileMode  { return 0o644 }
func (probeInfo) ModTime() time.Time { return time.Time{} }
func (probeInfo) IsDir() bool        { return false }
func (probeInfo) Sys() any           { return nil }
