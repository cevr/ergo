package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

var changeSeed = flag.Uint64("change.seed", 0, "run TestLintChangeSetModel on this seed alone")

// changeTemplates are the function bodies of TestLintChangeSetModel, by
// id; %s is the function's name, %[2]s a local's, %[3]d a constant. Two
// functions of one template and one constant do the same (shapeOf), what
// their names; any other two differ.
var changeTemplates = []string{
	"func %s(n int) int {\n\tlet %[2]s = n  *  %[3]d\n\t%[2]s + 1\n}\n",
	"func %s(s string) string {\n\tlet %[2]s = s + \"%[3]d\"\n\t%[2]s  +  %[2]s\n}\n",
}

// changeFunc is one generated function: its name, file and identity
// (template and constant); a loose one, which the edit adds, keeps the
// templates' extra spaces for ergo fmt to take out.
type changeFunc struct {
	name, file string
	id         [2]int
	loose      bool
}

func (f changeFunc) source() string {
	src := fmt.Sprintf(changeTemplates[f.id[0]], f.name, "v"+f.name, f.id[1])
	if !f.loose {
		src = strings.ReplaceAll(src, "  ", " ")
	}
	return src
}

// TestLintChangeSetModel is AS6's command property (red at base: no
// change record, no added-clone or scattered-edit). Per seed it builds a
// module of four packages in a temporary git repository (git is what
// --diff reads; no in-process stand-in exists for a work tree), commits it,
// then commits an edit: functions added to random packages (some do what
// a function of the program does), at most one function moved to another
// file of its package, and one three-line if added to the Keep function
// of one to four packages. The reference decides each finding from the
// generator's own identities, not from shapeOf: an added or moved
// function is added-clone iff another function of the program has its
// template and constant; each added if is scattered-edit iff it is added
// in three or more packages. The record must name every changed file
// with its counts, the changed packages and the edit's subject. Then
// ergo fmt rewrites the added files (their spacing is not the
// formatter's), and the record and the findings must stay as they were.
// Mutants: change-clone-self (a function is its own twin, so a moved one
// is a clone), change-scattered-two (two packages are scattered),
// change-files-untracked-dropped (an untracked file left out of the record).
//
// Replay: go test -run '^TestLintChangeSetModel$' ./cmd/ergo -args -change.seed=N
func TestLintChangeSetModel(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("git is needed: %v", err)
	}
	seeds := []uint64{1, 2, 3, 4, 5, 6}
	if *changeSeed != 0 {
		seeds = []uint64{*changeSeed}
	}
	reached := map[string]bool{}
	for _, seed := range seeds {
		changeSeedRun(t, seed, reached)
	}
	if *changeSeed == 0 {
		for _, k := range []string{"clone", "unique", "moved", "scattered", "not-scattered"} {
			if !reached[k] {
				t.Errorf("the seeds never reached %s", k)
			}
		}
	}
}

func changeSeedRun(t *testing.T, seed uint64, reached map[string]bool) {
	t.Helper()
	r := rand.New(rand.NewPCG(seed, 0xa56))
	dir := physicalTempDir(t)
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"--no-optional-locks", "-c", "user.email=lint@example.org", "-c", "user.name=lint", "-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("seed %d: git %v: %v\n%s", seed, args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	pkgs := []string{"p0", "p1", "p2", "p3"}
	keep := "func Keep(n int) int {\n\tn\n}\n"
	files := map[string][]changeFunc{} // file -> its generated functions, in order
	write := func(file string, keepFirst bool, extra string) {
		t.Helper()
		var b strings.Builder
		if keepFirst {
			b.WriteString(extra)
		}
		for _, f := range files[file] {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(f.source())
		}
		smokeWrite(t, dir, file, b.String())
	}
	smokeWrite(t, dir, "ergo.mod", "module app\n")
	var main strings.Builder
	for _, p := range pkgs {
		fmt.Fprintf(&main, "import app/%s\n", p)
	}
	main.WriteString("\nfunc main() needs Console {\n")
	for _, p := range pkgs {
		fmt.Fprintf(&main, "\tConsole.Println(\"\\(%s.Keep(1))\")\n", p)
	}
	main.WriteString("}\n")
	smokeWrite(t, dir, "cmd/app/main.ergo", main.String())
	n := 0
	fresh := func(file string) changeFunc {
		n++
		return changeFunc{name: fmt.Sprintf("F%d", n), file: file, id: [2]int{r.IntN(len(changeTemplates)), r.IntN(2)}}
	}
	for _, p := range pkgs {
		a := p + "/a.ergo"
		for range 1 + r.IntN(2) {
			files[a] = append(files[a], fresh(a))
		}
		write(a, true, keep)
	}
	git("init", "-q")
	git("add", ".")
	git("commit", "-q", "-m", "base")
	base := git("rev-parse", "HEAD")

	// The edit.
	var added []changeFunc
	for range 1 + r.IntN(3) {
		p := pkgs[r.IntN(len(pkgs))]
		file := p + "/a.ergo"
		f := fresh(file)
		f.loose = true
		if r.IntN(2) == 0 && len(files) > 0 {
			// Do what an existing function does, under another name.
			all := []changeFunc{}
			for _, fs := range files {
				all = append(all, fs...)
			}
			slices.SortFunc(all, func(x, y changeFunc) int { return strings.Compare(x.name, y.name) })
			f.id = all[r.IntN(len(all))].id
		}
		files[file] = append(files[file], f)
		added = append(added, f)
	}
	var moved []changeFunc
	if r.IntN(2) == 0 {
		p := pkgs[r.IntN(len(pkgs))]
		from := p + "/a.ergo"
		if fs := files[from]; len(fs) > 0 {
			f := fs[0]
			files[from] = fs[1:]
			f.file = p + "/b.ergo"
			files[f.file] = append(files[f.file], f)
			moved = append(moved, f)
			reached["moved"] = true
		}
	}
	ifs := 1 + r.IntN(4)
	guard := "func Keep(n int) int {\n\tif n > 7 {\n\t\treturn n  -  7\n\t}\n\tn\n}\n"
	for i, p := range pkgs {
		k := keep
		if i < ifs {
			k = guard
		}
		write(p+"/a.ergo", true, k)
		if len(files[p+"/b.ergo"]) > 0 {
			write(p+"/b.ergo", false, "")
		}
	}
	git("add", ".")
	git("commit", "-q", "-m", "edit")
	// A new file left untracked: all added.
	c := fresh("p0/c.ergo")
	c.loose = true
	files["p0/c.ergo"] = []changeFunc{c}
	added = append(added, files["p0/c.ergo"][0])
	write("p0/c.ergo", false, "")

	// The reference.
	var all []changeFunc
	for _, fs := range files {
		all = append(all, fs...)
	}
	want := map[string]bool{}
	for _, f := range append(slices.Clone(added), moved...) {
		clone := false
		for _, g := range all {
			if g.name != f.name && g.id == f.id {
				clone = true
			}
		}
		if clone {
			want["L130 "+f.name] = true
			reached["clone"] = true
		} else {
			reached["unique"] = true
		}
	}
	if ifs >= 3 {
		reached["scattered"] = true
		for _, p := range pkgs[:ifs] {
			want["L131 "+p+"/a.ergo"] = true
		}
	} else {
		reached["not-scattered"] = true
	}

	type finding struct {
		Code    string
		File    string
		Message string
	}
	type rec struct {
		Files []struct {
			Path           string
			Added, Deleted int
		}
		Packages []string
		Subjects []string
		Purpose  string
	}
	lintOnce := func() (map[string]bool, rec) {
		t.Helper()
		out, code := commandInProcess(t, "lint", "--json", "--preset", "change", "--diff="+base, filepath.Join(dir, "cmd/app"))
		var report struct {
			Diagnostics []finding
			ChangeSet   *rec `json:"changeSet"`
		}
		if err := json.Unmarshal(out, &report); err != nil || report.ChangeSet == nil {
			t.Fatalf("seed %d: exit %d %v: %s", seed, code, err, out)
		}
		got := map[string]bool{}
		for _, d := range report.Diagnostics {
			switch d.Code {
			case "L130":
				name, _, _ := strings.Cut(d.Message, ",")
				got["L130 "+name] = true
			case "L131":
				got["L131 "+d.File] = true
			default:
				t.Fatalf("seed %d: unexpected %+v", seed, d)
			}
		}
		return got, *report.ChangeSet
	}
	got, record := lintOnce()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("seed %d: lint says %v, the reference %v\nReplay: go test -run '^TestLintChangeSetModel$' ./cmd/ergo -args -change.seed=%d", seed, got, want, seed)
	}
	var paths []string
	for _, f := range record.Files {
		paths = append(paths, f.Path)
		if f.Added == 0 && f.Deleted == 0 {
			t.Fatalf("seed %d: %s changed with no counted line: %+v", seed, f.Path, record)
		}
	}
	slices.Sort(paths)
	wantPaths := []string{}
	changed := map[string]bool{"p0/c.ergo": true}
	for _, f := range append(slices.Clone(added), moved...) {
		changed[f.file] = true
	}
	for i, p := range pkgs {
		if i < ifs {
			changed[p+"/a.ergo"] = true
		}
	}
	for _, f := range moved {
		changed[strings.Replace(f.file, "b.ergo", "a.ergo", 1)] = true
	}
	for p := range changed {
		wantPaths = append(wantPaths, p)
	}
	slices.Sort(wantPaths)
	if !reflect.DeepEqual(paths, wantPaths) || !reflect.DeepEqual(record.Subjects, []string{"edit"}) || record.Purpose != "branch main; edit" {
		t.Fatalf("seed %d: the record %+v; want files %v, subjects [edit], purpose \"branch main; edit\"", seed, record, wantPaths)
	}
	var pkgsWant []string
	for _, p := range wantPaths {
		pkgsWant = append(pkgsWant, filepath.Dir(p))
	}
	slices.Sort(pkgsWant)
	if !reflect.DeepEqual(record.Packages, slices.Compact(pkgsWant)) {
		t.Fatalf("seed %d: packages %v, want %v", seed, record.Packages, slices.Compact(pkgsWant))
	}
	// Formatting the changed files changes no line count, record or finding.
	for _, p := range wantPaths {
		before, _ := os.ReadFile(filepath.Join(dir, p))
		if _, code := commandInProcess(t, "fmt", filepath.Join(dir, p)); code != 0 {
			t.Fatalf("seed %d: ergo fmt %s: exit %d", seed, p, code)
		}
		after, _ := os.ReadFile(filepath.Join(dir, p))
		if string(before) == string(after) && strings.Contains(string(before), "  ") {
			t.Fatalf("seed %d: ergo fmt left %s as it was", seed, p)
		}
	}
	got2, record2 := lintOnce()
	if !reflect.DeepEqual(got2, got) || !reflect.DeepEqual(record2, record) {
		t.Fatalf("seed %d: after ergo fmt, findings %v and record %+v; before, %v and %+v", seed, got2, record2, got, record)
	}
}
