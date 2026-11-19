package main

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"ergo.local/prototype/internal/ergobuild"
	"ergo.local/prototype/internal/testinputs"
)

func TestMain(m *testing.M) {
	os.Exit(testinputs.MainFunc(m, func() int {
		// The one ergo binary of the process (buildErgo) is built here.
		dir, err := os.MkdirTemp("", "downstream-ergo-")
		if err != nil {
			fmt.Fprintln(os.Stderr, "scripts/downstream tests:", err)
			return 2
		}
		defer os.RemoveAll(dir)
		oneErgo.path = filepath.Join(dir, "ergo")
		return m.Run()
	}))
}

// gitFixture runs git in repo with a fixed identity and no user config.
func gitFixture(t *testing.T, repo string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"--no-optional-locks", "-C", repo, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// commitFiles writes files into repo (made with git init when new) and
// commits them; it returns the commit's hash.
func commitFiles(t *testing.T, repo string, files map[string]string) string {
	t.Helper()
	if _, err := os.Stat(filepath.Join(repo, ".git")); err != nil {
		if err := os.MkdirAll(repo, 0o755); err != nil {
			t.Fatal(err)
		}
		gitFixture(t, repo, "init", "-q")
	}
	for rel, content := range files {
		path := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitFixture(t, repo, "add", "-A")
	gitFixture(t, repo, "commit", "-q", "--allow-empty", "-m", "fixture")
	return gitFixture(t, repo, "rev-parse", "HEAD")
}

// A pin is a commit: a branch or a tag in its place is refused before any
// fetch. It catches ds-fetch-follows-branch.
func TestPinsAreCommits(t *testing.T) {
	hash := strings.Repeat("a", 40)
	for _, row := range []struct {
		text string
		ok   bool
	}{
		{"# none\n\n", true},
		{"work file:///r " + hash + "\n", true},
		{"work file:///r main\n", false},
		{"work file:///r v1.2.3\n", false},
		{"work file:///r " + hash[:12] + "\n", false},
		{"work file:///r " + strings.ToUpper(hash) + "\n", false},
		{"work file:///r " + hash + "\nwork file:///s " + hash + "\n", false},
		{"work file:///r\n", false},
	} {
		_, err := parsePins(row.text)
		if (err == nil) != row.ok {
			t.Errorf("parsePins(%q): error %v, want ok %v", row.text, err, row.ok)
		}
	}
}

// fetch takes the pinned commit's tree, not the branch's head, from a
// file:// remote, with no .git; a second fetch of a cached pin starts no
// git process (gitRuns).
func TestFetchTakesTheCommit(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "remote")
	first := commitFiles(t, remote, map[string]string{"a.txt": "first\n"})
	commitFiles(t, remote, map[string]string{"a.txt": "second\n", "b.txt": "later\n"})
	cache := t.TempDir()
	pins := []pin{{name: "fx", url: "file://" + remote, commit: first}}
	var out bytes.Buffer
	if code := fetch(&out, cache, pins); code != 0 {
		t.Fatalf("fetch: exit %d\n%s", code, out.String())
	}
	tree := entry(cache, pins[0])
	got, err := os.ReadFile(filepath.Join(tree, "a.txt"))
	if err != nil || string(got) != "first\n" {
		t.Fatalf("a.txt of the entry: %q, %v; want the pinned commit's", got, err)
	}
	for _, absent := range []string{"b.txt", ".git"} {
		if _, err := os.Lstat(filepath.Join(tree, absent)); err == nil {
			t.Errorf("the entry holds %s", absent)
		}
	}
	before := gitRuns
	out.Reset()
	if code := fetch(&out, cache, pins); code != 0 || gitRuns != before || !strings.Contains(out.String(), "cached fx") {
		t.Fatalf("a second fetch: exit %d, %d git runs; want 0 and none\n%s", code, gitRuns-before, out.String())
	}
	missing := []pin{{name: "gone", url: "file://" + remote, commit: strings.Repeat("0", 40)}}
	out.Reset()
	if code := fetch(&out, cache, missing); code != 1 {
		t.Fatalf("a commit the remote lacks: exit %d, want 1\n%s", code, out.String())
	}
	if _, err := os.Stat(entry(cache, missing[0])); err == nil {
		t.Fatal("a failed fetch left an entry")
	}
}

// With no pin, the check passes and says so only while downstream/moved
// holds no path; once a path moved, zero pins is red. It catches
// ds-zero-pins-pass-after-move.
func TestZeroPinsPassOnlyBeforeAMove(t *testing.T) {
	for _, row := range []struct {
		moved string
		code  int
		line  string
	}{
		{"# none yet\n", 0, "downstream: 0 pins\n"},
		{"modules/work " + strings.Repeat("b", 40) + " cevr/work\n", 1, "FAIL downstream: 0 pins, but downstream/moved holds 1 moved paths: pin the repositories they moved to\n"},
	} {
		root := t.TempDir()
		for file, content := range map[string]string{pinsFile: "# none\n", movedFile: row.moved} {
			path := filepath.Join(root, file)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		var out, errs bytes.Buffer
		code := run([]string{"-root", root, "-cache", t.TempDir()}, &out, &errs)
		if code != row.code || out.String() != row.line {
			t.Errorf("moved %q: exit %d, out %q; want %d, %q (stderr %q)", row.moved, code, out.String(), row.code, row.line, errs.String())
		}
	}
}

// A pin that is not in the cache fails, names the fetch command and is
// never skipped, with no ergo run. It catches ds-missing-pin-skips.
func TestAMissingPinFails(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "downstream"), 0o755); err != nil {
		t.Fatal(err)
	}
	commit := strings.Repeat("c", 40)
	if err := os.WriteFile(filepath.Join(root, pinsFile), []byte("fx file:///nonexistent/loop-probe-x "+commit+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errs bytes.Buffer
	code := run([]string{"-root", root, "-ergo", "/nonexistent/loop-probe-x/ergo", "-cache", t.TempDir()}, &out, &errs)
	if code != 1 || !strings.Contains(out.String(), "FAIL fx "+commit[:12]+": not in the cache") || !strings.Contains(out.String(), "run go run ./scripts/downstream fetch") {
		t.Fatalf("a pin not in the cache: exit %d, want 1 naming the fetch\n%s%s", code, out.String(), errs.String())
	}
}

// The legs run the -ergo binary, never an ergo found on PATH, in the
// copy's root: check, test, then build and cases of each binary that has
// cases. A fake ergo logs each command; a fake first on PATH fails every
// command. It runs no compiler. It catches ds-uses-path-ergo and
// ds-cases-leg-dropped, which TestEveryPinIsBuiltOnce catches too, in
// the full tier only.
func TestLegsRunTheCheckoutErgo(t *testing.T) {
	root := t.TempDir()
	log := filepath.Join(t.TempDir(), "log")
	ergo := filepath.Join(root, "bin", "ergo")
	pathDir := t.TempDir()
	for file, script := range map[string]string{
		ergo:                           "#!/bin/sh\necho \"$*\" >> " + log + "\n",
		filepath.Join(pathDir, "ergo"): "#!/bin/sh\necho 'not the checkout ergo' >&2\nexit 3\n",
	} {
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", pathDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cache := t.TempDir()
	commit := strings.Repeat("d", 40)
	p := pin{name: "fx", url: "file:///nonexistent/loop-probe-x", commit: commit}
	for rel, content := range map[string]string{
		"ergo.mod":                         "module fx\n",
		"cmd/fx/main.ergo":                 "",
		"cmd/fx/cases/answer/expected.out": "",
		"cmd/plain/main.ergo":              "",
	} {
		path := filepath.Join(entry(cache, p), rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "downstream"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, pinsFile), []byte(p.name+" "+p.url+" "+p.commit+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errs bytes.Buffer
	if code := run([]string{"-root", root, "-cache", cache}, &out, &errs); code != 0 {
		t.Fatalf("exit %d, want 0\n%s%s", code, out.String(), errs.String())
	}
	logged, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, line := range strings.Split(strings.TrimSpace(string(logged)), "\n") {
		// The -o folder is the run's scratch: keep its last element.
		if strings.HasPrefix(line, "build ") {
			fields := strings.Fields(line)
			fields[len(fields)-1] = filepath.Base(fields[len(fields)-1])
			line = strings.Join(fields, " ")
		}
		got = append(got, line)
	}
	want := []string{"check ./...", "test ./...", "build ./cmd/fx -o fx", "test ./cmd/fx --cases cmd/fx/cases", "build ./cmd/plain -o plain"}
	if !slices.Equal(got, want) {
		t.Fatalf("the commands ergo ran:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// needsBuild keeps the tests that build ergo and Ergo programs in the full
// tier.
func needsBuild(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("builds ergo and the fixture repositories' programs")
	}
}

// oneErgo is the one ergo binary of the test process, built once from
// this checkout's cmd/ergo.
var oneErgo struct {
	once sync.Once
	path string
	out  []byte
	err  error
}

func buildErgo(t *testing.T) string {
	t.Helper()
	oneErgo.once.Do(func() {
		command := ergobuild.Command(oneErgo.path, "../../cmd/ergo", "-buildvcs=false")
		oneErgo.out, oneErgo.err = command.CombinedOutput()
	})
	if oneErgo.err != nil {
		t.Fatalf("build ergo: %v\n%s", oneErgo.err, oneErgo.out)
	}
	return oneErgo.path
}

// fixtureRepo is a generated downstream repository: a tiny Ergo module
// with a law and a binary with one case. redLaw refutes the law; redCase
// records a wrong golden; missing pins a commit that is never fetched.
type fixtureRepo struct {
	name                     string
	redLaw, redCase, missing bool
}

// files are the module's files.
func (f fixtureRepo) files() map[string]string {
	law := "!!b == b"
	if f.redLaw {
		law = "!b == b"
	}
	golden := "42\n"
	if f.redCase {
		golden = "41\n"
	}
	return map[string]string{
		"ergo.mod":                         "module fx\n",
		"calc/calc.ergo":                   "// Package calc doubles.\nfunc Double(n int) int { n + n }\n\nlaw NotNot(b bool) {\n\t" + law + "\n}\n",
		"cmd/fx/main.ergo":                 "import fx/calc\n\nfunc main() needs Console {\n\tConsole.Println(\"\\(calc.Double(21))\")\n}\n",
		"cmd/fx/cases/answer/expected.out": golden,
	}
}

// green is the model's verdict: a pin passes only when it is in the
// cache and its check, test, build and cases legs all pass.
func (f fixtureRepo) green() bool { return !f.redLaw && !f.redCase && !f.missing }

// TestEveryPinIsBuiltOnce is the downstream check's property: for a
// generated pins file over fixture repositories (git init in a temporary
// folder; some with a red law, a red case or a commit never fetched), the
// runner's verdict per pin is the model's, every pin has exactly one
// verdict line, and a pin absent from the cache names the fetch command.
// A fake ergo first on PATH that fails every command makes sure the runner
// uses -ergo, never PATH's. It builds Go because a downstream verdict is a
// build verdict: check, test, build and cases of a real module with the
// checkout's ergo.
func TestEveryPinIsBuiltOnce(t *testing.T) {
	needsBuild(t)
	ergo := buildErgo(t)
	fake := t.TempDir()
	if err := os.WriteFile(filepath.Join(fake, "ergo"), []byte("#!/bin/sh\necho 'not the checkout ergo' >&2\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fake+string(os.PathListSeparator)+os.Getenv("PATH"))
	for seed := uint64(1); seed <= 3; seed++ {
		r := rand.New(rand.NewPCG(seed, 11))
		root := t.TempDir()
		cache := t.TempDir()
		var repos []fixtureRepo
		var lines []string
		var fetched []pin
		for i := range 1 + r.IntN(3) {
			f := fixtureRepo{name: fmt.Sprintf("r%d", i), redLaw: r.IntN(4) == 0, redCase: r.IntN(4) == 0, missing: r.IntN(4) == 0}
			repo := filepath.Join(root, "repos", f.name)
			commit := commitFiles(t, repo, f.files())
			p := pin{name: f.name, url: "file://" + repo, commit: commit}
			if !f.missing {
				fetched = append(fetched, p)
			}
			repos = append(repos, f)
			lines = append(lines, p.name+" "+p.url+" "+p.commit)
		}
		var out bytes.Buffer
		if code := fetch(&out, cache, fetched); code != 0 {
			t.Fatalf("seed %d: fetch: exit %d\n%s", seed, code, out.String())
		}
		if err := os.MkdirAll(filepath.Join(root, "downstream"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, pinsFile), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		out.Reset()
		var errs bytes.Buffer
		code := run([]string{"-root", root, "-ergo", ergo, "-cache", cache}, &out, &errs)
		wantCode := 0
		for _, f := range repos {
			if !f.green() {
				wantCode = 1
			}
			var verdicts []string
			for _, line := range strings.Split(out.String(), "\n") {
				if strings.HasPrefix(line, "ok "+f.name+" ") || strings.HasPrefix(line, "FAIL "+f.name+" ") {
					verdicts = append(verdicts, line)
				}
			}
			oks := slices.IndexFunc(verdicts, func(l string) bool { return strings.HasPrefix(l, "ok ") })
			switch {
			case f.green() && (len(verdicts) != 1 || oks != 0):
				t.Errorf("seed %d: %s (%+v) is green in the model; got %q", seed, f.name, f, verdicts)
			case !f.green() && (len(verdicts) == 0 || oks >= 0):
				t.Errorf("seed %d: %s (%+v) is red in the model; got %q", seed, f.name, f, verdicts)
			case f.missing && (len(verdicts) != 1 || !strings.Contains(verdicts[0], "go run ./scripts/downstream fetch")):
				t.Errorf("seed %d: %s is not in the cache; want one line naming the fetch, got %q", seed, f.name, verdicts)
			case !f.missing && f.redCase && !slices.ContainsFunc(verdicts, func(l string) bool { return strings.Contains(l, ": cases ./cmd/fx: ") }):
				t.Errorf("seed %d: %s has a red case; want its cases leg to fail, got %q", seed, f.name, verdicts)
			}
		}
		if code != wantCode {
			t.Errorf("seed %d: exit %d, want %d", seed, code, wantCode)
		}
		if t.Failed() {
			t.Fatalf("seed %d: output:\n%s\nstderr:\n%s", seed, out.String(), errs.String())
		}
	}
}
