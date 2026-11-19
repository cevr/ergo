package mygo

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

var editorSeed = flag.Int("editor.seed", 0, "run TestEditorGuiOnTheDesktop on this seed only")

// The words of an editor text, what a script types and what it finds (as
// cmd/ergo's TestEditorGuiAgreesWithControl draws them, less the invalid
// UTF-8: a window's text input carries text, not bytes).
var (
	editorWords = []string{"a", "the", "río", "naïve", "日本", "λόγος", "𝄞", "ab", "aa", "", " ", "x"}
	editorTyped = []string{"a", "é", "日", "𝄞", "aa", "ab", " ", "x y", "\t", "ba"}
	editorFind  = []string{"a", "aa", "ab", "é", "日本", "𝄞", "the", "zz", " "}
)

// editorText is a seed's document: none, empty, or lines of words, now
// and then without a final line break or with blank lines.
func editorText(r *rand.Rand) (string, bool) {
	if r.IntN(12) == 0 {
		return "", false
	}
	n := []int{0, 1, 2, 5, 12, 40}[r.IntN(6)]
	var b strings.Builder
	for i := 0; i < n; i++ {
		for w := r.IntN(6); w > 0; w-- {
			b.WriteString(pick(r, editorWords...))
		}
		if i < n-1 || r.IntN(3) > 0 {
			b.WriteByte('\n')
		}
	}
	return b.String(), true
}

// editorScript is a seed's script of the example's commands: typing,
// line breaks, erasing, moves of up to 7 (each a key press, so a pass of
// the window), the ends, selections, searches, replaces, undo and redo,
// saves, copies, cuts and pastes, clicks on the page's rows and columns,
// and lines that are no command.
func editorScript(r *rand.Rand) string {
	var b strings.Builder
	for i := 10 + r.IntN(70); i > 0; i-- {
		switch k := r.IntN(46); {
		case k >= 44:
			b.WriteString("paste\n")
		case k >= 40:
			if k < 42 {
				fmt.Fprintf(&b, "click %d %d\n", r.IntN(7), pick(r, 0, 1, 3, 7, 40, 1000))
			} else {
				b.WriteString(pick(r, "copy", "cut") + "\n")
			}
		case k < 6:
			b.WriteString("show\n")
		case k < 12:
			b.WriteString("type " + pick(r, editorTyped...) + "\n")
		case k < 13:
			b.WriteString("enter\n")
		case k < 15:
			b.WriteString(pick(r, "back", "del") + "\n")
		case k < 21:
			fmt.Fprintf(&b, "%s %d\n", pick(r, "left", "right", "up", "down"), pick(r, 0, 1, 1, 2, 3, 7))
		case k < 23:
			b.WriteString(pick(r, "home", "end", "top", "bottom") + "\n")
		case k < 25:
			b.WriteString(pick(r, "select", "escape") + "\n")
		case k < 27:
			b.WriteString("find\ntype " + pick(r, editorFind...) + "\n")
		case k < 29:
			b.WriteString(pick(r, "next", "back") + "\n")
		case k < 30:
			b.WriteString("replace " + pick(r, "", "b", "aab", "日本日本", "Ω") + "\n")
		case k < 35:
			b.WriteString(pick(r, "undo", "redo", "undo", "undo", "redo") + "\n")
		case k < 36:
			b.WriteString("save\n")
		default:
			b.WriteString(pick(r, "", "# a note", "fly", "left x", "left -1", "show me", "type", "find now", "click 1", "click x 2", "paste now") + "\n")
		}
	}
	b.WriteString("show\n")
	return b.String()
}

// buildEditor builds examples/editor-gui's own binary (its scene runs the
// view on ui.TestHost), its window binary (cmd/editor-gui) and the
// MyGo-direct control (controls/editor-gui).
var buildEditor = sync.OnceValues(func() ([3]string, error) {
	ergo, err := builtErgo()
	if err != nil {
		return [3]string{}, err
	}
	dir := filepath.Dir(ergo)
	bins := [3]string{filepath.Join(dir, "editor-testhost"), filepath.Join(dir, "editor-desktop"), filepath.Join(dir, "editor-control")}
	if o, err := bounded(ergo, "build", "../../examples/editor-gui/cmd/editor-gui", "-o", bins[0]).CombinedOutput(); err != nil {
		return bins, fmt.Errorf("ergo build examples/editor-gui: %v\n%s", err, o)
	}
	if o, err := bounded(ergo, "build", "./cmd/editor-gui", "-o", bins[1]).CombinedOutput(); err != nil {
		return bins, fmt.Errorf("ergo build cmd/editor-gui: %v\n%s", err, o)
	}
	if o, err := bounded("go", "build", "-o", bins[2], "./controls/editor-gui").CombinedOutput(); err != nil {
		return bins, fmt.Errorf("go build controls/editor-gui: %v\n%s", err, o)
	}
	return bins, nil
})

// editorRun runs bin with args in a new folder that holds doc.txt when
// exists, on script; it is what bin printed and what doc.txt holds after
// (absent: ok false).
func editorRun(t *testing.T, bin, text string, exists bool, script string, args ...string) (out string, err error, file string, ok bool) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.txt")
	if exists {
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := bounded(bin, append(args, "doc.txt")...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(script)
	o, err := cmd.CombinedOutput()
	data, rerr := os.ReadFile(path)
	return string(o), err, string(data), rerr == nil
}

// TestEditorGuiOnTheDesktop is GP2d's agreement: for seeded documents and
// scripts, the editor's window binary (cmd/editor-gui -headless -check:
// the example's view on the desktop driver, through a headless MyGo
// window, its kept layout held to the rebuilt one) prints what the
// example's own `editor-gui scene` prints (the same view on ui.TestHost,
// which cmd/ergo's TestEditorGuiAgreesWithControl holds to its reference
// model) and leaves the same file, and so does the MyGo-direct control
// (controls/editor-gui -headless: the hand-Go model, its window on MyGo's
// widgets laid out and painted by ui.Tester after each line). The window
// binary's steps are pixels
// and keys: a key after a click beside the toolbar (no focused field or
// button takes it), shortcuts with Ctrl, a click on a button, typed text
// into the text input after a click on it, the replacement typed into its
// field, a press on the page. A failing seed prints the first line that
// differs and its replay command.
//
// It builds and runs Go binaries because the window binary is Ergo source
// that calls Go (go.Foreign): no in-process check runs it.
func TestEditorGuiOnTheDesktop(t *testing.T) {
	bins, err := buildEditor()
	if err != nil {
		t.Fatal(err)
	}
	seeds := []int{}
	if *editorSeed != 0 {
		seeds = append(seeds, *editorSeed)
	} else {
		for s := 1; s <= 60; s++ {
			seeds = append(seeds, s)
		}
	}
	failed, lines := 0, 0
	for _, seed := range seeds {
		r := rand.New(rand.NewPCG(uint64(seed), 0xed17d))
		text, exists := editorText(r)
		script := editorScript(r)
		want, werr, wantFile, wantOK := editorRun(t, bins[0], text, exists, script, "scene")
		if werr != nil {
			t.Fatalf("editor-gui scene, seed %d: %v\n%s", seed, werr, want)
		}
		lines += strings.Count(want, "\n")
		for _, leg := range []struct {
			name string
			args []string
		}{{"desktop", []string{bins[1], "-headless", "-check"}}, {"control", []string{bins[2], "-headless"}}} {
			got, err, file, ok := editorRun(t, leg.args[0], text, exists, script, leg.args[1:]...)
			var why string
			switch {
			case err != nil || got != want:
				why = fmt.Sprintf("err %v, %s", err, firstDiff(want, got))
			case ok != wantOK || file != wantFile:
				why = fmt.Sprintf("the file is %q (%v), the scene's %q (%v)", file, ok, wantFile, wantOK)
			default:
				continue
			}
			failed++
			if failed <= 3 {
				t.Errorf("seed %d on the %s: %s\nscript:\n%s\nreplay: go test -run TestEditorGuiOnTheDesktop -editor.seed=%d ./hosts/mygo", seed, leg.name, why, script, seed)
			}
		}
	}
	if failed > 3 {
		t.Errorf("%d more seeds differ", failed-3)
	}
	t.Logf("%d seeds, %d lines agree on both legs", len(seeds), lines)
}
