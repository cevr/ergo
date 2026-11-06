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
	// Stat follows symbolic links (os.Stat, not os.Lstat): a link to a
	// file gives the target's size, a link to a directory is a directory,
	// a dangling link is NotFound. Name is the link's own name.
	for _, l := range []struct{ link, target string }{{"link-a", "a.md"}, {"link-sub", "sub"}, {"link-gone", "gone.md"}} {
		if err := os.Symlink(l.target, at(l.link)); err != nil {
			t.Fatal(err)
		}
	}
	if info, err := FSStat(at("link-a")); info != (Info{Name: "link-a", Size: 6, Dir: false}) || err != nil {
		t.Errorf("FSStat(link-a) = %#v, %v; want the target's size 6", info, err)
	}
	if info, err := FSStat(at("link-sub")); info.Name != "link-sub" || !info.Dir || err != nil {
		t.Errorf("FSStat(link-sub) = %#v, %v; want a directory", info, err)
	}
	if _, err := FSStat(at("link-gone")); err != (NotFound{Path: at("link-gone")}) {
		t.Errorf("FSStat(link-gone) error = %#v; want NotFound", err)
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

// TestFSWriteFileStopsBeforeRename: a write that stops after the new file
// is complete and before the rename, as a crash there would, leaves the
// old file whole, removes the new file, and is Failed at the target path.
// The new file is made in the directory of the target, so the rename does
// not cross a device.
func TestFSWriteFileStopsBeforeRename(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "x.md")
	if err := os.WriteFile(target, []byte("old"), 0o640); err != nil {
		t.Fatal(err)
	}
	var seen string
	beforeRename = func(tmp string) error {
		seen = tmp
		if data, err := os.ReadFile(tmp); string(data) != "new" || err != nil {
			t.Errorf("the new file holds %q, %v before the rename; want %q", data, err, "new")
		}
		return errors.New("stopped")
	}
	t.Cleanup(func() { beforeRename = nil })
	err := FSWriteFile(target, "new")
	if err != (Failed{Path: target, Reason: "stopped"}) {
		t.Errorf("FSWriteFile error = %#v; want Failed at the target", err)
	}
	if seen == "" {
		t.Fatal("FSWriteFile did not reach the point before the rename")
	}
	if filepath.Dir(seen) != dir {
		t.Errorf("the new file %s is not in the directory of the target %s", seen, dir)
	}
	if data, err := os.ReadFile(target); string(data) != "old" || err != nil {
		t.Errorf("the target holds %q, %v; want the old text whole", data, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "x.md" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("the directory holds %q; want only x.md", names)
	}
	beforeRename = nil
	if err := FSWriteFile(target, "new"); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(target); string(data) != "new" || err != nil {
		t.Errorf("after a write with no stop the target holds %q, %v", data, err)
	}
}

// TestFSWriteFileFailedRename: a rename that fails for a reason that is
// neither NotFound nor Denied (here the target became a directory with an
// entry just before it) is Failed at the target with the host's reason
// alone: the reason names neither the new file nor the target
// (fs.ergo: "Reason is the host's text for it, without the path"). The new
// file is removed.
func TestFSWriteFileFailedRename(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "x.md")
	var seen string
	beforeRename = func(tmp string) error {
		seen = tmp
		if err := os.MkdirAll(filepath.Join(target, "entry"), 0o755); err != nil {
			t.Fatal(err)
		}
		return nil
	}
	t.Cleanup(func() { beforeRename = nil })
	err := FSWriteFile(target, "new")
	failed, ok := err.(Failed)
	if !ok || failed.Path != target || failed.Reason == "" {
		t.Fatalf("FSWriteFile error = %#v; want Failed at the target with a reason", err)
	}
	for _, p := range []string{dir, "x.md", ".tmp", "rename"} {
		if strings.Contains(failed.Reason, p) {
			t.Errorf("Failed.Reason %q holds %q; want the host's reason without the operation and the paths", failed.Reason, p)
		}
	}
	if _, err := os.Lstat(seen); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the new file %s is still there after the failed rename (%v)", seen, err)
	}
}

// TestFSWriteFileFollowsLinks: a symbolic link at the path is followed, as
// Go's os.WriteFile follows it. The file it names gets the text, keeps its
// mode, and the link stays; a link to a missing name makes that file; a
// link to a directory is Failed "is a directory" and stays; a ".." in a
// link's text is the host's to resolve after a linked directory, not
// cleaned away; a loop of links is Failed.
func TestFSWriteFileFollowsLinks(t *testing.T) {
	dir := t.TempDir()
	at := func(name string) string { return filepath.Join(dir, name) }
	if err := os.WriteFile(at("file"), []byte("old"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(at("real/sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, l := range []struct{ link, text string }{
		{"lf", "file"}, {"lf2", "lf"}, {"ld", "real"}, {"dangling", "made"},
		{"dsub", at("real/sub")}, {"real/sub/up", "../up.md"}, {"loop1", "loop2"}, {"loop2", "loop1"},
	} {
		if err := os.Symlink(l.text, at(l.link)); err != nil {
			t.Fatal(err)
		}
	}
	isLink := func(name string) {
		t.Helper()
		if fi, err := os.Lstat(at(name)); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("%s is no longer a symbolic link (%v)", name, err)
		}
	}
	holds := func(name, want string) {
		t.Helper()
		if data, err := os.ReadFile(at(name)); string(data) != want || err != nil {
			t.Errorf("%s holds %q, %v; want %q", name, data, err, want)
		}
	}
	if err := FSWriteFile(at("lf2"), "new"); err != nil {
		t.Fatalf("FSWriteFile(lf2) = %v", err)
	}
	holds("file", "new")
	isLink("lf")
	isLink("lf2")
	if fi, err := os.Stat(at("file")); err != nil || fi.Mode().Perm() != 0o640 {
		t.Errorf("file mode after a write through a link = %v, %v; want 0640", fi.Mode(), err)
	}
	if err := FSWriteFile(at("ld"), "new"); err != (Failed{Path: at("ld"), Reason: "is a directory"}) {
		t.Errorf("FSWriteFile(ld) = %#v; want Failed is a directory", err)
	}
	isLink("ld")
	if err := FSWriteFile(at("dangling"), "made"); err != nil {
		t.Fatalf("FSWriteFile(dangling) = %v", err)
	}
	isLink("dangling")
	holds("made", "made")
	if err := FSWriteFile(at("dsub/up"), "up"); err != nil {
		t.Fatalf("FSWriteFile(dsub/up) = %v", err)
	}
	holds("real/up.md", "up")
	if _, err := os.Lstat(at("up.md")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the write through dsub/up made %s: the link's text was cleaned (%v)", at("up.md"), err)
	}
	if err := FSWriteFile(at("loop1"), "x"); err != (Failed{Path: at("loop1"), Reason: "too many levels of symbolic links"}) {
		t.Errorf("FSWriteFile(loop1) = %#v; want Failed too many levels of symbolic links", err)
	}
	// A chain of maxLinks links is followed; one more is too many.
	for i := 1; i <= maxLinks+1; i++ {
		text := "file"
		if i <= maxLinks {
			text = fmt.Sprintf("c%d", i+1)
		}
		if err := os.Symlink(text, at(fmt.Sprintf("c%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	if err := FSWriteFile(at("c2"), "chain"); err != nil {
		t.Errorf("FSWriteFile through %d links = %v", maxLinks, err)
	}
	holds("file", "chain")
	if err := FSWriteFile(at("c1"), "x"); err != (Failed{Path: at("c1"), Reason: "too many levels of symbolic links"}) {
		t.Errorf("FSWriteFile through %d links = %#v; want Failed too many levels of symbolic links", maxLinks+1, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("a new file %s is left in %s", e.Name(), dir)
		}
	}
}

// TestFSMkdirAllRow: MkdirAll's row is Denied | Failed, so a missing path
// is Failed with the host's reason, never NotFound; a file in the way is
// Failed too.
func TestFSMkdirAllRow(t *testing.T) {
	if err := FSMkdirAll(""); err != (Failed{Path: "", Reason: "no such file or directory"}) {
		t.Errorf("FSMkdirAll(\"\") = %#v; want Failed", err)
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := FSMkdirAll(filepath.Join(file, "c")); err != (Failed{Path: filepath.Join(file, "c"), Reason: "not a directory"}) {
		t.Errorf("FSMkdirAll(f/c) = %#v; want Failed not a directory", err)
	}
	if err := FSMkdirAll(filepath.Join(dir, "a", "b")); err != nil {
		t.Errorf("FSMkdirAll(a/b) = %v", err)
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
