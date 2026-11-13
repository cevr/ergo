package main

import (
	"bytes"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ergo.local/prototype/internal/testwait"
)

// warmModule writes the module the warm tests build: a main package that
// imports a library package, which embeds an asset. value and banner are
// one digit and one letter, so an edit of either keeps the file's size.
func warmModule(t *testing.T, root string, value int, banner byte) {
	t.Helper()
	smokeWrite(t, root, "ergo.mod", "module warm\n")
	smokeWrite(t, root, "lib/lib.ergo", fmt.Sprintf("// Package lib is the warm tests' library.\n\nlet Banner = embedded[string](\"banner.txt\")\n\n// Value is the library's number.\nfunc Value() int {\n\t%d\n}\n", value))
	smokeWrite(t, root, "lib/banner.txt", "banner "+string(banner)+"\n")
	smokeWrite(t, root, "cmd/app/main.ergo", "import io\nimport os\nimport warm/lib\n\nfunc main() fails os.Exit needs Ambient {\n\twith context { io.Stdout: io.NewHost() } {\n\t\tio.Stdout.Println(\"\\(lib.Banner) \\(lib.Value())\")\n\t}\n}\n")
}

// warmRun is one `ergo` run of the warm tests: its exit status, stdout and
// stage counters. Every run clears GOFLAGS: a build's warm answer is the
// direct path's, which a GOFLAGS of the caller's (the mutant runner's -p)
// would turn away.
type warmRun struct {
	code   int
	stdout string
	counts map[string]int64
}

func runWarm(t *testing.T, bin, root, xdg string, env []string, args ...string) warmRun {
	t.Helper()
	report := filepath.Join(t.TempDir(), "stage.json")
	command := exec.Command(bin, args...)
	command.Dir = root
	command.Env = append(append(os.Environ(), "GOFLAGS=", "XDG_CACHE_HOME="+xdg, "ERGO_STAGE_REPORT="+report), env...)
	out, stderr, code := runSmokeCommand(t, command)
	if code > 1 {
		t.Fatalf("ergo %v: exit %d: %s", args, code, stderr)
	}
	return warmRun{code: code, stdout: string(out), counts: readStageReport(t, report)}
}

// TestWarmRequestDoesNoWork: a second identical `ergo build` and `ergo
// check` of a package folder answer from the store: every work counter is
// 0 (nothing parsed, checked, lowered, compiled, linked, started, written
// or placed), and the bytes read and hashed are exactly the inputs' bytes
// (the .ergo files, ergo.mod and the asset), each read once. A build to a
// new output path places it and does nothing else. It kills
// cs-warm-places-again, which only these counts see. It builds Go because
// the warm answer of a build is about the direct path's store and its
// executable.
func TestWarmRequestDoesNoWork(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier: builds Go")
	}
	bin := buildTestCLI(t)
	root := physicalTempDir(t)
	xdg := physicalTempDir(t)
	warmModule(t, root, 1, 'a')
	var inputs int64
	for _, name := range []string{"ergo.mod", "lib/lib.ergo", "lib/banner.txt", "cmd/app/main.ergo"} {
		info, err := os.Stat(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		inputs += info.Size()
	}
	out := filepath.Join(root, "dist", "app")
	for _, args := range [][]string{{"build", "./cmd/app", "-o", out}, {"check", "./cmd/app"}} {
		cold := runWarm(t, bin, root, xdg, nil, args...)
		warm := runWarm(t, bin, root, xdg, nil, args...)
		if cold.code != 0 || warm.code != 0 || warm.stdout != cold.stdout {
			t.Fatalf("%v: cold exit %d, warm exit %d; stdout %q, then %q", args, cold.code, warm.code, cold.stdout, warm.stdout)
		}
		for k, v := range warm.counts {
			want := int64(0)
			switch k {
			case "files_read":
				want = 4
			case "bytes_read", "bytes_hashed":
				want = inputs
			}
			if v != want {
				t.Errorf("warm %v: %s = %d, want %d", args, k, v, want)
			}
		}
	}
	moved := runWarm(t, bin, root, xdg, nil, "build", "./cmd/app", "-o", out+"2")
	if moved.counts["outputs_placed"] != 1 || moved.counts["files_parsed"] != 0 || moved.counts["bytes_written"] != 0 {
		t.Fatalf("a warm build to a new output: %v", moved.counts)
	}
	a, err := os.Stat(out)
	b, err2 := os.Stat(out + "2")
	if err != nil || err2 != nil || !os.SameFile(a, b) {
		t.Fatalf("the two outputs are not the store's one file: %v %v", err, err2)
	}
}

// warmWorld is the state TestWarmAnswerAgreesWithFullBuild edits.
type warmWorld struct {
	root          string
	value         int
	banner        byte
	extra, hidden bool // lib/extra.ergo exists; it is named _extra.ergo
	seed          int  // check's --seed
	probe         bool // ERGO_WARM_PROBE=1 in the environment
	other         bool // the second compiler binary
}

// TestWarmAnswerAgreesWithFullBuild is the model-based property of the warm
// answer (models/warmstamp): seeded sequences of edits of the world, and
// after each one an `ergo build` and an `ergo check` on the warm path
// against the reference, ERGO_BUILD=full in a cache of its own, which
// reads, checks and keys every request from the files. After every step
// the exit statuses, the check output and the executable's bytes are the
// reference's, and the warm path does the model's least work: a step that
// changes no input and no part of the request (rewriting the same bytes, a
// touch, deleting or replacing the output, two builds at once) parses,
// compiles, links and starts nothing; a step that changes them (a byte of a
// source or of the asset with its size and modification time kept, adding,
// removing or renaming a file, check's --seed, an ERGO_* variable, another
// compiler) runs the normal path, which reads each input once, as the
// reference does. A trim of the executable's blob builds again. A failing
// seed prints its steps; replay with ERGO_WARM_SEED=N.
//
// It kills cs-warm-key-omits-new-file, cs-warm-key-omits-asset,
// cs-warm-key-omits-flag, cs-warm-key-omits-release, cs-warm-trusts-mtime,
// cs-warm-output-by-record, cs-warm-latest-only and cs-warm-rereads. It
// builds Go because the property is about the executables a build places.
func TestWarmAnswerAgreesWithFullBuild(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier: builds Go")
	}
	bin := buildTestCLI(t)
	// Another compiler: the same program with one more byte, so its
	// identity (the digest of its image) differs.
	other := filepath.Join(t.TempDir(), "ergo-other")
	data, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, append(data, 0), 0o755); err != nil {
		t.Fatal(err)
	}
	seeds := []uint64{1, 2}
	if s := os.Getenv("ERGO_WARM_SEED"); s != "" {
		var seed uint64
		fmt.Sscan(s, &seed)
		seeds = []uint64{seed}
	}
	for _, seed := range seeds {
		warmSequence(t, seed, bin, other)
	}
}

func warmSequence(t *testing.T, seed uint64, bin, other string) {
	r := rand.New(rand.NewPCG(seed, 7))
	w := warmWorld{root: physicalTempDir(t), value: 1, banner: 'a', seed: 1}
	warmModule(t, w.root, w.value, w.banner)
	warmXDG, refXDG := physicalTempDir(t), physicalTempDir(t)
	out, ref := filepath.Join(w.root, "dist", "app"), filepath.Join(t.TempDir(), "ref")
	var steps []string
	// version counts the edits of the inputs; seen is, for each request
	// (compiler, environment and, for check, --seed), the version its last
	// successful run answered, which a warm manifest now holds.
	version, seen := 0, map[string]int{}
	fail := func(format string, args ...any) {
		t.Helper()
		t.Fatalf("seed %d, after %s: %s (replay with ERGO_WARM_SEED=%d)", seed, strings.Join(steps, ", "), fmt.Sprintf(format, args...), seed)
	}
	lib := filepath.Join(w.root, "lib", "lib.ergo")
	keepStamp := func(path string, edit func()) {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		edit()
		if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
			t.Fatal(err)
		}
	}
	extraPath := func() string {
		name := "extra.ergo"
		if w.hidden {
			name = "_extra.ergo"
		}
		return filepath.Join(w.root, "lib", name)
	}
	for i := range 14 {
		edited, concurrent, trimmed := false, false, false
		var step string
		switch k := r.IntN(13); {
		case i == 0:
			step, edited = "first build", true
		case k == 0:
			step = "rewrite the same bytes"
			path := filepath.Join(w.root, []string{"ergo.mod", "lib/lib.ergo", "lib/banner.txt", "cmd/app/main.ergo"}[r.IntN(4)])
			data, _ := os.ReadFile(path)
			os.WriteFile(path, data, 0o644)
		case k == 1:
			step = "touch"
			later := time.Now().Add(time.Duration(1+r.IntN(5)) * time.Second)
			os.Chtimes(lib, later, later)
		case k == 2:
			w.value = (w.value + 1 + r.IntN(8)) % 10
			step, edited = fmt.Sprintf("value %d, size and time kept", w.value), true
			keepStamp(lib, func() { warmModule(t, w.root, w.value, w.banner) })
		case k == 3:
			w.banner = byte('a' + (int(w.banner-'a')+1+r.IntN(3))%4)
			step, edited = fmt.Sprintf("asset %c, size and time kept", w.banner), true
			keepStamp(filepath.Join(w.root, "lib", "banner.txt"), func() { warmModule(t, w.root, w.value, w.banner) })
		case k == 4 && !w.extra:
			step, edited, w.extra, w.hidden = "add lib/extra.ergo (a second Value)", true, true, false
			smokeWrite(t, w.root, "lib/extra.ergo", "func Value() int {\n\t7\n}\n")
		case k == 4:
			step, edited = "remove the extra file", true
			os.Remove(extraPath())
			w.extra = false
		case k == 5 && w.extra:
			from := extraPath()
			w.hidden = !w.hidden
			step, edited = "rename the extra file to "+filepath.Base(extraPath()), true
			os.Rename(from, extraPath())
		case k == 5:
			step = "touch the folder"
			later := time.Now().Add(time.Second)
			os.Chtimes(filepath.Join(w.root, "lib"), later, later)
		case k == 6:
			w.seed = 3 - w.seed
			step = fmt.Sprintf("check --seed %d", w.seed)
		case k == 7:
			w.probe = !w.probe
			step = fmt.Sprintf("ERGO_WARM_PROBE set %t", w.probe)
		case k == 8:
			w.other = !w.other
			step = fmt.Sprintf("the other compiler %t", w.other)
		case k == 9:
			step = "delete the output"
			os.Remove(out)
		case k == 10:
			step = "replace the output with other bytes of its size"
			if data, err := os.ReadFile(out); err == nil {
				data[len(data)/2] ^= 0xff
				os.Remove(out)
				os.WriteFile(out, data, 0o755)
			}
		case k == 11:
			step, trimmed = "trim the output's blob", true
			trimBlobOf(t, warmXDG, out)
		default:
			step, concurrent = "two builds at once", true
		}
		steps = append(steps, step)
		compiler := bin
		if w.other {
			compiler = other
		}
		var env []string
		if w.probe {
			env = append(env, "ERGO_WARM_PROBE=1")
		}
		seedArg := fmt.Sprint(w.seed)
		refBuild := runWarm(t, compiler, w.root, refXDG, append(env, "ERGO_BUILD=full"), "build", "./cmd/app", "-o", ref)
		refCheck := runWarm(t, compiler, w.root, refXDG, append(env, "ERGO_BUILD=full"), "check", "./cmd/app", "--seed", seedArg)
		var builds []warmRun
		if concurrent {
			builds = concurrentBuilds(t, compiler, w.root, warmXDG, env, out)
		} else {
			builds = []warmRun{runWarm(t, compiler, w.root, warmXDG, env, "build", "./cmd/app", "-o", out)}
		}
		check := runWarm(t, compiler, w.root, warmXDG, env, "check", "./cmd/app", "--seed", seedArg)
		if check.code != refCheck.code || check.stdout != refCheck.stdout {
			fail("check: exit %d %q, the reference exit %d %q", check.code, check.stdout, refCheck.code, refCheck.stdout)
		}
		for _, b := range builds {
			if b.code != refBuild.code {
				fail("build: exit %d, the reference %d", b.code, refBuild.code)
			}
		}
		if refBuild.code == 0 {
			got, err := os.ReadFile(out)
			want, err2 := os.ReadFile(ref)
			if err != nil || err2 != nil || !bytes.Equal(got, want) {
				fail("the output is not the reference's executable (%v, %v)", err, err2)
			}
		}
		if edited {
			version++
		}
		buildReq := fmt.Sprint(compiler, w.probe)
		checkReq := fmt.Sprint(compiler, w.probe, w.seed)
		runs := []struct {
			req  string
			run  warmRun
			sure bool // whether a miss must run the normal path
		}{{checkReq, check, true}}
		for _, b := range builds {
			runs = append(runs, struct {
				req  string
				run  warmRun
				sure bool
			}{buildReq, b, !concurrent && !trimmed})
		}
		for _, x := range runs {
			parsed := x.run.counts["files_parsed"]
			last, known := seen[x.req]
			switch {
			case known && last == version && !(trimmed && x.req == buildReq):
				// The model's hit: the request and its inputs as last answered.
				if parsed != 0 || x.run.counts["go_compiles"] != 0 || x.run.counts["links"] != 0 || x.run.counts["processes"] != 0 {
					fail("an unchanged request did work: %v", x.run.counts)
				}
			case x.sure:
				// The normal path ran, reading each input once.
				if parsed == 0 {
					fail("a changed request was answered warm: %v", x.run.counts)
				}
				if reads := x.run.counts["files_read"]; reads != refCheck.counts["files_read"] {
					fail("the normal path read %d files, the reference %d", reads, refCheck.counts["files_read"])
				}
			}
			if x.run.code == 0 {
				seen[x.req] = version
			}
		}
	}
}

// concurrentBuilds runs two warm builds to out at once.
func concurrentBuilds(t *testing.T, bin, root, xdg string, env []string, out string) []warmRun {
	t.Helper()
	runs := make([]warmRun, 2)
	errs := make([]error, 2)
	reports := make([]string, 2)
	var wg sync.WaitGroup
	for i := range runs {
		report := filepath.Join(t.TempDir(), "stage.json")
		reports[i] = report
		command := exec.Command(bin, "build", "./cmd/app", "-o", out)
		command.Dir = root
		command.Env = append(append(os.Environ(), "GOFLAGS=", "XDG_CACHE_HOME="+xdg, "ERGO_STAGE_REPORT="+report), env...)
		wg.Add(1)
		go func() {
			defer wg.Done()
			var code int
			_, _, code, errs[i] = runSmokeDeadline(command, testwait.Left(t))
			runs[i].code = code
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
		runs[i].counts = readStageReport(t, reports[i])
	}
	return runs
}

// trimBlobOf removes the store's blob that out is, as a trim may at any
// time.
func trimBlobOf(t *testing.T, xdg, out string) {
	t.Helper()
	info, err := os.Stat(out)
	if err != nil {
		return
	}
	blobs := filepath.Join(xdg, "ergo", "go", "blobs")
	filepath.WalkDir(blobs, func(path string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return nil
		}
		if b, err := os.Stat(path); err == nil && os.SameFile(b, info) {
			os.Remove(path)
		}
		return nil
	})
}
