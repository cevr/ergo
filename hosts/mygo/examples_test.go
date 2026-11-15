package mygo

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

var examplesSeed = flag.Int("examples.seed", 0, "run TestExamplesAgreeOnTheDesktop on this seed only")

// guiExamples are the examples whose window binaries live in cmd/: the
// example's folder, which is also the name of its two binaries.
var guiExamples = []string{"counter-gui", "todo-gui"}

// built is the binaries of one example: its own (ui.TestHost) and its
// window binary (the desktop driver), and the MyGo-direct control if it
// has one.
type built struct{ testHost, desktop, control string }

var buildExamples = sync.OnceValues(func() (map[string]built, error) {
	ergo, err := builtErgo()
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(ergo)
	out := map[string]built{}
	for _, name := range guiExamples {
		b := built{testHost: filepath.Join(dir, name+"-testhost"), desktop: filepath.Join(dir, name+"-desktop")}
		if o, err := bounded(ergo, "build", "../../examples/"+name+"/cmd/"+name, "-o", b.testHost).CombinedOutput(); err != nil {
			return nil, fmt.Errorf("ergo build examples/%s: %v\n%s", name, err, o)
		}
		if o, err := bounded(ergo, "build", "./cmd/"+name, "-o", b.desktop).CombinedOutput(); err != nil {
			return nil, fmt.Errorf("ergo build cmd/%s: %v\n%s", name, err, o)
		}
		if _, err := os.Stat(filepath.Join("controls", name)); err == nil {
			b.control = filepath.Join(dir, name+"-control")
			if o, err := bounded("go", "build", "-o", b.control, "./controls/"+name).CombinedOutput(); err != nil {
				return nil, fmt.Errorf("go build controls/%s: %v\n%s", name, err, o)
			}
		}
		out[name] = b
	}
	return out, nil
})

// play runs bin on the script and is its output, with its error.
func play(bin, script string, args ...string) (string, error) {
	cmd := bounded(bin, args...)
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

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

// TestExamplesCrossBuild builds the window binaries for the Mac from this
// host with CGO_ENABLED=0 (ergo build --platform darwin/arm64); the Linux
// build is TestExamplesAgreeOnTheDesktop's. Windows is not an ergo build
// platform (interop.md, Platforms: runtime/v1 starts processes with
// Setpgid and Kill), so no Windows binary is claimed.
//
// It builds Go binaries because the claim is that the Go toolchain links
// them for another platform; no in-process check links.
func TestExamplesCrossBuild(t *testing.T) {
	ergo, err := builtErgo()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range guiExamples {
		bin := filepath.Join(filepath.Dir(ergo), name+"-darwin-arm64")
		if o, err := bounded(ergo, "build", "./cmd/"+name, "--platform", "darwin/arm64", "-o", bin).CombinedOutput(); err != nil {
			t.Errorf("ergo build cmd/%s --platform darwin/arm64: %v\n%s", name, err, o)
		}
	}
}

// TestExamplesAgreeOnTheDesktop is X-gui's agreement: the examples'
// programs, counter-gui and todo-gui, print the same lines (the model's
// summary after each step) for the same seeded scene script on
// ui.TestHost (the example's own binary), through a headless MyGo window
// on the desktop driver (its window binary here, -headless), and, for
// todo-gui, on the MyGo-direct control (controls/todo-gui, MyGo's
// widgets driven by ui.Tester). Each script comes from the example's own
// generator (gen -seed S -n 120): taps of every button, typing, filters,
// and labels that are not on the screen; a todo script fills the list
// past its scroll's fold, so a step must scroll to its row first.
//
// It builds and runs Go binaries because the window binary is Ergo source
// that calls Go (go.Foreign) and the control is Go: no in-process check
// runs either. A failing seed prints the first line that differs, the
// script and the replay command.
func TestExamplesAgreeOnTheDesktop(t *testing.T) {
	bins, err := buildExamples()
	if err != nil {
		t.Fatal(err)
	}
	seeds := []int{}
	if *examplesSeed != 0 {
		seeds = append(seeds, *examplesSeed)
	} else {
		for s := 1; s <= 20; s++ {
			seeds = append(seeds, s)
		}
	}
	for _, name := range guiExamples {
		b := bins[name]
		failed, lines := 0, 0
		for _, seed := range seeds {
			script, err := play(b.testHost, "", "gen", "-seed", fmt.Sprint(seed), "-n", "120")
			if err != nil {
				t.Fatalf("%s gen -seed %d: %v\n%s", name, seed, err, script)
			}
			want, err := play(b.testHost, script)
			if err != nil {
				t.Fatalf("%s on ui.TestHost, seed %d: %v\n%s", name, seed, err, want)
			}
			lines += strings.Count(want, "\n")
			legs := [][2]string{{"desktop", b.desktop}}
			if b.control != "" {
				legs = append(legs, [2]string{"control", b.control})
			}
			for _, leg := range legs {
				got, err := play(leg[1], script, "-headless")
				if err == nil && got == want {
					continue
				}
				failed++
				if failed <= 3 {
					t.Errorf("%s seed %d: ui.TestHost and the %s differ (%v), %s\nscript:\n%s\nreplay: go test -run TestExamplesAgreeOnTheDesktop -examples.seed=%d", name, seed, leg[0], err, firstDiff(want, got), script, seed)
				}
			}
		}
		if failed > 3 {
			t.Errorf("%s: %d more runs differ", name, failed-3)
		}
		t.Logf("%s: %d seeds, %d lines agree", name, len(seeds), lines)
	}
}
