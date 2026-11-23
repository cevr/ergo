package mygo

import (
	"encoding/json"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// treeEntry is one line of a -tree run: ui.TreeLine's object.
type treeEntry struct {
	Role   string    `json:"role"`
	Name   string    `json:"name"`
	States []string  `json:"states"`
	Box    []float64 `json:"box"`
}

// TestHeadlessScriptIsData is TREE-JSON's agent loop: counter-gui's window
// binary plays a seeded script written as a JSON act list (-script, read
// by ui.ParseActs) and prints the same lines as the script on standard
// input (and, with -tree, the same lines as that script on standard input
// with -tree), then, with -tree, the screen as JSON lines: every button the view
// draws, by role and name, each with a box the window laid out (nonempty,
// and no two buttons in one place); and, with -png, the window's picture,
// the window's size, with the inc button drawn where its box is (its
// middle pixel is not the window's corner pixel).
//
// It builds and runs Go binaries because the window binary is Ergo source
// that calls Go (go.Foreign): no in-process check runs it.
func TestHeadlessScriptIsData(t *testing.T) {
	bins, err := buildExamples()
	if err != nil {
		t.Fatal(err)
	}
	b := bins["counter-gui"]
	for seed := 1; seed <= 5; seed++ {
		script, err := play(b.testHost, "", "gen", "-seed", fmt.Sprint(seed), "-n", "40")
		if err != nil {
			t.Fatalf("gen -seed %d: %v\n%s", seed, err, script)
		}
		want, err := play(b.desktop, script, "-headless")
		if err != nil {
			t.Fatalf("seed %d, script on stdin: %v\n%s", seed, err, want)
		}
		acts := []string{}
		for _, line := range strings.Split(strings.TrimSpace(script), "\n") {
			verb, rest, _ := strings.Cut(line, " ")
			arg, _ := json.Marshal(rest)
			if verb == "settle" {
				arg = []byte("true")
			}
			acts = append(acts, fmt.Sprintf("{%q: %s}", verb, arg))
		}
		file := filepath.Join(t.TempDir(), "script.json")
		if err := os.WriteFile(file, []byte("["+strings.Join(acts, ",\n")+"]\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		picture := filepath.Join(t.TempDir(), "screen.png")
		got, err := play(b.desktop, "", "-headless", "-script", file, "-tree", "-png", picture)
		if err != nil {
			t.Fatalf("seed %d, -script -tree: %v\n%s", seed, err, got)
		}
		if !strings.HasPrefix(got, want) {
			t.Fatalf("seed %d: the JSON script plays otherwise than the same script on stdin: %s", seed, firstDiff(want, got))
		}
		// -tree after a script on stdin (the reference of the phone's
		// accessibility tree, phone_access_test.go) prints the same lines.
		if stdin, err := play(b.desktop, script, "-headless", "-tree"); err != nil || stdin != got {
			t.Fatalf("seed %d: -tree after the script on stdin (%v) and after -script differ: %s", seed, err, firstDiff(got, stdin))
		}
		buttons := map[string][]float64{}
		for _, line := range strings.Split(strings.TrimSpace(strings.TrimPrefix(got, want)), "\n") {
			var e treeEntry
			if err := json.Unmarshal([]byte(line), &e); err != nil {
				t.Fatalf("seed %d: a tree line is no JSON object: %v\n%s", seed, err, line)
			}
			if len(e.Box) != 4 || e.Box[2] <= 0 || e.Box[3] <= 0 {
				t.Fatalf("seed %d: %s %q has no box the window laid out: %s", seed, e.Role, e.Name, line)
			}
			if e.Role == "button" {
				buttons[e.Name] = e.Box
			}
		}
		at := map[string]string{}
		for _, name := range []string{"dec", "inc", "reset"} {
			box, ok := buttons[name]
			if !ok {
				t.Fatalf("seed %d: no button %q in the tree:\n%s", seed, name, strings.TrimPrefix(got, want))
			}
			place := fmt.Sprint(box[0], box[1])
			if other, ok := at[place]; ok {
				t.Fatalf("seed %d: buttons %q and %q share the box at %s", seed, other, name, place)
			}
			at[place] = name
		}
		pf, err := os.Open(picture)
		if err != nil {
			t.Fatalf("seed %d: -png wrote no picture: %v", seed, err)
		}
		img, err := png.Decode(pf)
		pf.Close()
		if err != nil {
			t.Fatalf("seed %d: the picture is no PNG: %v", seed, err)
		}
		if r := img.Bounds(); r.Dx() != 360 || r.Dy() != 220 {
			t.Fatalf("seed %d: the picture is %v, not the window's 360x220", seed, r)
		}
		inc := buttons["inc"]
		if img.At(int(inc[0]+inc[2]/2), int(inc[1]+inc[3]/2)) == img.At(0, 0) {
			t.Fatalf("seed %d: the picture shows nothing at the inc button's box %v", seed, inc)
		}
	}
}
