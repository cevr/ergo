package main

import (
	"bytes"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"ergo.local/prototype/internal/testinputs"
	"ergo.local/prototype/internal/testwait"
)

// The tests run the runner in this process over fixture roots in
// t.TempDir(), with the fake compiler in testdata. They read nothing else of
// the checkout: the real tree and the real compiler belong to the gate step.

// asRunner makes this test binary run as the runner itself: the signal test
// starts it so, to signal a runner that is not the test process.
const asRunner = "EXAMPLES_TEST_AS_RUNNER"

func TestMain(m *testing.M) {
	if os.Getenv(asRunner) == "1" {
		os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
	}
	os.Exit(testinputs.Main(m))
}

// caseLife is how long a fixture's process that the runner must stop lives
// by itself (a ticker, a sleeping leader, a stream holder): a run that took
// as long ended by the case, not by the runner. It is the fixtures' own end,
// an event, and no bound of the runner's speed.
const caseLife = 60 * time.Second

// ticker is a fixture's shell lines that start a background loop in the
// process group: it appends a line to file (a shell word) every 50 ms, and
// it ends by itself caseLife after it started, so a runner that failed to stop
// the group cannot leak it for long. Then they wait until the loop ticked
// once, an event (a 10 s bound could pass a loaded host by; fu-time-limits).
// Every detection window of these tests is well inside the 60 s.
func ticker(file string) string {
	return `( end=$(( $(date +%s) + ` + fmt.Sprint(int(caseLife/time.Second)) + ` )); while [ "$(date +%s)" -lt "$end" ]; do echo tick >> ` + file + `; sleep 0.05; done ) &` + "\n" +
		`until [ -s ` + file + ` ]; do sleep 0.01; done` + "\n"
}

// caseSleep is a fixture's line that sleeps caseLife.
var caseSleep = fmt.Sprintf("sleep %d\n", int(caseLife/time.Second))

// caseTimeout is the runner's -timeout for a case that ends by itself: the
// test's own time left (testwait.Left), so only a hang meets it; a year
// when the test has no deadline, as the runner needs a positive one.
func caseTimeout(t *testing.T) string {
	t.Helper()
	if left := testwait.Left(t); left > 0 {
		return left.String()
	}
	return "8760h"
}

type fixture struct {
	t     *testing.T
	root  string
	ergo  string
	calls string
}

// newFixture is an empty examples/ root and a fresh link to the fake
// compiler, which logs its calls beside the link. A link, not a copy: a copy
// is a file this process writes, and a parallel test's fork can hold that
// write descriptor open until its exec, so running the copy can fail with
// "text file busy" (ETXTBSY, golang/go#22315).
func newFixture(t *testing.T) *fixture {
	t.Helper()
	tools := t.TempDir()
	fake, err := filepath.Abs(filepath.Join("testdata", "fake-ergo"))
	if err != nil {
		t.Fatal(err)
	}
	ergo := filepath.Join(tools, "ergo")
	if err := os.Symlink(fake, ergo); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "examples"), 0o755); err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, root: root, ergo: ergo, calls: filepath.Join(tools, "calls.log")}
}

func (f *fixture) write(name, content string) {
	f.t.Helper()
	path := filepath.Join(f.root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// program writes examples/NAME: main.ergo (a shell script the fake compiler
// "builds"), README.md, tags (none when tags is "-"), the cases (case name
// to file name to content) and any extra files.
func (f *fixture) program(name, main, tags string, cases map[string]map[string]string, extra map[string]string) string {
	f.t.Helper()
	dir := "examples/" + name
	f.write(dir+"/main.ergo", main)
	f.write(dir+"/README.md", "# "+name+"\n")
	if tags != "-" {
		f.write(dir+"/tags", tags)
	}
	for file, content := range extra {
		f.write(dir+"/"+file, content)
	}
	for c, files := range cases {
		if err := os.MkdirAll(filepath.Join(f.root, dir, "cases", c), 0o755); err != nil {
			f.t.Fatal(err)
		}
		for file, content := range files {
			f.write(dir+"/cases/"+c+"/"+file, content)
		}
	}
	return filepath.Join(f.root, dir)
}

func (f *fixture) good() {
	f.program("good", "#!/bin/sh\necho hi\n", "default\n", map[string]map[string]string{"basic": {"expected.out": "hi\n"}}, nil)
}

// run runs the runner over the fixture with the fake compiler.
func (f *fixture) run(args ...string) (int, string) {
	f.t.Helper()
	var out bytes.Buffer
	code := run(append([]string{"-root", f.root, "-ergo", f.ergo}, args...), &out, &out)
	return code, out.String()
}

// verdicts are the case lines of a run: ok cases, and FAIL lines by label.
func verdicts(out string) (map[string]bool, map[string]string) {
	oks, fails := map[string]bool{}, map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if m := regexp.MustCompile(`^ok (\S+/\S+) \(`).FindStringSubmatch(line); m != nil {
			oks[m[1]] = true
		}
		if m := regexp.MustCompile(`^FAIL (\S+?):(.*)$`).FindStringSubmatch(line); m != nil {
			fails[m[1]] = m[2]
		}
	}
	return oks, fails
}

// A case passes exactly when stdout, stderr and the status all equal the
// recorded ones, byte for byte; an absent expected.err is empty and an absent
// expected.status is 0. A failure names each stream that differs and only
// those, and shows a unified diff.
func TestCasePassesExactlyWhenItsStreamsAndStatusMatch(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	random := rand.New(rand.NewPCG(20261104, 1))
	words := []string{"a", "tide", "shell", "x y", "", "zz"}
	text := func() string {
		var lines []string
		for range random.IntN(4) {
			lines = append(lines, words[random.IntN(len(words))])
		}
		s := strings.Join(lines, "\n")
		if s != "" && random.IntN(10) < 6 {
			s += "\n"
		}
		return s
	}
	pick := func(options ...string) string { return options[random.IntN(len(options))] }
	good := map[string]map[string]string{}
	broken := map[string]map[string]string{}
	mutated := map[string]string{}
	for i := range 60 {
		out, errText := text(), text()
		status := []int{0, 0, 1, 2, 3, 7}[random.IntN(6)]
		files := map[string]string{
			"args":         strings.ReplaceAll(out, "\n", `\n`) + "\n" + strings.ReplaceAll(errText, "\n", `\n`) + "\n" + fmt.Sprint(status) + "\n",
			"expected.out": out,
		}
		if errText != "" || random.IntN(2) == 0 {
			files["expected.err"] = errText
		}
		if status != 0 || random.IntN(2) == 0 {
			files["expected.status"] = pick(fmt.Sprint(status), fmt.Sprint(status)+"\n")
		}
		name := fmt.Sprintf("c%03d", i)
		if i%2 == 0 {
			good[name] = files
			continue
		}
		stream := pick("stdout", "stderr", "status")
		switch stream {
		case "stdout":
			changed := pick(out+"x", strings.TrimSuffix(out, "\n"), out+"\n", "q"+out)
			if changed == out {
				changed = out + "q"
			}
			files["expected.out"] = changed
		case "stderr":
			if errText == "" {
				files["expected.err"] = pick("e\n", " ", "\n")
			} else if random.IntN(2) == 0 {
				delete(files, "expected.err")
			} else {
				files["expected.err"] = errText + "!"
			}
		case "status":
			if status == 0 {
				files["expected.status"] = pick("1\n", "3")
			} else if random.IntN(2) == 0 {
				delete(files, "expected.status")
			} else {
				files["expected.status"] = fmt.Sprint(status+1) + "\n"
			}
		}
		mutated[name] = stream
		broken[name] = files
	}
	echoer := "#!/bin/sh\nprintf '%b' \"$1\"\nprintf '%b' \"$2\" >&2\nexit \"$3\"\n"
	f.program("echoer", echoer, "default\n", good, nil)
	f.program("broken", echoer, "default\n", broken, nil)

	code, out := f.run("echoer")
	oks, fails := verdicts(out)
	if code != 0 || len(fails) != 0 || strings.Contains(out, "broken/") {
		t.Fatalf("matching cases: exit %d\n%s", code, out)
	}
	for name := range good {
		if !oks["echoer/"+name] {
			t.Fatalf("no ok line for echoer/%s\n%s", name, out)
		}
	}
	code, out = f.run("broken")
	_, fails = verdicts(out)
	if code != 1 {
		t.Fatalf("mismatching cases: exit %d, want 1\n%s", code, out)
	}
	for name, stream := range mutated {
		line, ok := fails["broken/"+name]
		if !ok {
			t.Fatalf("no FAIL line for broken/%s (mutated %s)\n%s", name, stream, out)
		}
		for _, s := range []string{"stdout", "stderr", "status"} {
			if strings.Contains(line, s) != (s == stream) {
				t.Errorf("FAIL broken/%s:%s; mutated only %s", name, line, stream)
			}
		}
	}
	if !strings.Contains(out, "--- want\n+++ got\n") {
		t.Fatalf("a stream failure prints no unified diff\n%s", out)
	}
}

// A case sees only its declared world: its arguments one per line, its stdin
// (or empty input), PATH, HOME set to a fresh empty working directory, and
// its env lines; setup.sh prepares that directory. Nothing else of the
// runner's environment, stdin or earlier cases reaches it.
func TestCaseSeesOnlyItsDeclaredWorld(t *testing.T) {
	f := newFixture(t)
	t.Setenv("EXAMPLES_LEAK", "leaked")
	t.Setenv("FOO", "leaked")
	// TMPDIR goes through a link, as it does on darwin (/var is a link to
	// /private/var): HOME must still name the working directory itself.
	temp := t.TempDir()
	if err := os.Mkdir(filepath.Join(temp, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(temp, "real"), filepath.Join(temp, "link")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", filepath.Join(temp, "link"))
	probe := `#!/bin/sh
echo "argc=$#"
printf '[%s]' "$@"
echo
echo "stdin:$(cat)"
echo "leak=${EXAMPLES_LEAK-unset} foo=${FOO-unset} path=${PATH:+set}"
if [ "$(cd "$HOME" && pwd -P)" = "$(pwd -P)" ] && [ "$HOME" = "$(pwd -P)" ]; then echo home=cwd; else echo "home=$HOME cwd=$(pwd -P)"; fi
ls -A
touch made-by-case
`
	f.program("probe", probe, "default\n", map[string]map[string]string{
		"a1": {"args": "x y\n\nz\n", "stdin": "hello\n", "env": "FOO=bar\n", "expected.out": "argc=3\n[x y][][z]\nstdin:hello\nleak=unset foo=bar path=set\nhome=cwd\n"},
		"a2": {"args": "one", "expected.out": "argc=1\n[one]\nstdin:\nleak=unset foo=unset path=set\nhome=cwd\n"},
		"a3": {"setup.sh": "printf 'fixture\\n' > fixture.txt\n", "args": "\n", "expected.out": "argc=1\n[]\nstdin:\nleak=unset foo=unset path=set\nhome=cwd\nfixture.txt\n"},
	}, nil)
	code, out := f.run()
	oks, _ := verdicts(out)
	if code != 0 || !oks["probe/a1"] || !oks["probe/a2"] || !oks["probe/a3"] {
		t.Fatalf("exit %d\n%s", code, out)
	}
}

// grows reports whether a file still grows a while after the run returned.
func grows(t *testing.T, file string) bool {
	t.Helper()
	size := func() int64 {
		info, err := os.Stat(file)
		if err != nil {
			t.Fatalf("the case's background child never wrote: %v", err)
		}
		return info.Size()
	}
	before := size()
	time.Sleep(400 * time.Millisecond)
	return size() != before
}

// A case past -timeout fails "timed out", and the runner stops its process
// group: its background children too. A case that exits while its child
// still holds stdout does not hold the runner, and the child is stopped.
func TestCaseProcessGroupIsStopped(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ticks := t.TempDir()
	f.program("sleeper", "#!/bin/sh\n"+ticker(`"$TICKS"`)+caseSleep, "slow\n", map[string]map[string]string{
		"slow": {"env": "TICKS=" + filepath.Join(ticks, "slow") + "\n", "expected.out": ""},
	}, nil)
	f.program("leaver", "#!/bin/sh\n"+ticker(`"$TICKS"`)+"echo done\n", "slow\n", map[string]map[string]string{
		"quick": {"env": "TICKS=" + filepath.Join(ticks, "quick") + "\n", "expected.out": "done\n"},
	}, nil)
	started := time.Now()
	code, out := f.run("-timeout", "1s", "sleeper")
	if took := time.Since(started); took >= caseLife {
		t.Fatalf("a 1s timeout took %s: the case ended by itself", took)
	}
	_, fails := verdicts(out)
	if code != 1 || !strings.Contains(fails["sleeper/slow"], "timed out after 1s") {
		t.Fatalf("want FAIL sleeper/slow: timed out; exit %d\n%s", code, out)
	}
	if grows(t, filepath.Join(ticks, "slow")) {
		t.Fatal("the timed-out case's background child still runs")
	}
	started = time.Now()
	code, out = f.run("-timeout", caseTimeout(t), "leaver")
	if took := time.Since(started); code != 0 || took >= caseLife {
		t.Fatalf("a case that left a child holding stdout: exit %d after %s\n%s", code, took, out)
	}
	if grows(t, filepath.Join(ticks, "quick")) {
		t.Fatal("the background child of a finished case still runs")
	}
}

// A case of a default program over -budget fails naming the budget; a slow
// program runs only when selected by tag or by name.
func TestBudgetAndSelection(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	slow := map[string]map[string]string{"basic": {"expected.out": "done\n"}}
	f.program("slowpoke", "#!/bin/sh\nsleep 1\necho done\n", "default\n", slow, nil)
	f.program("lazy", "#!/bin/sh\nsleep 1\necho done\n", "slow net\n", slow, nil)
	code, out := f.run("-budget", "300ms")
	_, fails := verdicts(out)
	if code != 1 || !strings.Contains(fails["slowpoke/basic"], "budget") || strings.Contains(out, "lazy") {
		t.Fatalf("default selection: want FAIL slowpoke/basic budget and no lazy; exit %d\n%s", code, out)
	}
	for _, args := range [][]string{{"-tags", "slow", "-budget", "300ms"}, {"-tags", "cc,net"}, {"lazy"}} {
		code, out = f.run(args...)
		oks, _ := verdicts(out)
		if code != 0 || !oks["lazy/basic"] || strings.Contains(out, "slowpoke") {
			t.Fatalf("%q: want only ok lazy/basic; exit %d\n%s", args, code, out)
		}
	}
	for _, args := range [][]string{{"nosuch"}, {"-tags", "fast"}, {"-check"}} {
		if code, out := f.run(args...); code != 2 {
			t.Fatalf("%q: want exit 2, got %d\n%s", args, code, out)
		}
	}
	empty := newFixture(t)
	empty.write("examples/README.md", "# examples\n")
	if code, out := empty.run(); code != 0 || !strings.Contains(out, "no program selected") {
		t.Fatalf("an empty selection: exit %d\n%s", code, out)
	}
}

// Every folder of examples/ is a program or a named non-program, and every
// program's layout is checked before any case runs, whatever the selection.
// A layout error exits 2 naming the folder.
func TestLayoutErrorsNameTheFolder(t *testing.T) {
	t.Parallel()
	one := map[string]map[string]string{"basic": {"expected.out": ""}}
	layouts := []struct {
		folder string
		make   func(f *fixture)
		args   []string
	}{
		{"mixed", func(f *fixture) { f.program("mixed", "#!/bin/sh\n", "default net\n", one, nil) }, nil},
		{"unknown", func(f *fixture) { f.program("unknown", "#!/bin/sh\n", "fast\n", one, nil) }, nil},
		{"spaced", func(f *fixture) { f.program("spaced", "#!/bin/sh\n", "slow  net\n", one, nil) }, nil},
		{"lines", func(f *fixture) { f.program("lines", "#!/bin/sh\n", "slow\nnet\n", one, nil) }, nil},
		{"untagged", func(f *fixture) { f.program("untagged", "#!/bin/sh\n", "-", one, nil) }, nil},
		{"stray", func(f *fixture) { f.write("examples/stray/README.md", "# stray\n") }, nil},
		{"nogolden", func(f *fixture) {
			f.program("nogolden", "#!/bin/sh\n", "default\n", map[string]map[string]string{"basic": {"args": "a\n"}}, nil)
		}, nil},
		{"typo", func(f *fixture) {
			f.program("typo", "#!/bin/sh\n", "default\n", map[string]map[string]string{"basic": {"expected.out": "", "expected.stdout": ""}}, nil)
		}, nil},
		{"badenv", func(f *fixture) {
			f.program("badenv", "#!/bin/sh\n", "default\n", map[string]map[string]string{"basic": {"expected.out": "", "env": "FOO\n"}}, nil)
		}, nil},
		{"badstatus", func(f *fixture) {
			f.program("badstatus", "#!/bin/sh\n", "default\n", map[string]map[string]string{"basic": {"expected.out": "", "expected.status": "01\n"}}, nil)
		}, nil},
		{"nocases", func(f *fixture) { f.program("nocases", "#!/bin/sh\n", "default\n", nil, nil) }, nil},
		{"emptycases", func(f *fixture) {
			f.program("emptycases", "#!/bin/sh\n", "default\n", nil, nil)
			if err := os.Mkdir(filepath.Join(f.root, "examples/emptycases/cases"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, nil},
		{"halfport", func(f *fixture) {
			f.program("halfport", "#!/bin/sh\n", "default\n", one, map[string]string{"record.sh": "#!/bin/sh\n"})
		}, nil},
		{"extra", func(f *fixture) {
			f.program("extra", "#!/bin/sh\n", "default\n", one, map[string]string{"notes.txt": "x\n"})
		}, nil},
		{"loose.ergo", func(f *fixture) { f.write("examples/loose.ergo", "x\n") }, nil},
		{"term", func(f *fixture) { f.program("term", "#!/bin/sh\n", "tty\n", one, nil) }, []string{"-tags", "tty"}},
		{"window", func(f *fixture) { f.program("window", "#!/bin/sh\n", "gui\n", one, nil) }, []string{"window"}},
		// A layout error outside the selection still stops the run.
		{"stray", func(f *fixture) { f.write("examples/stray/README.md", "# stray\n") }, []string{"good"}},
	}
	for _, layout := range layouts {
		f := newFixture(t)
		f.good()
		layout.make(f)
		code, out := f.run(layout.args...)
		if code != 2 || !strings.Contains(out, "examples/"+layout.folder) {
			t.Errorf("%s %q: want exit 2 naming examples/%s; exit %d\n%s", layout.folder, layout.args, layout.folder, code, out)
		}
		if strings.Contains(out, "ok good/") {
			t.Errorf("%s: a case ran before the layout was checked\n%s", layout.folder, out)
		}
	}
	f := newFixture(t)
	f.good()
	f.program("term", "#!/bin/sh\n", "tty\n", one, nil)
	f.write("examples/README.md", "# examples\n")
	f.write("examples/editions/sketch/main.ergo", "#!/bin/sh\n# UNFORMATTED BUILDFAIL\n")
	f.write("examples/editions/sketch/expected.out", "nothing\n")
	code, out := f.run()
	if oks, _ := verdicts(out); code != 0 || !oks["good/basic"] {
		t.Fatalf("a root with editions and a tty program: exit %d\n%s", code, out)
	}
}

// A program that fails fmt or build fails alone: its cases do not run, the
// other programs' do, and the runner writes nothing into the root.
func TestFmtOrBuildFailureFailsOnlyThatProgram(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.good()
	hi := map[string]map[string]string{"basic": {"expected.out": "hi\n"}}
	f.program("nobuild", "#!/bin/sh\n# BUILDFAIL\necho hi\n", "default\n", hi, nil)
	f.program("messy", "#!/bin/sh\necho hi\n", "default\n", hi, map[string]string{"part.ergo": "# UNFORMATTED\n"})
	before := tree(t, f.root)
	code, out := f.run()
	oks, fails := verdicts(out)
	if code != 1 || !strings.Contains(out, "FAIL nobuild: build\n") || !strings.Contains(out, "FAIL messy: fmt\n") || !oks["good/basic"] {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if oks["nobuild/basic"] || oks["messy/basic"] || fails["nobuild/basic"] != "" || fails["messy/basic"] != "" {
		t.Fatalf("a failed program's cases ran\n%s", out)
	}
	if after := tree(t, f.root); !slices.Equal(before, after) {
		t.Fatalf("the runner wrote into the root:\nbefore %q\nafter  %q", before, after)
	}
	calls := readCalls(t, f)
	for _, want := range []string{
		"fmt --check " + filepath.Join(f.root, "examples/messy/main.ergo") + " " + filepath.Join(f.root, "examples/messy/part.ergo"),
		"build " + filepath.Join(f.root, "examples/good/main.ergo") + " -o ",
	} {
		if !slices.ContainsFunc(calls, func(c string) bool { return strings.HasPrefix(c, want) }) {
			t.Errorf("no call starting %q in %q", want, calls)
		}
	}
}

func tree(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		files = append(files, strings.TrimPrefix(name, root))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func readCalls(t *testing.T, f *fixture) []string {
	t.Helper()
	content, err := os.ReadFile(f.calls)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return lines(content)
}

func read(t *testing.T, name string) (string, bool) {
	t.Helper()
	content, err := os.ReadFile(name)
	if os.IsNotExist(err) {
		return "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(content), true
}

// -record writes a port's goldens from its original (record.sh), not from
// the port; -record -check compares them and writes nothing.
func TestRecordLegWritesGoldensFromTheOriginal(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.good()
	original := func(word string) string {
		return "#!/bin/sh\nif [ \"$1\" = quiet ]; then exit 0; fi\nprintf '" + word + ":%s\\n' \"$@\"\nprintf 'warn\\n' >&2\nexit 3\n"
	}
	port := f.program("porter", original("orig"), "default\n", map[string]map[string]string{
		"r1": {"args": "a\n"},
		"r2": {"args": "quiet\n", "expected.out": "stale\n", "expected.err": "stale\n", "expected.status": "9\n"},
	}, map[string]string{"record.sh": original("orig"), "origin": "a fixture original\n"})
	drift := f.program("drifter", original("port"), "default\n", map[string]map[string]string{"r1": {"args": "a\n"}},
		map[string]string{"record.sh": original("orig"), "origin": "a fixture original\n"})
	code, out := f.run("-record", "porter", "drifter")
	if code != 0 || !strings.Contains(out, "recorded porter/r1\n") {
		t.Fatalf("-record: exit %d\n%s", code, out)
	}
	want := map[string]string{
		"porter/cases/r1/expected.out":    "orig:a\n",
		"porter/cases/r1/expected.err":    "warn\n",
		"porter/cases/r1/expected.status": "3\n",
		"porter/cases/r2/expected.out":    "",
		"porter/cases/r2/expected.err":    "-",
		"porter/cases/r2/expected.status": "-",
		"drifter/cases/r1/expected.out":   "orig:a\n",
	}
	for file, content := range want {
		got, ok := read(t, filepath.Join(f.root, "examples", file))
		if content == "-" && ok {
			t.Errorf("%s: a stale golden was kept: %q", file, got)
		}
		if content != "-" && (!ok || got != content) {
			t.Errorf("%s: want %q, got %q (present %t)", file, content, got, ok)
		}
	}
	if code, out := f.run("porter"); code != 0 {
		t.Fatalf("the port against its recorded goldens: exit %d\n%s", code, out)
	}
	code, out = f.run("drifter")
	if _, fails := verdicts(out); code != 1 || !strings.Contains(fails["drifter/r1"], "stdout") {
		t.Fatalf("a port that drifted from its original: want FAIL drifter/r1 stdout; exit %d\n%s", code, out)
	}
	if code, out := f.run("-record", "-check", "porter", "drifter"); code != 0 {
		t.Fatalf("-record -check on fresh goldens: exit %d\n%s", code, out)
	}
	golden := filepath.Join(port, "cases/r1/expected.out")
	f.write("examples/porter/cases/r1/expected.out", "orig:b\n")
	code, out = f.run("-record", "-check", "porter")
	if _, fails := verdicts(out); code != 1 || !strings.Contains(fails["porter/r1"], "stdout") {
		t.Fatalf("-record -check after an edit: want FAIL porter/r1 stdout; exit %d\n%s", code, out)
	}
	if got, _ := read(t, golden); got != "orig:b\n" {
		t.Fatalf("-record -check wrote a golden: %q", got)
	}
	if code, out := f.run("-record", "good"); code != 2 || !strings.Contains(out, "record.sh") {
		t.Fatalf("-record on a program without record.sh: want exit 2 naming record.sh; exit %d\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(drift, "cases/r1/expected.err")); err != nil {
		t.Fatalf("drifter's recorded stderr: %v", err)
	}
}

// When the original cannot start, -record fails the case and writes nothing:
// the goldens keep their bytes. A command that never ran has no output to
// record. Each cause of a start failure that a fixture can show is a row.
func TestRecordLegWritesNothingWhenTheOriginalCannotStart(t *testing.T) {
	goldens := map[string]string{"expected.out": "original out\n", "expected.err": "original err\n", "expected.status": "3\n"}
	port := func(t *testing.T, stdin bool) (*fixture, string) {
		f := newFixture(t)
		files := map[string]string{}
		for file, content := range goldens {
			files[file] = content
		}
		if stdin {
			files["stdin"] = "input\n"
		}
		dir := f.program("port", "#!/bin/sh\necho port\n", "default\n", map[string]map[string]string{"basic": files},
			map[string]string{"record.sh": "echo original out; echo original err >&2; exit 3\n", "origin": "a fixture original\n"})
		return f, filepath.Join(dir, "cases", "basic")
	}
	check := func(t *testing.T, f *fixture, caseDir string) {
		t.Helper()
		code, out := f.run("-record", "port")
		_, fails := verdicts(out)
		if code != 1 || !strings.HasPrefix(fails["port/basic"], " could not start: ") || strings.Contains(out, "recorded port/basic") {
			t.Errorf("-record: want exit 1, FAIL port/basic: could not start, and no recorded line; exit %d\n%s", code, out)
		}
		for file, want := range goldens {
			if got, ok := read(t, filepath.Join(caseDir, file)); !ok || got != want {
				t.Errorf("%s: want %q kept, got %q (present %t)", file, want, got, ok)
			}
		}
		code, out = f.run("-record", "-check", "port")
		if _, fails := verdicts(out); code != 1 || !strings.HasPrefix(fails["port/basic"], " could not start: ") {
			t.Errorf("-record -check: want exit 1 and FAIL port/basic: could not start; exit %d\n%s", code, out)
		}
	}
	t.Run("no sh on PATH", func(t *testing.T) {
		f, caseDir := port(t, false)
		t.Setenv("PATH", filepath.Join(t.TempDir(), "no-sh-here"))
		check(t, f, caseDir)
	})
	t.Run("stdin cannot be opened", func(t *testing.T) {
		f, caseDir := port(t, true)
		stdin := filepath.Join(caseDir, "stdin")
		if err := os.Chmod(stdin, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(stdin, 0o644) })
		if file, err := os.Open(stdin); err == nil {
			file.Close()
			t.Skip("this user can open a file of mode 000 (root or CAP_DAC_OVERRIDE), so the row has no unopenable stdin")
		}
		check(t, f, caseDir)
	})
}

// execute returns an error, and no outcome, for each cause of a start
// failure that a test can make: a stdin that cannot be opened, a command that
// cannot be started, and a run that a signal is stopping. A command that ran
// returns no error.
func TestExecuteReportsAStartFailureAsAnError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing")
	stopping := newLive()
	stopping.closed = true
	rows := []struct {
		what  string
		live  *live
		argv  []string
		stdin string
	}{
		{"stdin cannot be opened", newLive(), []string{"sh", "-c", "exit 0"}, missing},
		{"command cannot be started", newLive(), []string{missing}, ""},
		{"the run is stopping", stopping, []string{"sh", "-c", "exit 0"}, ""},
	}
	for _, row := range rows {
		o, err := row.live.execute(row.argv, dir, os.Environ(), row.stdin, 10*time.Second, 0)
		if err == nil {
			t.Errorf("%s: no error; outcome %+v", row.what, o)
		}
	}
	if o, err := newLive().execute([]string{"sh", "-c", "exit 4"}, dir, os.Environ(), "", 10*time.Second, 0); err != nil || o.status != 4 {
		t.Errorf("a command that ran: status %d, error %v; want status 4 and no error", o.status, err)
	}
}

// The test leg runs ergo test DIR/main.ergo for each program, one that
// declares no test or law too (its test build may run a std law against
// its provider; fu6b), after its build; a failure fails the program and
// its cases still run.
// The checkout's compiler has ergo test, so the reply of a compiler without
// it (the pending line) is a failure like any other, never a skip.
func TestTestLeg(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	hi := map[string]map[string]string{"basic": {"expected.out": "hi\n"}}
	marked := func(marker string) string {
		return "#!/bin/sh\necho hi\nexit 0\n# " + marker + "\ntest Fixture {\n}\n"
	}
	f.program("passing", marked("-"), "default\n", hi, nil)
	f.program("failing", marked("TESTFAIL"), "default\n", hi, nil)
	f.program("pending", marked("TESTPENDING"), "default\n", hi, nil)
	f.program("untested", "#!/bin/sh\necho hi\n# test is not a declaration here\n", "default\n", hi, nil)
	f.program("other", "#!/bin/sh\necho hi\n", "default\n", hi, map[string]string{"more.ergo": "test InAnotherFile {\n}\n"})
	code, out := f.run()
	if code != 1 {
		t.Fatalf("a failing test: want exit 1, got %d\n%s", code, out)
	}
	for _, want := range []string{"ok passing: test (", "FAIL failing: test\n", "    FAIL Fixture: assert failed", "FAIL pending: test\n", "    ergo test: not available yet", "ok other: test (", "ok untested: test ("} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q\n%s", want, out)
		}
	}
	oks, _ := verdicts(out)
	for _, p := range []string{"passing", "failing", "pending", "untested", "other"} {
		if !oks[p+"/basic"] {
			t.Errorf("no ok %s/basic: a test result must not stop the cases\n%s", p, out)
		}
	}
	var tests []string
	for _, call := range readCalls(t, f) {
		if strings.HasPrefix(call, "test ") {
			tests = append(tests, call)
		}
	}
	var want []string
	for _, p := range []string{"failing", "other", "passing", "pending", "untested"} {
		want = append(want, "test "+filepath.Join(f.root, "examples", p, "main.ergo"))
	}
	if !slices.Equal(tests, want) {
		t.Fatalf("ergo test calls:\n%q\nwant\n%q", tests, want)
	}
	if code, out := f.run("pending"); code != 1 || strings.Contains(out, "skip") {
		t.Fatalf("a pending reply alone: want exit 1 and no skip, got %d\n%s", code, out)
	}
}

// A skip line that the program's expected-skips file does not list fails
// the test leg (fs4); a listed one passes, and the cases still run.
func TestTestLegFailsAnUnexpectedSkip(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	hi := map[string]map[string]string{"basic": {"expected.out": "hi\n"}}
	src := "#!/bin/sh\necho hi\nexit 0\n# TESTSKIP\ntest Fixture {\n}\n"
	line := "skip law Store.Keeps for Box: its values have no generator: why"
	f.program("skipping", src, "default\n", hi, nil)
	f.program("listed", src, "default\n", hi, map[string]string{"expected-skips": "# Box holds a function\n" + line + "\n"})
	code, out := f.run()
	if code != 1 {
		t.Fatalf("an unexpected skip: want exit 1, got %d\n%s", code, out)
	}
	for _, want := range []string{"FAIL skipping: test: 1 unexpected skips", "    " + line + "\n", "ok listed: test ("} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q\n%s", want, out)
		}
	}
	oks, _ := verdicts(out)
	if !oks["skipping/basic"] || !oks["listed/basic"] {
		t.Errorf("a skip must not stop the cases\n%s", out)
	}
}

// An ergo test that cannot start fails the test leg and the run, as any
// other failure of the leg does; the cases still run (apps-1 G1 r2 m2).
func TestTestLegThatCannotStartFails(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	// The fake compiler moves itself away after the build.
	f.program("gone", "#!/bin/sh\necho hi\nexit 0\n# SELFDESTRUCT\ntest Gone {\n}\n", "default\n",
		map[string]map[string]string{"basic": {"expected.out": "hi\n"}}, nil)
	code, out := f.run("gone")
	oks, _ := verdicts(out)
	if code != 1 || !strings.Contains(out, "FAIL gone: test: could not start: ") || strings.Contains(out, "ok gone: test") || !oks["gone/basic"] {
		t.Fatalf("want exit 1, FAIL gone: test: could not start, no ok for the test leg, and ok gone/basic; exit %d\n%s", code, out)
	}
	if !strings.Contains(out, "examples: FAILED: gone: test\n") {
		t.Fatalf("the start failure is not counted\n%s", out)
	}
}

func TestUnifiedDiff(t *testing.T) {
	t.Parallel()
	got := unifiedDiff([]byte("a\nb\nc\n"), []byte("a\nB\nc"))
	want := "--- want\n+++ got\n@@ -1,3 +1,3 @@\n a\n-b\n-c\n+B\n+c\n\\ No newline at end of file\n"
	if got != want {
		t.Fatalf("diff:\n%s\nwant:\n%s", got, want)
	}
	long := strings.Repeat("same\n", 20)
	got = unifiedDiff([]byte("x\n"+long+"y\n"), []byte("X\n"+long+"Y\n"))
	if strings.Count(got, "@@ -") != 2 || !strings.Contains(got, "@@ -1,4 +1,4 @@\n-x\n+X\n same\n") || !strings.Contains(got, "@@ -19,4 +19,4 @@\n same\n same\n same\n-y\n+Y\n") {
		t.Fatalf("two distant changes are not two hunks:\n%s", got)
	}
	if got := unifiedDiff(nil, []byte("new\n")); got != "--- want\n+++ got\n@@ -0,0 +1,1 @@\n+new\n" {
		t.Fatalf("diff from empty:\n%s", got)
	}
}
