package main

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestEditorGuiAgreesWithControl is the agreement property of
// examples/editor-gui (corpus wave GP2a): on seeded texts and scripts, the
// Ergo program and the hand Go control (testdata/editor-gui/control)
// print exactly what a plain reference model prints (editorReference: the
// text as one string, undo and redo as snapshots of the whole text, lines
// found by splitting), stdout, stderr and exit status, and each leaves
// the saved file holding the reference's text. The inputs reach the
// edges: an empty or missing file, texts with runes of one to four bytes,
// empty lines and no final line break, invalid UTF-8 (refused with its
// offset), moves past both ends and across lines of other lengths,
// selections in both directions, searches with no match, replaces whose
// text holds the query, long undo and redo chains, and lines that are no
// command. On every fifth seed (and on a replayed one) the Ergo program
// also plays the script as a scene (GP2b): the window's view on
// ui.TestHost, driven by keys, buttons, its text input and its
// replacement field, with saves the view asks the host for; it is held
// to the reference of the script as a window plays it
// (editorSceneAsPlay), and so is the file it saves. A failing seed prints
// its replay command and the first line that differs.
//
// It builds Go because the programs under test are the linked binaries:
// the Ergo program reads and writes the host's file and reads its
// standard input, and the control is Go; no in-process check runs either.
// Each run is bounded by proclimit, and the test waits for its exit.
//
// It kills the example's mutants that no law sees (examples/editor-gui/
// testdata/mutants.json): see the ledger's cmd/ergo killers.
func TestEditorGuiAgreesWithControl(t *testing.T) {
	needsBuild(t)
	ergoBin, work := buildCorpusProgram(t, "editor-gui")
	control := buildCorpusControl(t, work, "editor-gui")
	seeds := 150
	first := uint64(1)
	if s := os.Getenv("EDITOR_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("EDITOR_SEED=%q is not a seed", s)
		}
		first, seeds = n, 1
	}
	for seed := first; seed < first+uint64(seeds); seed++ {
		r := rand.New(rand.NewPCG(seed, 0xed17))
		text, exists := editorText(r)
		script := editorScript(r)
		type leg struct{ bin, command, script string }
		legs := []leg{{ergoBin, "edit", script}, {control, "edit", script}}
		if seeds == 1 || seed%5 == 0 {
			// A scene costs a pass a key, and a move of N is N keys:
			// every fifth seed plays one.
			legs = append(legs, leg{ergoBin, "scene", editorSceneAsPlay(script)})
		}
		for _, l := range legs {
			bin := l.bin
			want, saved := editorReference(text, l.script)
			dir := t.TempDir()
			path := filepath.Join(dir, "doc.txt")
			if exists {
				if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			got := runCorpus(t, bin, dir, []string{l.command, "doc.txt"}, script)
			if got != want {
				t.Fatalf("seed %d: %s %s printed other text than the reference\n%s\nreplay: EDITOR_SEED=%d go test -count=1 -run '^TestEditorGuiAgreesWithControl$' ./cmd/ergo",
					seed, filepath.Base(bin), l.command, firstDiff(got, want), seed)
			}
			file, err := os.ReadFile(path)
			if saved == nil {
				if exists && string(file) != text || !exists && err == nil {
					t.Fatalf("seed %d: %s changed doc.txt with no save\nreplay: EDITOR_SEED=%d go test -count=1 -run '^TestEditorGuiAgreesWithControl$' ./cmd/ergo", seed, filepath.Base(bin), seed)
				}
			} else if string(file) != *saved {
				t.Fatalf("seed %d: %s saved %q, the reference %q\nreplay: EDITOR_SEED=%d go test -count=1 -run '^TestEditorGuiAgreesWithControl$' ./cmd/ergo", seed, filepath.Base(bin), file, *saved, seed)
			}
		}
	}
}

var (
	editorWords = []string{"a", "the", "río", "naïve", "日本", "λόγος", "𝄞", "ab", "aa", "", " ", "x"}
	editorTyped = []string{"a", "é", "日", "𝄞", "aa", "ab", " ", "x y", "\t", "ba"}
	editorFind  = []string{"a", "aa", "ab", "é", "日本", "𝄞", "the", "zz", "\xff", "\xce", " "}
)

// editorSceneAsPlay is script as the scene plays it, written for the
// reference: a window moves by one rune or line a key, so a move of N is
// N moves of 1 (for up and down that differs: each keeps the column of
// the line it leaves, where up N keeps the column it started from). Every
// other line, a malformed count included, stays as it is.
func editorSceneAsPlay(script string) string {
	var b strings.Builder
	for _, line := range strings.SplitAfter(script, "\n") {
		word, rest, _ := strings.Cut(strings.TrimSuffix(line, "\n"), " ")
		n, err := strconv.Atoi(rest)
		if (word == "left" || word == "right" || word == "up" || word == "down") && err == nil && n >= 0 && n <= 1000000 && rest == strconv.Itoa(n) {
			b.WriteString(strings.Repeat(word+" 1\n", n))
			continue
		}
		b.WriteString(line)
	}
	return b.String()
}

// editorText is a seed's file: absent, empty, or lines of words, now and
// then without a final line break, with blank lines, or invalid.
func editorText(r *rand.Rand) (string, bool) {
	if r.IntN(12) == 0 {
		return "", false
	}
	sizes := []int{0, 1, 2, 5, 12, 40}
	n := sizes[r.IntN(len(sizes))]
	var b strings.Builder
	for i := 0; i < n; i++ {
		for w := r.IntN(6); w > 0; w-- {
			b.WriteString(editorWords[r.IntN(len(editorWords))])
		}
		if i < n-1 || r.IntN(3) > 0 {
			b.WriteByte('\n')
		}
	}
	text := b.String()
	if r.IntN(15) == 0 {
		at := r.IntN(len(text) + 1)
		text = text[:at] + []string{"\xff", "\xce", "\xe6\x97"}[r.IntN(3)] + text[at:]
	}
	return text, true
}

// editorScript is a seed's script.
func editorScript(r *rand.Rand) string {
	var b strings.Builder
	for i := r.IntN(120); i > 0; i-- {
		switch k := r.IntN(40); {
		case k < 6:
			b.WriteString("show\n")
		case k < 12:
			b.WriteString("type " + editorTyped[r.IntN(len(editorTyped))] + "\n")
		case k < 13:
			b.WriteString("enter\n")
		case k < 15:
			b.WriteString([]string{"back", "del"}[r.IntN(2)] + "\n")
		case k < 21:
			fmt.Fprintf(&b, "%s %d\n", []string{"left", "right", "up", "down"}[r.IntN(4)], []int{0, 1, 1, 2, 3, 7, 100}[r.IntN(7)])
		case k < 23:
			b.WriteString([]string{"home", "end", "top", "bottom"}[r.IntN(4)] + "\n")
		case k < 25:
			b.WriteString([]string{"select", "escape"}[r.IntN(2)] + "\n")
		case k < 27:
			b.WriteString("find\n")
			b.WriteString("type " + editorFind[r.IntN(len(editorFind))] + "\n")
		case k < 29:
			b.WriteString([]string{"next", "back"}[r.IntN(2)] + "\n")
		case k < 30:
			b.WriteString("replace " + []string{"", "b", "aab", "日本日本", "Ω"}[r.IntN(5)] + "\n")
		case k < 35:
			b.WriteString([]string{"undo", "redo", "undo", "undo", "redo"}[r.IntN(5)] + "\n")
		case k < 36:
			b.WriteString("save\n")
		default:
			b.WriteString([]string{"", "# a note", "fly", "left x", "left -1", "show me", "type", "up 1000001", "find now", "left +2"}[r.IntN(10)] + "\n")
		}
	}
	b.WriteString("show\n")
	return b.String()
}

// editorSnap is the reference's undo entry: the whole text and caret
// before and after a change.
type editorSnap struct {
	before, after           string
	caretBefore, caretAfter int
}

// editorReference is what editor-gui prints for script over the file
// text, and the text the last save wrote (nil when none). It is a plain
// model: the text is one string, an undo restores a snapshot, and lines
// are found by splitting the text.
func editorReference(text string, script string) (string, *string) {
	if !utf8.ValidString(text) {
		bad := 0
		for bad < len(text) {
			r, s := utf8.DecodeRuneInString(text[bad:])
			if r == utf8.RuneError && s == 1 {
				break
			}
			bad += s
		}
		return corpusOutput("", fmt.Sprintf("editor-gui: doc.txt: not UTF-8 text: an invalid byte at offset %d\n", bad), 1), nil
	}
	var out strings.Builder
	var saved *string
	caret, dirty := 0, false
	var undo, redo []editorSnap
	mode, anchor, query, origin := "editing", 0, "", 0
	change := func(next string, after int) {
		undo = append(undo, editorSnap{text, next, caret, after})
		redo = nil
		text, caret, dirty = next, after, true
	}
	lineOf := func(at int) int { return strings.Count(text[:at], "\n") + 1 }
	lines := func() []string { return strings.Split(text, "\n") }
	lineStart := func(n int) int {
		ls := lines()
		at := 0
		for i := 0; i < n-1 && i < len(ls); i++ {
			at += len(ls[i]) + 1
		}
		return min(at, len(text))
	}
	lineEnd := func(n int) int {
		ls := lines()
		n = min(max(n, 1), len(ls))
		return lineStart(n) + len(ls[n-1])
	}
	column := func(at int) int { return utf8.RuneCountInString(text[lineStart(lineOf(at)):at]) }
	toLine := func(n int) int {
		n = min(max(n, 1), len(lines()))
		start, end := lineStart(n), lineEnd(n)
		at, col := start, column(caret)
		for i := 0; i < col && at < end; i++ {
			_, s := utf8.DecodeRuneInString(text[at:end])
			at += s
		}
		return at
	}
	before := func(at int) int {
		if at == 0 {
			return 0
		}
		_, s := utf8.DecodeLastRuneInString(text[:at])
		return s
	}
	after := func(at int) int {
		if at == len(text) {
			return 0
		}
		_, s := utf8.DecodeRuneInString(text[at:])
		return s
	}
	move := func(word string, n int) (int, bool) {
		switch word {
		case "left":
			at := caret
			for i := 0; i < n && at > 0; i++ {
				at -= before(at)
			}
			return at, true
		case "right":
			at := caret
			for i := 0; i < n && at < len(text); i++ {
				at += after(at)
			}
			return at, true
		case "up":
			return toLine(lineOf(caret) - n), true
		case "down":
			return toLine(lineOf(caret) + n), true
		case "home":
			return lineStart(lineOf(caret)), true
		case "end":
			return lineEnd(lineOf(caret)), true
		case "top":
			return 0, true
		case "bottom":
			return len(text), true
		}
		return 0, false
	}
	find := func(q string, from int) int {
		if q == "" {
			return from
		}
		at := min(from, len(text))
		if i := strings.Index(text[at:], q); i >= 0 {
			return at + i
		}
		if j := strings.Index(text, q); j >= 0 {
			return j
		}
		return from
	}
	count := func(q string) int {
		if q == "" {
			return 0
		}
		return strings.Count(text, q)
	}
	bare := "show enter back del home end top bottom select find next undo redo escape save"
	for _, line := range strings.Split(strings.TrimSuffix(script, "\n"), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		word, rest, _ := strings.Cut(line, " ")
		n := 0
		switch word {
		case "type", "replace":
		case "left", "right", "up", "down":
			v, err := strconv.Atoi(rest)
			if err != nil || v < 0 || v > 1000000 {
				fmt.Fprintf(&out, "? \"%s\": want a count\n", line)
				continue
			}
			n = v
		default:
			if !strings.Contains(" "+bare+" ", " "+word+" ") {
				fmt.Fprintf(&out, "? \"%s\": want a command: %s type replace left right up down\n", line, bare)
				continue
			}
			if rest != "" {
				fmt.Fprintf(&out, "? \"%s\": want no argument\n", line)
				continue
			}
		}
		if word == "enter" {
			word, rest = "type", "\n"
		}
		if word == "show" {
			at := lineOf(caret)
			d := map[bool]string{true: "yes", false: "no"}[dirty]
			fmt.Fprintf(&out, "%s line=%d col=%d caret=%d len=%d undo=%d redo=%d dirty=%s", mode, at, column(caret), caret, len(text), len(undo), len(redo), d)
			switch mode {
			case "selecting":
				fmt.Fprintf(&out, " anchor=%d", anchor)
			case "searching":
				fmt.Fprintf(&out, " find=\"%s\" matches=%d", query, count(query))
			}
			out.WriteByte('\n')
			ls := lines()
			for k := max(at-2, 1); k <= min(at+2, len(ls)); k++ {
				mark := map[bool]string{true: ">", false: " "}[k == at]
				fmt.Fprintf(&out, "%s%d|%s\n", mark, k, ls[k-1])
			}
			continue
		}
		switch mode {
		case "editing":
			switch word {
			case "type":
				if rest != "" {
					change(text[:caret]+rest+text[caret:], caret+len(rest))
				}
			case "back":
				if s := before(caret); s > 0 {
					change(text[:caret-s]+text[caret:], caret-s)
				}
			case "del":
				if s := after(caret); s > 0 {
					change(text[:caret]+text[caret+s:], caret)
				}
			case "select":
				mode, anchor = "selecting", caret
			case "find":
				mode, query, origin = "searching", "", caret
			case "undo":
				if len(undo) > 0 {
					u := undo[len(undo)-1]
					undo, redo = undo[:len(undo)-1], append(redo, u)
					text, caret, dirty = u.before, u.caretBefore, true
				}
			case "redo":
				if len(redo) > 0 {
					u := redo[len(redo)-1]
					redo, undo = redo[:len(redo)-1], append(undo, u)
					text, caret, dirty = u.after, u.caretAfter, true
				}
			case "save":
				s := text
				saved, dirty = &s, false
				fmt.Fprintf(&out, "saved doc.txt (%d bytes)\n", len(text))
			default:
				if at, ok := move(word, n); ok {
					caret = at
				}
			}
		case "selecting":
			lo, hi := min(anchor, caret), max(anchor, caret)
			switch word {
			case "type":
				if rest != "" {
					change(text[:lo]+rest+text[hi:], lo+len(rest))
					mode = "editing"
				}
			case "back", "del":
				if lo < hi {
					change(text[:lo]+text[hi:], lo)
				}
				mode = "editing"
			case "escape":
				mode = "editing"
			default:
				if at, ok := move(word, n); ok {
					caret = at
				}
			}
		case "searching":
			switch word {
			case "type":
				query += rest
				caret = find(query, origin)
			case "back":
				if query != "" {
					_, s := utf8.DecodeLastRuneInString(query)
					query = query[:len(query)-s]
					caret = find(query, origin)
				}
			case "next":
				if query != "" && strings.Contains(text, query) {
					caret = find(query, caret+1)
				}
			case "replace":
				if count(query) > 0 {
					first := strings.Index(text, query)
					change(strings.ReplaceAll(text, query, rest), first)
					mode = "editing"
				}
			case "escape":
				caret, mode = origin, "editing"
			case "find":
				mode = "editing"
			}
		}
	}
	return corpusOutput(out.String(), "", 0), saved
}
