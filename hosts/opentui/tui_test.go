package opentui

import (
	"flag"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
)

var (
	agreeSeed    = flag.Uint64("agree.seed", 0, "run TestTuiHostAgreesWithTestHost on this seed only")
	examplesSeed = flag.Int("examples.seed", 0, "run TestExamplesAgreeOnTheTerminal on this seed only")
)

// TestMain gives the ergo builds an emission cache of the test process's
// own (ERGO_CACHE, as internal/testinputs does for the repository's
// tests), and removes it and the folder the binaries were built into.
func TestMain(m *testing.M) {
	flag.Parse()
	// $ERGO_TEST_CACHE keeps one across runs (a mutant run), never the
	// user's.
	cache, keep := os.Getenv("ERGO_TEST_CACHE"), true
	var err error
	if cache == "" {
		cache, err = os.MkdirTemp("", "hostsopentui-cache")
		keep = false
	}
	if err == nil {
		err = os.Setenv("ERGO_CACHE", cache)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "private ergo cache:", err)
		os.Exit(1)
	}
	code := m.Run()
	if !keep {
		os.RemoveAll(cache)
	}
	if binDir != "" {
		os.RemoveAll(binDir)
	}
	os.Exit(code)
}

var binDir string

// builtErgo is the repository's ergo command, built once into a temporary
// folder that TestMain removes; $ERGO_BIN names one already built.
var builtErgo = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp("", "hostsopentui")
	if err != nil {
		return "", err
	}
	binDir = dir
	if e := os.Getenv("ERGO_BIN"); e != "" {
		return e, nil
	}
	ergo := filepath.Join(dir, "ergo")
	if out, err := bounded("go", "build", "-C", "../..", "-o", ergo, "./cmd/ergo").CombinedOutput(); err != nil {
		return "", fmt.Errorf("go build ergo: %v\n%s", err, out)
	}
	return ergo, nil
})

// build is ergo build of the Ergo program at src into the binary folder,
// once per name.
func build(name, src string) (string, error) {
	built.Lock()
	defer built.Unlock()
	if b, ok := built.bins[name]; ok {
		return b, nil
	}
	ergo, err := builtErgo()
	if err != nil {
		return "", err
	}
	bin := filepath.Join(binDir, name)
	if out, err := bounded(ergo, "build", src, "-o", bin).CombinedOutput(); err != nil {
		return "", fmt.Errorf("ergo build %s: %v\n%s", src, err, out)
	}
	built.bins[name] = bin
	return bin, nil
}

var built = struct {
	sync.Mutex
	bins map[string]string
}{bins: map[string]string{}}

// bounded is a command whose data segment the kernel holds to 4 GiB on
// Linux (prlimit), so a program that grows stops with ENOMEM before it
// reaches the host's memory. Darwin's kernel enforces no such limit.
func bounded(name string, args ...string) *exec.Cmd {
	if runtime.GOOS == "linux" {
		if p, err := exec.LookPath("prlimit"); err == nil {
			return exec.Command(p, append([]string{"--data=4294967296", "--", name}, args...)...)
		}
	}
	return exec.Command(name, args...)
}

// play runs bin on the script and is its output, with its error.
func play(bin, script string, args ...string) (string, error) {
	cmd := bounded(bin, args...)
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// agreeApps are the apps of cmd/hostagree; seed s plays app s mod their count.
var agreeApps = []string{"counter", "shelf", "form", "load", "lock", "pad", "list", "sketch", "spin", "drag"}

// words are texts a script types: empty, ASCII, and runes of two and
// three bytes (a wide one too), so Backspace must take a rune, not a byte.
var words = []string{"", "a", "bob", "é", "日本", "zé日"}

func pick[T any](r *rand.Rand, s ...T) T { return s[r.IntN(len(s))] }

// genScript is the hostagree input of one seed: the app, then 4 to 19
// steps its labels make meaningful, a few that name no node, and focus
// moves of 0 to 5 Tabs.
func genScript(r *rand.Rand, app string) string {
	lines := []string{app}
	if app == "shelf" {
		// rows past the list's fold of four from the start
		for range 6 {
			lines = append(lines, "tap add")
		}
	}
	if app == "list" {
		// a turn of the wheel while the list (60 rows) has room to move
		lines = append(lines, fmt.Sprintf("wheel list %d", 1+r.IntN(3)))
	}
	if app == "drag" {
		// a press held in the bottom band for some frames, so every drag
		// script edge-scrolls the list (mutant OT5-edge-scroll-still)
		lines = append(lines, "point drag down 40 352", fmt.Sprintf("point drag move 40 %d", 16*(36+r.IntN(3))), fmt.Sprintf("frames %d", 2+r.IntN(8)))
	}
	// held: a pad's or a sketch's pointer went down and has not come up. A person's
	// mouse cannot press a button or Tab then, so the script does not.
	held := false
	for range 4 + r.IntN(16) {
		var step string
		switch app {
		case "counter":
			step = pick(r, "tap inc", "tap dec", "tap reset", "key +", "key -", "key x", "key q", "tap nothing")
		case "shelf":
			// more adds than deletes, so the list grows past its fold
			step = pick(r, "tap add", "tap add", "tap first", fmt.Sprintf("tap del %d", 1+r.IntN(12)), "tap row 1")
		case "form":
			step = pick(r, "type name\t"+pick(r, words...), "type note\t"+pick(r, words...), "tap done", "tap bold", "tap send", "key a")
		case "load":
			step = pick(r, "tap next", "settle", "settle")
		case "lock":
			step = pick(r, "tap locked", "tap bump", "tap after", "type word\t"+pick(r, words...))
		case "pad":
			// whole cells of the pad (8 by 16 DIPs), below its text and
			// its button; a wheel turn is 3 rows (48 DIPs) down
			x := 8 * r.IntN(25)
			if r.IntN(5) == 0 {
				step = fmt.Sprintf("point pad wheel %d 48", x)
			} else {
				step = fmt.Sprintf("point pad %s %d %d", pick(r, "down", "up", "move", "move"), x, 16*(2+r.IntN(5)))
			}
			if r.IntN(8) == 0 && !held {
				// over the pad's button (inside the Area: no crossing),
				// a tap on it, or out of every Area (the pad's Leave)
				step = pick(r, "tap clear", "tap reset", "point clear move 0 0", "leave")
			}
		case "drag":
			// a press, moves and frames in the Area: y in its top band, in its
			// bottom band (the screen is 40 rows) or its middle
			y := pick(r, 16*r.IntN(2), 16*(36+r.IntN(3)), 16*(5+r.IntN(25)))
			step = pick(r, fmt.Sprintf("point drag down 40 %d", y), fmt.Sprintf("point drag move 40 %d", y), fmt.Sprintf("point drag move 40 %d", y), "point drag up 40 0",
				fmt.Sprintf("frames %d", 1+r.IntN(12)), fmt.Sprintf("frames %d", 1+r.IntN(12)))
		case "spin":
			// frames a few at a time, so a spin (12 frames) ends in some
			// scripts and runs through others
			step = pick(r, "tap go", fmt.Sprintf("frames %d", 1+r.IntN(4)), fmt.Sprintf("frames %d", 1+r.IntN(4)), "cost frames 1")
		case "sketch":
			step = fmt.Sprintf("point sketch %s %d %d", pick(r, "down", "down", "up", "move"), 8*r.IntN(25), 16*r.IntN(10))
		case "list":
			// the wheel turns down more than up, so it moves a list that
			// starts at its top
			wheel := fmt.Sprintf("wheel list %d", pick(r, -2, -1, 1, 2, 3))
			step = pick(r, "tap more", "tap less", "tap note", "tap down", "tap up", fmt.Sprintf("scroll list %d", 16*r.IntN(190)),
				wheel, wheel, fmt.Sprintf("tap row %d", r.IntN(90)), fmt.Sprintf("tap row %d", r.IntN(20)))
		}
		if r.IntN(5) == 0 && !held {
			step = fmt.Sprintf("focus %d", r.IntN(6))
		}
		if strings.HasPrefix(step, "point pad down ") || strings.HasPrefix(step, "point sketch down ") || strings.HasPrefix(step, "point drag down ") {
			held = true
		} else if strings.HasPrefix(step, "point pad up ") || strings.HasPrefix(step, "point sketch up ") || strings.HasPrefix(step, "point drag up ") {
			held = false
		}
		lines = append(lines, step)
	}
	return strings.Join(lines, "\n") + "\n"
}

// TestTuiHostAgreesWithTestHost: the terminal driver (tui), fed terminal
// bytes through a headless screen, agrees with ui.TestHost on seeded scene
// scripts of seven apps (one with a disabled Fieldset, a pointer pad and
// a virtual list). After every step both hold the same tree
// (ui.SameShapes) and the same app state, an Area holds the pointer on
// both or on neither (ui.Captured), the scene holds a node for each of the
// driver's, each written row of a list is at the list's top plus its
// index's rows less the offset the terminal reported (TestHost takes that
// viewport), and every text's words are on the screen's rows its box
// covers. A pointer step is the SGR mouse bytes of a press, a release, a
// move (a drag while an Area holds the pointer) or a wheel turn; a scroll
// moves the list's scene offset; a wheel step turns the wheel on the list. A tap is the SGR mouse bytes of a
// press and a release at the node's first cell, once it shows (a shelf
// row below its list's fold scrolls into view first), so it passes
// OpenTUI's input parser, its layout and its hit test and the driver's
// walk up to the Pressable; typing passes Backspace and the typed bytes;
// focus steps Tab through the focus order and press with Enter.
//
// It builds and runs a Go binary because the driver is Ergo source that
// calls Go (go.Foreign): no in-process check runs it, and hostagree is the
// program that does. A failing seed prints the script, the step that
// differs and the replay command.
func TestTuiHostAgreesWithTestHost(t *testing.T) {
	bin, err := build("hostagree", "./cmd/hostagree")
	if err != nil {
		t.Fatal(err)
	}
	seeds := []uint64{}
	if *agreeSeed != 0 {
		seeds = append(seeds, *agreeSeed)
	} else {
		for s := uint64(1); s <= 40; s++ {
			seeds = append(seeds, s)
		}
	}
	steps, failed := 0, 0
	for _, seed := range seeds {
		app := agreeApps[seed%uint64(len(agreeApps))]
		script := genScript(rand.New(rand.NewPCG(seed, 0x6167726565)), app)
		text, err := play(bin, script)
		var bad []string
		for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
			switch {
			case strings.HasPrefix(line, "ok "):
				steps++
			case line == "end", strings.HasPrefix(line, "cost "):
			default:
				bad = append(bad, line)
			}
		}
		if err != nil || len(bad) > 0 || !strings.HasSuffix(text, "end\n") {
			failed++
			if failed <= 3 {
				t.Errorf("seed %d (%s): %v\n%s\nscript:\n%s\nreplay: go test -run TestTuiHostAgreesWithTestHost -agree.seed=%d", seed, app, err, strings.Join(bad, "\n"), script, seed)
			}
		}
	}
	if failed > 3 {
		t.Errorf("%d more seeds differ", failed-3)
	}
	t.Logf("%d seeds, %d steps agree", len(seeds), steps)
}

// tuiExamples are the examples whose terminal binaries live in cmd/: the
// example's folder, and the terminal binary's name.
var tuiExamples = [][2]string{{"counter-gui", "counter-tui"}, {"todo-gui", "todo-tui"}}

// firstDiff is the first line where a and b differ, both sides.
func firstDiff(a, b string) string {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := range max(len(al), len(bl)) {
		var x, y string
		if i < len(al) {
			x = al[i]
		}
		if i < len(bl) {
			y = bl[i]
		}
		if x != y {
			return fmt.Sprintf("line %d:\n  %s\n  %s", i+1, x, y)
		}
	}
	return ""
}

// TestExamplesAgreeOnTheTerminal: the examples' programs, counter-gui and
// todo-gui, print the same lines (the model's summary after each step)
// for the same seeded scene script on ui.TestHost (the example's own
// binary) and through a headless screen on the terminal driver (the
// terminal binary here, -headless). Each script comes from the example's
// own generator (gen -seed S -n 120): taps of every button, typing,
// filters, and labels that are not on the screen.
//
// It builds and runs Go binaries because both are Ergo programs that run
// as binaries (the terminal one calls Go): no in-process check runs them.
// A failing seed prints the first line that differs, the script and the
// replay command.
func TestExamplesAgreeOnTheTerminal(t *testing.T) {
	seeds := []int{}
	if *examplesSeed != 0 {
		seeds = append(seeds, *examplesSeed)
	} else {
		for s := 1; s <= 20; s++ {
			seeds = append(seeds, s)
		}
	}
	for _, ex := range tuiExamples {
		own, err := build(ex[0], "../../examples/"+ex[0]+"/cmd/"+ex[0])
		if err != nil {
			t.Fatal(err)
		}
		term, err := build(ex[1], "./cmd/"+ex[1])
		if err != nil {
			t.Fatal(err)
		}
		failed, lines := 0, 0
		for _, seed := range seeds {
			script, err := play(own, "", "gen", "-seed", strconv.Itoa(seed), "-n", "120")
			if err != nil {
				t.Fatalf("%s gen -seed %d: %v\n%s", ex[0], seed, err, script)
			}
			want, err := play(own, script)
			if err != nil {
				t.Fatalf("%s on ui.TestHost, seed %d: %v\n%s", ex[0], seed, err, want)
			}
			lines += strings.Count(want, "\n")
			got, err := play(term, script, "-headless")
			if err == nil && got == want {
				continue
			}
			failed++
			if failed <= 3 {
				t.Errorf("%s seed %d: ui.TestHost and the terminal differ (%v), %s\nscript:\n%s\nreplay: go test -run TestExamplesAgreeOnTheTerminal -examples.seed=%d", ex[1], seed, err, firstDiff(want, got), script, seed)
			}
		}
		if failed > 3 {
			t.Errorf("%s: %d more seeds differ", ex[1], failed-3)
		}
		t.Logf("%s: %d seeds, %d lines agree", ex[1], len(seeds), lines)
	}
}

// TestTuiHostPrimitivesOnly holds the FFI edge to two packages: the Go
// files of the module (its root, cmd/ and any later package; not ot/, the
// generated binding, and not screen/, the Go half that reads the
// library's cell planes) import neither "C" nor "unsafe".
func TestTuiHostPrimitivesOnly(t *testing.T) {
	fset := token.NewFileSet()
	files := 0
	err := filepath.WalkDir(".", func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() {
			switch path {
			case "ot", "screen", "testdata", "ergo.bind":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		files++
		for _, imp := range f.Imports {
			if p, _ := strconv.Unquote(imp.Path.Value); p == "C" || p == "unsafe" {
				t.Errorf("%s imports %q: only ot/ and screen/ cross the FFI", fset.Position(imp.Pos()), p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files == 0 {
		t.Fatal("no Go file read: the scan read nothing")
	}
}
