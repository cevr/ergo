package main

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestStarsGuiAgreesWithControl is the agreement property of
// examples/stars-gui (corpus wave GP1a): on seeded catalogues and scripts,
// the Ergo program and the hand Go control (testdata/stars-gui/control)
// print exactly what a plain reference model prints (starsReference: the
// view filtered and sorted again from the whole catalogue after every
// change, with its own regular-expression reader of cells), stdout,
// stderr and exit status. The inputs reach the edges: an empty catalogue
// and one of 2,000 stars, ties in every column, names that case-fold
// beyond ASCII and filters with invalid UTF-8 (so a typed letter both
// narrows and sorts again), windows of 1 to 12 rows, moves past both
// ends, selections across sorts, edits that each column takes and
// refuses, malformed catalogues and lines that are no command. On every
// fifth seed (and on a replayed one) the Ergo program also plays the
// script as a scene (GP1b): the window's view on ui.TestHost, driven by
// keys, buttons and its text field, with the rows of a virtual list;
// what it shows is held to the same reference. A failing seed prints its
// replay command and the first line that differs.
//
// It builds Go because the programs under test are the linked binaries:
// the Ergo program reads the host's file and its standard input, and the
// control is Go; no in-process check runs either. Each run is bounded by
// proclimit, and the test waits for its exit.
//
// It kills the example's mutants that no law sees (examples/stars-gui/
// testdata/mutants.json): a cursor mark on the window's top row, an edit
// that leaves the cursor where it was, a filter's Cancel that clears the
// filter it should restore, a selection that stops short of the cursor,
// and a planets column that takes four-digit counts.
func TestStarsGuiAgreesWithControl(t *testing.T) {
	needsBuild(t)
	ergoBin, work := buildCorpusProgram(t, "stars-gui")
	control := buildCorpusControl(t, work, "stars-gui")
	dir := t.TempDir()
	seeds := 150
	first := uint64(1)
	if s := os.Getenv("STARS_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("STARS_SEED=%q is not a seed", s)
		}
		first, seeds = n, 1
	}
	for seed := first; seed < first+uint64(seeds); seed++ {
		r := rand.New(rand.NewPCG(seed, 0x57a25))
		catalogue := starsCatalogue(r)
		script := starsScript(r)
		height := 1 + r.IntN(12)
		if err := os.WriteFile(filepath.Join(dir, "cat.tsv"), []byte(catalogue), 0o644); err != nil {
			t.Fatal(err)
		}
		args := []string{"run", "-rows", strconv.Itoa(height), "cat.tsv"}
		want := starsReference(catalogue, script, height)
		runs := []struct {
			bin  string
			args []string
		}{{ergoBin, args}, {control, args}}
		if seeds == 1 || seed%5 == 0 {
			// A scene costs a pass of the window a key press, so a
			// "down 3000" is 3,000 passes: every fifth seed plays one.
			runs = append(runs, struct {
				bin  string
				args []string
			}{ergoBin, append([]string{"scene"}, args[1:]...)})
		}
		for _, run := range runs {
			got := runCorpus(t, run.bin, dir, run.args, script)
			if got != want {
				t.Fatalf("seed %d (%d rows high): %s %s printed other text than the reference\n%s\nreplay: STARS_SEED=%d go test -count=1 -run '^TestStarsGuiAgreesWithControl$' ./cmd/ergo",
					seed, height, filepath.Base(run.bin), run.args[0], firstDiff(got, want), seed)
			}
		}
	}
}

var (
	starsNames  = []string{"Vega", "vega", "Ωmega", "ωmega", "Ärm", "ärm", "Orion 2", "Lyra", "İzar", "x", "Deneb", "deneb b", "Altair", "Sirius", "Mira", "a"}
	starsConsts = []string{"Lyra", "Orion", "Cygnus", "Ωrion", "Aquila", "Leo", "Ursa Major"}
	starsTyped  = []string{"o", "r", "a", "Ω", "ω", "ä", "İ", "i", " ", "\xff", "\xce", "2", "ve", "LY", "z"}
	starsDrafts = []string{"1.5", "-0.25", "30.01", "-30", "abc", "05h30m", "24h00m", "7", "999", "1000", "12345", "", " ", "x\ty", "0.0", "-", "1.", ".5", "Nova", "Ωmega", "8000000.0", "99999999.9", "90.1", "-90.0"}
	starsCols   = []string{"name", "constellation", "magnitude", "distance", "class", "ascension", "declination", "planets"}
)

// starsCatalogue is a seed's catalogue text: valid lines, with a
// malformed line now and then.
func starsCatalogue(r *rand.Rand) string {
	sizes := []int{0, 1, 2, 5, 30, 200, 2000}
	n := sizes[r.IntN(len(sizes))]
	var b strings.Builder
	for i := 0; i < n; i++ {
		mag := []int{-146, 0, 50, 50, 1200, -3000, 3000}[r.IntN(7)]
		if r.IntN(2) == 0 {
			mag = r.IntN(6001) - 3000
		}
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%s\t%02dh%02dm\t%s\t%d\n",
			starsNames[r.IntN(len(starsNames))], starsConsts[r.IntN(len(starsConsts))],
			refFixed(mag, 2), refFixed(r.IntN(3)*r.IntN(100000), 1), []string{"G2V", "A0V", "M1I", "K5III"}[r.IntN(4)],
			r.IntN(24), r.IntN(60), refFixed(r.IntN(1801)-900, 1), r.IntN(3)*r.IntN(400))
	}
	if n > 0 && r.IntN(10) == 0 {
		bad := []string{"Only\tthree\tcells", "", "Name\tLyra\t1.234\t1.0\tA\t00h00m\t0.0\t0", "Name\tLyra\t1.00\t1.0\tA\t25h00m\t0.0\t0", "Name\tLyra\t1.00\t-1.0\tA\t00h00m\t0.0\t0", " \tLyra\t1.00\t1.0\tA\t00h00m\t0.0\t0", "Name\tLyra\t1.00\t1.0\tA\t00h00m\t0.0\t1000", "Name\tLyra\t1.00\t1.0\tA\t00h00m\t0.0\t7\r"}
		b.WriteString(bad[r.IntN(len(bad))] + "\n")
	}
	return b.String()
}

// starsScript is a seed's script.
func starsScript(r *rand.Rand) string {
	var b strings.Builder
	steps := r.IntN(160)
	for i := 0; i < steps; i++ {
		switch k := r.IntN(30); {
		case k < 5:
			b.WriteString("show\n")
		case k < 8:
			fmt.Fprintf(&b, "%s %d\n", []string{"down", "up"}[r.IntN(2)], []int{0, 1, 2, 7, 50, 3000}[r.IntN(6)])
		case k < 10:
			b.WriteString([]string{"pagedown", "pageup", "home", "end"}[r.IntN(4)] + "\n")
		case k < 12:
			b.WriteString("sort " + starsCols[r.IntN(len(starsCols))] + "\n")
		case k < 14:
			b.WriteString("filter\n")
		case k < 18:
			if r.IntN(2) == 0 {
				b.WriteString("type " + starsTyped[r.IntN(len(starsTyped))] + "\n")
			} else {
				b.WriteString("type " + starsDrafts[r.IntN(len(starsDrafts))] + "\n")
			}
		case k < 20:
			b.WriteString("backspace\n")
		case k < 22:
			b.WriteString([]string{"enter", "escape"}[r.IntN(2)] + "\n")
		case k < 25:
			b.WriteString([]string{"mark", "extend"}[r.IntN(2)] + "\n")
		case k < 28:
			b.WriteString("edit " + starsCols[r.IntN(len(starsCols))] + "\n")
		default:
			b.WriteString([]string{"", "# a note", "fly", "sort up", "down x", "down -1", "show me", "edit", "type"}[r.IntN(9)] + "\n")
		}
	}
	b.WriteString("show\n")
	return b.String()
}

func refFixed(v, places int) string {
	sign := ""
	if v < 0 {
		sign, v = "-", -v
	}
	scale := map[int]int{1: 10, 2: 100}[places]
	return fmt.Sprintf("%s%d.%0*d", sign, v/scale, places, v%scale)
}

// refStar is a star as cells: texts, and numbers in the column's unit.
type refStar struct {
	text [8]string
	num  [8]int
}

var (
	refFixed1  = regexp.MustCompile(`^-?[0-9]{1,9}(\.[0-9])?$`)
	refFixed2  = regexp.MustCompile(`^-?[0-9]{1,9}(\.[0-9]{1,2})?$`)
	refHours   = regexp.MustCompile(`^[0-9]{2}h[0-9]{2}m$`)
	refPlanets = regexp.MustCompile(`^[0-9]{1,4}$`)
)

// refCell reads text as column c's cell: (ok, the number, the reason).
func refCell(c int, text string) (int, string) {
	switch c {
	case 0, 1, 4:
		if strings.TrimSpace(text) == "" {
			return 0, "a blank value"
		}
		if strings.ContainsAny(text, "\t\n\r") {
			return 0, "a tab or a line break"
		}
		return 0, ""
	case 3, 6, 2:
		re, places, lo, hi := refFixed1, 1, 0, 99999999
		if c == 2 {
			re, places, lo, hi = refFixed2, 2, -3000, 3000
		} else if c == 6 {
			lo, hi = -900, 900
		}
		if !re.MatchString(text) {
			return 0, "not a number of this column's form"
		}
		neg := strings.HasPrefix(text, "-")
		whole, frac, _ := strings.Cut(strings.TrimPrefix(text, "-"), ".")
		frac += strings.Repeat("0", places-len(frac))
		w, _ := strconv.Atoi(whole)
		f, _ := strconv.Atoi(frac)
		v := w*map[int]int{1: 10, 2: 100}[places] + f
		if neg {
			v = -v
		}
		if v < lo || v > hi {
			return 0, "out of range " + refFixed(lo, places) + " to " + refFixed(hi, places)
		}
		return v, ""
	case 5:
		if !refHours.MatchString(text) {
			return 0, "not a number of this column's form"
		}
		h, _ := strconv.Atoi(text[:2])
		m, _ := strconv.Atoi(text[3:5])
		if h > 23 || m > 59 {
			return 0, "out of range 00h00m to 23h59m"
		}
		return h*60 + m, ""
	}
	if !refPlanets.MatchString(text) {
		return 0, "not a number of this column's form"
	}
	n, _ := strconv.Atoi(text)
	if n > 999 {
		return 0, "out of range 0 to 999"
	}
	return n, ""
}

func (s refStar) cell(c int) string {
	switch c {
	case 0, 1, 4:
		return s.text[c]
	case 2:
		return refFixed(s.num[c], 2)
	case 3, 6:
		return refFixed(s.num[c], 1)
	case 5:
		return fmt.Sprintf("%02dh%02dm", s.num[c]/60, s.num[c]%60)
	}
	return strconv.Itoa(s.num[c])
}

// starsReference is what a correct stars-gui prints for the catalogue,
// the script and the window's height.
func starsReference(catalogue, script string, height int) string {
	var rows []refStar
	lines := strings.SplitAfter(catalogue, "\n")
	for i, line := range lines {
		if line == "" {
			continue
		}
		cells := strings.Split(strings.TrimSuffix(line, "\n"), "\t")
		if len(cells) != 8 {
			return corpusOutput("", fmt.Sprintf("stars-gui: cat.tsv: line %d: %d cells, want 8\n", i+1, len(cells)), 1)
		}
		var s refStar
		for c, text := range cells {
			v, why := refCell(c, text)
			if why != "" {
				return corpusOutput("", fmt.Sprintf("stars-gui: cat.tsv: line %d: %s \"%s\": %s\n", i+1, starsCols[c], text, why), 1)
			}
			s.text[c], s.num[c] = text, v
		}
		rows = append(rows, s)
	}
	height = min(max(height, 1), 1000)
	var (
		out              strings.Builder
		mode             = "browsing"
		key, desc        = 0, false
		filter, before   string
		top, cursor      int
		anchor           = -1
		selected         []int
		editRow, editCol int
		draft            string
		view             []int
	)
	order := func() {
		view = view[:0]
		f := strings.ToLower(filter)
		for id, s := range rows {
			if strings.Contains(strings.ToLower(s.text[0]), f) || strings.Contains(strings.ToLower(s.text[1]), f) {
				view = append(view, id)
			}
		}
		sort.SliceStable(view, func(i, j int) bool {
			a, b := rows[view[i]], rows[view[j]]
			c := 0
			if key == 0 || key == 1 || key == 4 {
				c = strings.Compare(a.text[key], b.text[key])
			} else if a.num[key] != b.num[key] {
				c = map[bool]int{true: -1, false: 1}[a.num[key] < b.num[key]]
			}
			if desc {
				c = -c
			}
			return c < 0
		})
	}
	settle := func() {
		cursor = min(max(cursor, 0), max(len(view)-1, 0))
		if cursor < top {
			top = cursor
		}
		if cursor >= top+height {
			top = cursor - height + 1
		}
		top = min(max(top, 0), max(len(view)-height, 0))
	}
	follow := func(id int) {
		if i := slices.Index(view, id); i >= 0 {
			cursor = i
		}
	}
	dropLast := func(s string) string {
		_, size := utf8.DecodeLastRuneInString(s)
		return s[:len(s)-size]
	}
	order()
	for _, line := range strings.SplitAfter(script, "\n") {
		line = strings.TrimSuffix(line, "\n")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		word, rest, _ := strings.Cut(line, " ")
		bad := func(want string) { fmt.Fprintf(&out, "? \"%s\": want %s\n", line, want) }
		col := slices.Index(starsCols, rest)
		switch {
		case word == "down" || word == "up":
			n, err := strconv.Atoi(rest)
			if err != nil || n < 0 || n > 1000000000 {
				bad("a count of rows")
				continue
			}
			if word == "up" {
				n = -n
			}
			if mode == "browsing" {
				cursor += n
				settle()
			}
		case word == "sort" || word == "edit":
			if col < 0 {
				bad("a column: name constellation magnitude distance class ascension declination planets")
				continue
			}
			if mode != "browsing" || (word == "edit" && len(view) == 0) {
				continue
			}
			if word == "edit" {
				mode, editRow, editCol, draft = "editing", view[cursor], col, rows[view[cursor]].cell(col)
				continue
			}
			id := -1
			if len(view) > 0 {
				id = view[cursor]
			}
			desc = col == key && !desc
			key = col
			order()
			follow(id)
			settle()
		case word == "type":
			switch mode {
			case "filtering":
				filter += rest
				order()
				top, cursor = 0, 0
				settle()
			case "editing":
				draft += rest
			}
		case !slices.Contains([]string{"show", "pagedown", "pageup", "home", "end", "filter", "backspace", "enter", "escape", "mark", "extend"}, word):
			bad("a command: show down up pagedown pageup home end sort filter type backspace enter escape mark extend edit")
		case rest != "":
			bad("no argument")
		case word == "show":
			dir := map[bool]string{false: "asc", true: "desc"}[desc]
			fmt.Fprintf(&out, "%s sort=%s %s filter=\"%s\" shown=%d/%d top=%d cursor=%d selected=%d", mode, starsCols[key], dir, filter, len(view), len(rows), top, cursor, len(selected))
			if mode == "editing" {
				fmt.Fprintf(&out, " edit=%s row=%d draft=\"%s\"", starsCols[editCol], editRow, draft)
			}
			out.WriteString("\n")
			for at := top; at < min(top+height, len(view)); at++ {
				id := view[at]
				out.WriteString(map[bool]string{true: ">", false: " "}[at == cursor] + map[bool]string{true: "*", false: " "}[slices.Contains(selected, id)] + " " + strconv.Itoa(id))
				for c := range 8 {
					out.WriteString("\t" + rows[id].cell(c))
				}
				out.WriteString("\n")
			}
		case mode != "browsing" && (word == "pagedown" || word == "pageup" || word == "home" || word == "end" || word == "filter" || word == "mark" || word == "extend"):
		case word == "pagedown" || word == "pageup":
			by := map[bool]int{true: height, false: -height}[word == "pagedown"]
			cursor, top = cursor+by, top+by
			settle()
		case word == "home":
			cursor = 0
			settle()
		case word == "end":
			cursor = len(view) - 1
			settle()
		case word == "filter":
			mode, before = "filtering", filter
		case word == "mark":
			if len(view) > 0 {
				anchor = view[cursor]
			}
		case word == "extend":
			from := slices.Index(view, anchor)
			if anchor < 0 || from < 0 {
				continue
			}
			selected = slices.Clone(view[min(from, cursor) : max(from, cursor)+1])
			slices.Sort(selected)
		case word == "backspace":
			if mode == "filtering" && filter != "" {
				filter = dropLast(filter)
				order()
				top, cursor = 0, 0
				settle()
			} else if mode == "editing" && draft != "" {
				draft = dropLast(draft)
			}
		case word == "enter" && mode == "filtering":
			mode = "browsing"
		case word == "enter" && mode == "editing":
			v, why := refCell(editCol, draft)
			if why != "" {
				fmt.Fprintf(&out, "refused %s \"%s\": %s\n", starsCols[editCol], draft, why)
				continue
			}
			rows[editRow].text[editCol], rows[editRow].num[editCol] = draft, v
			order()
			follow(editRow)
			settle()
			mode = "browsing"
		case word == "escape" && mode == "browsing":
			anchor, selected = -1, nil
		case word == "escape" && mode == "filtering":
			filter = before
			order()
			top, cursor = 0, 0
			settle()
			mode = "browsing"
		case word == "escape" && mode == "editing":
			mode = "browsing"
		}
	}
	return corpusOutput(out.String(), "", 0)
}
