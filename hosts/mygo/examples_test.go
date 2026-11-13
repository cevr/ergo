package mygo

import (
	"debug/pe"
	"flag"
	"fmt"
	"os"
	"os/exec"
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

// TestExamplesCrossBuild builds the window binaries for the Mac, Windows
// and the browser from this host with CGO_ENABLED=0 (ergo build
// --platform darwin/arm64, windows/amd64, js/wasm); the Linux build is
// TestExamplesAgreeOnTheDesktop's. The Windows file is a PE amd64
// executable. The browser binary also runs, headless, under Node with
// Go's wasm_exec_node.js: for seeds 1 to 3 of its example's script it
// prints what ui.TestHost prints (W1's smoke; the window binary's MyGo
// binding states js/wasm, interop.md, Platforms). With no node on PATH
// that run is skipped, by name.
//
// It builds Go binaries because the claim is that the Go toolchain links
// them for another platform, and runs the browser one because its
// runtime (runtime/v1's files for js) is what is claimed; no in-process
// check links or runs. Node runs under bounded's memory limit.
func TestExamplesCrossBuild(t *testing.T) {
	ergo, err := builtErgo()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(ergo)
	for _, name := range guiExamples {
		for _, platform := range []string{"darwin/arm64", "windows/amd64", "js/wasm"} {
			bin := filepath.Join(dir, name+"-"+strings.ReplaceAll(platform, "/", "-"))
			if o, err := bounded(ergo, "build", "./cmd/"+name, "--platform", platform, "-o", bin).CombinedOutput(); err != nil {
				t.Errorf("ergo build cmd/%s --platform %s: %v\n%s", name, platform, err, o)
			}
		}
		if f, err := pe.Open(filepath.Join(dir, name+"-windows-amd64")); err != nil {
			t.Errorf("%s for windows/amd64: %v", name, err)
		} else {
			if f.Machine != pe.IMAGE_FILE_MACHINE_AMD64 {
				t.Errorf("%s for windows/amd64: machine %#x", name, f.Machine)
			}
			f.Close()
		}
	}
	if t.Failed() {
		return
	}
	t.Run("browser-run", func(t *testing.T) {
		node, err := exec.LookPath("node")
		if err != nil {
			t.Skip("no node on PATH: the js/wasm binaries are built, not run")
		}
		goroot, err := exec.Command("go", "env", "GOROOT").Output()
		if err != nil {
			t.Fatal(err)
		}
		runner := filepath.Join(strings.TrimSpace(string(goroot)), "lib", "wasm", "wasm_exec_node.js")
		bins, err := buildExamples()
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range guiExamples {
			wasm := filepath.Join(dir, name+"-js-wasm")
			for seed := 1; seed <= 3; seed++ {
				script, err := play(bins[name].testHost, "", "gen", "-seed", fmt.Sprint(seed), "-n", "120")
				if err != nil {
					t.Fatalf("%s gen -seed %d: %v\n%s", name, seed, err, script)
				}
				want, err := play(bins[name].testHost, script)
				if err != nil {
					t.Fatalf("%s on ui.TestHost, seed %d: %v\n%s", name, seed, err, want)
				}
				got, err := play(node, script, runner, wasm, "-headless")
				if err != nil || got != want {
					t.Errorf("%s on js/wasm, seed %d (err %v): %s\nreplay: go test -run TestExamplesCrossBuild ./hosts/mygo; node %s %s -headless < (%s gen -seed %d -n 120)", name, seed, err, firstDiff(want, got), runner, wasm, bins[name].testHost, seed)
				}
			}
		}
	})
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
