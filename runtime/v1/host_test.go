package v1

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
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
	if info, err := FSStat(at("a.md")); info != (Info{Name: "a.md", Size: 6, Dir: false, Perm: int(permOf(t, at("a.md")))}) || err != nil {
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
	if info, err := FSStat(at("link-a")); info != (Info{Name: "link-a", Size: 6, Dir: false, Perm: int(permOf(t, at("a.md")))}) || err != nil {
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
		{"lf", "file"}, {"lf2", "lf"}, {"lr", "real/far.md"}, {"ld", "real"}, {"dangling", "made"},
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
	// madeIn makes the next write record its new file and checks that the
	// file's directory is the directory want, the one that holds the file
	// the link names: then the rename replaces that file and never crosses
	// a device (stdlib.md fs 8), even when the link is in another
	// directory or on another file system (D1 r2 M3).
	madeIn := func(want string) {
		t.Helper()
		beforeRename = func(tmp string) error {
			// filepath.Dir would clean "dsub/.." lexically, which the host
			// does not, so the file is looked up in want by its name.
			got, err := os.Stat(tmp)
			there, err2 := os.Stat(filepath.Join(at(want), filepath.Base(tmp)))
			if err != nil || err2 != nil || !os.SameFile(got, there) {
				t.Errorf("the new file %s is not in the directory %s of the file the link names (%v, %v)", tmp, at(want), err, err2)
			}
			return nil
		}
	}
	t.Cleanup(func() { beforeRename = nil })
	madeIn(".")
	if err := FSWriteFile(at("lf2"), "new"); err != nil {
		t.Fatalf("FSWriteFile(lf2) = %v", err)
	}
	holds("file", "new")
	madeIn("real")
	if err := FSWriteFile(at("lr"), "far"); err != nil {
		t.Fatalf("FSWriteFile(lr) = %v", err)
	}
	isLink("lr")
	holds("real/far.md", "far")
	beforeRename = nil
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
	madeIn("real")
	if err := FSWriteFile(at("dsub/up"), "up"); err != nil {
		t.Fatalf("FSWriteFile(dsub/up) = %v", err)
	}
	beforeRename = nil
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

// TestIOLinesSplit: the host's Lines (a lineReader over a reader) keeps the
// law LinesSplitInput that io.Input states: the lines joined by line
// feeds, plus the final line feed if any, are the input, over seeded texts
// with carriage returns, empty lines, invalid UTF-8 and lines longer than
// a reader's buffer. A short read (one byte at a time) gives the same.
func TestIOLinesSplit(t *testing.T) {
	alphabet := []string{"a", "\n", "\n", "\r", "é", "\xff", " ", strings.Repeat("w", 5000)}
	seed := uint64(1)
	next := func(n int) int {
		seed = seed*6364136223846793005 + 1442695040888963407
		return int((seed >> 33) % uint64(n))
	}
	cases := []string{"", "\n", "\n\n", "a", "a\n", "a\r\n", "\r", strings.Repeat("x", 70_000)}
	for range 3000 {
		var b strings.Builder
		for range next(9) {
			b.WriteString(alphabet[next(len(alphabet))])
		}
		cases = append(cases, b.String())
	}
	for _, s := range cases {
		for _, r := range []interface{ Read([]byte) (int, error) }{strings.NewReader(s), &oneByte{s: s}} {
			var lines []string
			for line := range newLineReader(r).lines() {
				lines = append(lines, line)
			}
			joined := strings.Join(lines, "\n")
			if strings.HasSuffix(s, "\n") {
				joined += "\n"
			}
			if joined != s {
				t.Fatalf("Lines of %q is %q", s, lines)
			}
		}
	}
	// A break stops the Seq.
	n := 0
	for range newLineReader(strings.NewReader("a\nb\nc\n")).lines() {
		n++
		break
	}
	if n != 1 {
		t.Errorf("a break went on: %d steps", n)
	}
}

// TestIOStdinConcurrent: tasks that read one lineReader at once each get
// whole lines, and together every line exactly once (run with -race: the
// reader's lock is what makes the reads safe).
func TestIOStdinConcurrent(t *testing.T) {
	var in strings.Builder
	const n = 2000
	for i := range n {
		fmt.Fprintf(&in, "line %d\n", i)
	}
	l := newLineReader(strings.NewReader(in.String()))
	got := make([][]string, 4)
	done := make(chan int)
	for g := range got {
		go func() {
			for line := range l.lines() {
				got[g] = append(got[g], line)
			}
			done <- g
		}()
	}
	for range got {
		<-done
	}
	seen := map[string]bool{}
	for _, lines := range got {
		for _, line := range lines {
			if seen[line] || !strings.HasPrefix(line, "line ") {
				t.Fatalf("line %q read twice or torn", line)
			}
			seen[line] = true
		}
	}
	if len(seen) != n {
		t.Errorf("%d lines read; want %d", len(seen), n)
	}
}

// TestIOStdinShared (S1b round 2, M2): every Stdin read of the host goes
// through one reader, so no read loses what another read ahead. For
// seeded inputs and seeded plans, a sequence of IOLines loops (each with a
// break after k lines) and IOReadAll calls, over a pipe on os.Stdin, gives
// back exactly the input, in order. The inputs have and lack a final line
// feed and hold carriage returns, invalid UTF-8 and lines longer than the
// reader's 4096-byte buffer.
func TestIOStdinShared(t *testing.T) {
	alphabet := []string{"a", "\n", "\n", "\r", "é", "\xff", "b c", strings.Repeat("w", 5000)}
	seed := uint64(7)
	next := func(n int) int {
		seed = seed*6364136223846793005 + 1442695040888963407
		return int((seed >> 33) % uint64(n))
	}
	oldIn := os.Stdin
	defer func() { os.Stdin = oldIn }()
	for c := range 400 {
		var b strings.Builder
		for range next(12) {
			b.WriteString(alphabet[next(len(alphabet))])
		}
		s := b.String()
		if c == 0 {
			s = "a\nb\nc\n"
		}
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			_, _ = w.WriteString(s)
			w.Close()
		}()
		os.Stdin = r
		var got strings.Builder
		var plan []string
		for step := 0; step < 4 && got.Len() < len(s); step++ {
			if next(3) == 0 {
				all, err := IOReadAll()
				if err != nil {
					t.Fatal(err)
				}
				plan = append(plan, "ReadAll")
				got.WriteString(all)
				continue
			}
			k := next(4)
			plan = append(plan, fmt.Sprintf("Lines break after %d", k))
			if k == 0 {
				// A loop whose body breaks at once has taken one line.
				k = 1
			}
			n := 0
			for line := range IOLines() {
				rest := s[got.Len():]
				if strings.HasPrefix(rest, line+"\n") {
					got.WriteString(line + "\n")
				} else {
					got.WriteString(line)
				}
				n++
				if n == k {
					break
				}
			}
		}
		all, err := IOReadAll()
		if err != nil {
			t.Fatal(err)
		}
		plan = append(plan, "ReadAll")
		got.WriteString(all)
		r.Close()
		if got.String() != s {
			t.Fatalf("input %q, plan %q: the reads gave %q", s, plan, got.String())
		}
	}
}

// oneByte reads its text one byte per Read.
type oneByte struct{ s string }

func (o *oneByte) Read(p []byte) (int, error) {
	if o.s == "" {
		return 0, io.EOF
	}
	p[0] = o.s[0]
	o.s = o.s[1:]
	return 1, nil
}

// TestIOBodies: Print writes its text as it is to standard output, Eprint
// to standard error, ReadAll reads all of standard input, and IsTerminal
// is false on a pipe. Each stream is a pipe of the test.
func TestIOBodies(t *testing.T) {
	pipe := func() (*os.File, *os.File) {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		return r, w
	}
	oldOut, oldErr, oldIn := os.Stdout, os.Stderr, os.Stdin
	defer func() { os.Stdout, os.Stderr, os.Stdin = oldOut, oldErr, oldIn }()
	outR, outW := pipe()
	errR, errW := pipe()
	inR, inW := pipe()
	os.Stdout, os.Stderr, os.Stdin = outW, errW, inR
	IOPrint("a")
	IOPrint("b\n")
	IOEprint("e")
	terminal := IOIsTerminal()
	if _, err := inW.WriteString("x\ny\nz"); err != nil {
		t.Fatal(err)
	}
	inW.Close()
	all, err := IOReadAll()
	os.Stdout, os.Stderr, os.Stdin = oldOut, oldErr, oldIn
	outW.Close()
	errW.Close()
	out, _ := readAllFile(outR)
	errText, _ := readAllFile(errR)
	if out != "ab\n" || errText != "e" || terminal || err != nil || all != "x\ny\nz" {
		t.Errorf("stdout %q, stderr %q, terminal %v, ReadAll %q, %v", out, errText, terminal, all, err)
	}
	if got := (ReadFailed{Reason: "boom"}).Error(); got != "io.ReadFailed{Reason:boom}" {
		t.Errorf("ReadFailed text %q", got)
	}
}

// BenchmarkIOPrint: 10,000 lines through io.Host's Print body, against
// the Go controls fmt.Fprint to os.Stdout (unbuffered, the same work) and
// a bufio.Writer (buffered, which S4c may reach), all to /dev/null.
func BenchmarkIOPrint(b *testing.B) {
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		b.Fatal(err)
	}
	defer null.Close()
	old := os.Stdout
	defer func() { os.Stdout = old }()
	os.Stdout = null
	const lines = 10_000
	b.Run("ergo", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			for range lines {
				IOPrint("a line of text\n")
			}
		}
	})
	b.Run("fmt", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			for range lines {
				fmt.Fprint(os.Stdout, "a line of text\n")
			}
		}
	})
	b.Run("bufio", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			w := bufio.NewWriter(os.Stdout)
			for range lines {
				_, _ = w.WriteString("a line of text\n")
			}
			_ = w.Flush()
		}
	})
}

// BenchmarkIOLines: 10,000 lines through the shared reader's lines (one
// lock per line), against the Go control, a bufio.Reader's ReadString
// loop with no lock.
func BenchmarkIOLines(b *testing.B) {
	text := strings.Repeat("a line of text\n", 10_000)
	b.Run("ergo", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			n := 0
			for range newLineReader(strings.NewReader(text)).lines() {
				n++
			}
		}
	})
	b.Run("bufio", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			br := bufio.NewReader(strings.NewReader(text))
			n := 0
			for {
				line, err := br.ReadString('\n')
				if line != "" {
					n++
				}
				if err != nil {
					break
				}
			}
		}
	})
}

func readAllFile(f *os.File) (string, error) {
	var b strings.Builder
	buf := make([]byte, 512)
	for {
		n, err := f.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			return b.String(), nil
		}
	}
}
