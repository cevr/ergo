package v1

import (
	"errors"
	"fmt"
	"go/ast"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/std"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestOSArgs: os.Host's Args body is the arguments after the program name,
// in a slice that does not alias os.Args.
func TestOSArgs(t *testing.T) {
	saved := os.Args
	t.Cleanup(func() { os.Args = saved })
	cases := []struct {
		argv []string
		want []string
	}{
		{[]string{"prog", "a", "b"}, []string{"a", "b"}},
		{[]string{"prog", ""}, []string{""}},
		{[]string{"prog"}, []string{}},
		{[]string{}, []string{}},
		{nil, []string{}},
	}
	for _, c := range cases {
		os.Args = c.argv
		got := OSArgs()
		if got == nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("OSArgs() with os.Args %q = %#v; want %#v", c.argv, got, c.want)
		}
	}
	os.Args = []string{"prog", "a"}
	got := OSArgs()
	got[0] = "changed"
	if os.Args[1] != "a" {
		t.Errorf("OSArgs aliases os.Args: os.Args[1] = %q after a write to the result", os.Args[1])
	}
}

// TestOSEnv: os.Host's Getenv bodies tell a variable set to the empty
// string (Some("")) from an unset one (None).
func TestOSEnv(t *testing.T) {
	const set, empty, unset = "ERGO_V1_HOST_SET", "ERGO_V1_HOST_EMPTY", "ERGO_V1_HOST_UNSET"
	t.Setenv(set, "v")
	t.Setenv(empty, "")
	t.Setenv(unset, "x")
	if err := os.Unsetenv(unset); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		key   string
		has   bool
		value string
	}{
		{set, true, "v"},
		{empty, true, ""},
		{unset, false, ""},
	}
	for _, c := range cases {
		if got := OSHasEnv(c.key); got != c.has {
			t.Errorf("OSHasEnv(%s) = %v; want %v", c.key, got, c.has)
		}
		if got := OSGetenv(c.key); got != c.value {
			t.Errorf("OSGetenv(%s) = %q; want %q", c.key, got, c.value)
		}
	}
}

// TestExitText: os.Exit's text, which report writes when it is not main's
// only reason, is its Ergo name and fields as the lowering writes a
// record's.
func TestExitText(t *testing.T) {
	got := Exit{Code: 3, Message: "[NOT_FOUND] gone"}.Error()
	if want := "os.Exit{Code:3 Message:[NOT_FOUND] gone}"; got != want {
		t.Errorf("Exit.Error() = %q; want %q", got, want)
	}
}

// TestFSBodies: std fs's bodies read the host file system and give each
// failure kind its record: NotFound for a missing path, Denied for a file
// the process may not read, Failed with the host's reason, without the
// path, otherwise. ReadDir's names are sorted.
func TestFSBodies(t *testing.T) {
	dir := t.TempDir()
	at := func(name string) string { return filepath.Join(dir, name) }
	if err := os.WriteFile(at("a.md"), []byte("alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(at("sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	var want []string
	for i := 0; i < 20; i++ {
		want = append(want, fmt.Sprintf("n%02d", i))
	}
	for i := 19; i >= 0; i-- {
		if err := os.WriteFile(filepath.Join(at("sub"), want[(i*7)%20]), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if text, err := FSReadFile(at("a.md")); text != "alpha\n" || err != nil {
		t.Errorf("FSReadFile(a.md) = %q, %v", text, err)
	}
	if names, err := FSReadDir(at("sub")); !reflect.DeepEqual(names, want) || err != nil {
		t.Errorf("FSReadDir(sub) = %q, %v; want %q", names, err, want)
	}
	if names, err := FSReadDir(at("sub/n00")); names != nil || err != (Failed{Path: at("sub/n00"), Reason: "not a directory"}) {
		t.Errorf("FSReadDir(sub/n00) = %q, %#v", names, err)
	}
	if info, err := FSStat(at("a.md")); info != (Info{Name: "a.md", Size: 6, Dir: false}) || err != nil {
		t.Errorf("FSStat(a.md) = %#v, %v", info, err)
	}
	if info, err := FSStat(at("sub")); info.Name != "sub" || !info.Dir || err != nil {
		t.Errorf("FSStat(sub) = %#v, %v", info, err)
	}
	missing := at("missing.md")
	if _, err := FSReadFile(missing); err != (NotFound{Path: missing}) {
		t.Errorf("FSReadFile(missing) error = %#v", err)
	}
	if _, err := FSReadDir(missing); err != (NotFound{Path: missing}) {
		t.Errorf("FSReadDir(missing) error = %#v", err)
	}
	if _, err := FSStat(missing); err != (NotFound{Path: missing}) {
		t.Errorf("FSStat(missing) error = %#v", err)
	}
	if _, err := FSReadFile(at("sub")); err != (Failed{Path: at("sub"), Reason: "is a directory"}) {
		t.Errorf("FSReadFile(sub) error = %#v", err)
	}
	if os.Geteuid() != 0 {
		if err := os.WriteFile(at("b.md"), []byte("beta\n"), 0); err != nil {
			t.Fatal(err)
		}
		if _, err := FSReadFile(at("b.md")); err != (Denied{Path: at("b.md")}) {
			t.Errorf("FSReadFile(b.md) error = %#v", err)
		}
	}
	// An error that is not a *fs.PathError keeps its whole text.
	if err := fsErr("p", errors.New("odd")); err != (Failed{Path: "p", Reason: "odd"}) {
		t.Errorf("fsErr(odd) = %#v", err)
	}
}

// TestFSFailureText: each fs failure's text, which report writes for an
// uncaught one, is its Ergo name and fields as the lowering writes a
// record's.
func TestFSFailureText(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{NotFound{Path: "missing.md"}, "fs.NotFound{Path:missing.md}"},
		{Denied{Path: "b.md"}, "fs.Denied{Path:b.md}"},
		{Failed{Path: "sub", Reason: "is a directory"}, "fs.Failed{Path:sub Reason:is a directory}"},
	}
	for _, c := range cases {
		if got := c.err.Error(); got != c.want {
			t.Errorf("Error() = %q; want %q", got, c.want)
		}
	}
}

// recordOwners maps each record name (a type with fields, or none, in
// braces) that a std package other than the prelude declares to the
// packages that declare it. The lowering binds a std record to the v1
// struct of its bare name (lower/std.go runtimeType; stdlib.md, Types 4).
func recordOwners(t *testing.T, packages map[string]map[string]string) map[string][]string {
	t.Helper()
	owners := map[string][]string{}
	for path, files := range packages {
		for name, src := range files {
			f, err := syntax.Parse(src)
			if err != nil {
				t.Fatalf("%s/%s: %v", path, name, err)
			}
			for _, d := range f.Decls {
				if td, ok := d.(*syntax.TypeDecl); ok {
					if _, ok := td.Spec.(*syntax.StructType); ok {
						owners[td.Name.Name] = append(owners[td.Name.Name], path)
					}
				}
			}
		}
	}
	return owners
}

// structClashes lists each exported v1 struct type that more than one std
// record of owners would lower to.
func structClashes(t *testing.T, owners map[string][]string) []string {
	t.Helper()
	decls, err := sourceDecls()
	if err != nil {
		t.Fatal(err)
	}
	var clashes []string
	for name, g := range decls {
		if g.typ == nil {
			continue
		}
		if _, ok := g.typ.Type.(*ast.StructType); !ok {
			continue
		}
		if ps := owners[name]; len(ps) > 1 {
			sort.Strings(ps)
			clashes = append(clashes, name+" "+strings.Join(ps, ","))
		}
	}
	sort.Strings(clashes)
	return clashes
}

// stdPackages is every std package's sources but the prelude's.
func stdPackages(t *testing.T) map[string]map[string]string {
	t.Helper()
	out := map[string]map[string]string{}
	for _, path := range std.Packages() {
		files, ok := std.Package(path)
		if !ok {
			t.Fatalf("std lists %s but does not serve it", path)
		}
		out[path] = files
	}
	return out
}

// TestStructHasOneStdRecord is the guard of the bare-name binding (plan
// std-1 2.3): each exported struct of runtime/v1 is the Go type of at most
// one std record, so fs.NotFound and a later exec.NotFound cannot both
// lower to v1.NotFound. A fixture package that declares a second NotFound
// is found.
func TestStructHasOneStdRecord(t *testing.T) {
	packages := stdPackages(t)
	if clashes := structClashes(t, recordOwners(t, packages)); len(clashes) != 0 {
		t.Errorf("v1 structs that several std records lower to: %q", clashes)
	}
	packages["fixture/exec"] = map[string]string{"exec.ergo": "type NotFound { Path string }\n"}
	if got, want := structClashes(t, recordOwners(t, packages)), []string{"NotFound fixture/exec,fs"}; !reflect.DeepEqual(got, want) {
		t.Errorf("with a second NotFound: clashes %q; want %q", got, want)
	}
}
