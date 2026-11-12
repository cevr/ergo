package main

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TestDrawGuiAgreesWithControl is the agreement property of
// examples/draw-gui (corpus wave GP3a): on seeded pictures and scripts,
// the Ergo program and the hand Go control (testdata/draw-gui/control)
// print exactly what a plain reference model prints (drawReference:
// shapes in a slice, every hit found by testing every shape, the
// simplification done by recursion), stdout, stderr and exit status, and
// each leaves the saved file holding the reference's SVG. The inputs
// reach the edges: a missing, empty or malformed picture file, strokes of
// one point and of many, shapes of zero size, presses on edges and
// corners within the tolerance, overlapping shapes on every layer, drags
// that the world's edge stops, rubber-band selections in every
// direction, pans and zooms that put shapes off screen, and lines that
// are no command. A failing seed prints its replay command and the first
// line that differs.
//
// It builds Go because the programs under test are the linked binaries:
// the Ergo program reads and writes the host's file and reads its
// standard input, and the control is Go; no in-process check runs either.
// Each run is bounded by proclimit, and the test waits for its exit.
//
// It kills the example's mutants that no law sees (examples/draw-gui/
// testdata/mutants.json): see the ledger's cmd/ergo killers.
func TestDrawGuiAgreesWithControl(t *testing.T) {
	needsBuild(t)
	ergoBin, work := buildCorpusProgram(t, "draw-gui")
	control := buildCorpusControl(t, work, "draw-gui")
	seeds := 150
	first := uint64(1)
	if s := os.Getenv("DRAW_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("DRAW_SEED=%q is not a seed", s)
		}
		first, seeds = n, 1
	}
	for seed := first; seed < first+uint64(seeds); seed++ {
		r := rand.New(rand.NewPCG(seed, 0xd7a3))
		file, exists := drawFile(r)
		script := drawScript(r)
		want, saved := drawReference(file, exists, script)
		for _, bin := range []string{ergoBin, control} {
			dir := t.TempDir()
			path := filepath.Join(dir, "pic.svg")
			if exists {
				if err := os.WriteFile(path, []byte(file), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			got := runCorpus(t, bin, dir, []string{"play", "pic.svg"}, script)
			if got != want {
				t.Fatalf("seed %d: %s printed other text than the reference\n%s\nreplay: DRAW_SEED=%d go test -count=1 -run '^TestDrawGuiAgreesWithControl$' ./cmd/ergo",
					seed, filepath.Base(bin), firstDiff(got, want), seed)
			}
			text, err := os.ReadFile(path)
			switch {
			case saved != nil && string(text) != *saved:
				t.Fatalf("seed %d: %s saved\n%s\nthe reference\n%s\nreplay: DRAW_SEED=%d go test -count=1 -run '^TestDrawGuiAgreesWithControl$' ./cmd/ergo", seed, filepath.Base(bin), text, *saved, seed)
			case saved == nil && exists && string(text) != file, saved == nil && !exists && err == nil:
				t.Fatalf("seed %d: %s changed pic.svg with no save\nreplay: DRAW_SEED=%d go test -count=1 -run '^TestDrawGuiAgreesWithControl$' ./cmd/ergo", seed, filepath.Base(bin), seed)
			}
		}
		// Every fifth seed also plays the script as a scene on the
		// window's view (ui.TestHost): pointer lines are pointer events
		// on the picture, tool, layer, zoom and pan lines press the
		// toolbar's buttons, escape is the Escape key. Its text nodes and
		// the saved file are the reference's, but for a press of a value
		// the toolbar has no button for (drawSceneAsPlay).
		if seed%5 == 0 || seeds == 1 {
			want, saved := drawReference(file, exists, drawSceneAsPlay(script))
			dir := t.TempDir()
			path := filepath.Join(dir, "pic.svg")
			if exists {
				if err := os.WriteFile(path, []byte(file), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			got := runCorpus(t, ergoBin, dir, []string{"scene", "pic.svg"}, script)
			if got != want {
				t.Fatalf("seed %d: the scene printed other text than the reference\n%s\nreplay: DRAW_SEED=%d go test -count=1 -run '^TestDrawGuiAgreesWithControl$' ./cmd/ergo",
					seed, firstDiff(got, want), seed)
			}
			if text, _ := os.ReadFile(path); saved != nil && string(text) != *saved {
				t.Fatalf("seed %d: the scene saved\n%s\nthe reference\n%s\nreplay: DRAW_SEED=%d go test -count=1 -run '^TestDrawGuiAgreesWithControl$' ./cmd/ergo", seed, text, *saved, seed)
			}
		}
	}
}

// drawSceneAsPlay is a play script as the scene plays it: a layer, zoom
// or pan line whose numbers read, but for which the toolbar has no button
// (layers 0 to 3; zooms 25, 50, 100, 200 and 400; pans of 100 along one
// axis), becomes the scene's "? no node LABEL" (an "echo" line of
// drawReference); every other line is the same.
func drawSceneAsPlay(script string) string {
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimSuffix(script, "\n"), "\n") {
		words := strings.Split(line, " ")
		var nums []int
		for _, w := range words[1:] {
			if v, err := strconv.Atoi(w); err == nil && v >= -10000 && v <= 10000 {
				nums = append(nums, v)
			}
		}
		label := ""
		switch {
		case words[0] == "layer" && len(words) == 2 && len(nums) == 1 && (nums[0] < 0 || nums[0] > 3):
			label = fmt.Sprintf("layer %d", nums[0])
		case words[0] == "zoom" && len(words) == 2 && len(nums) == 1 && !slices.Contains([]int{25, 50, 100, 200, 400}, nums[0]):
			label = fmt.Sprintf("zoom %d", nums[0])
		case words[0] == "pan" && len(words) == 3 && len(nums) == 2 && !slices.Contains([][2]int{{-100, 0}, {100, 0}, {0, -100}, {0, 100}}, [2]int{nums[0], nums[1]}):
			label = fmt.Sprintf("pan %d %d", nums[0], nums[1])
		}
		if label != "" {
			b.WriteString("echo ? no node " + label + "\n")
		} else {
			b.WriteString(line + "\n")
		}
	}
	return b.String()
}

type drawPoint struct{ x, y int }

// drawShape is a reference shape: kind 0 stroke (pts), 1 rect (a, w, h),
// 2 ellipse (a centre, w and h radii), 3 line (a, b).
type drawShape struct {
	id, layer, kind int
	pts             []drawPoint
	a, b            drawPoint
	w, h            int
}

const drawHeader = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="-10000 -10000 20000 20000">`

// drawBadFiles are malformed pictures and the failure each reads as.
var drawBadFiles = [][2]string{
	{"hello\n", "line 1: want the svg header"},
	{drawHeader + "\n<g id=\"layer0\">\n</g>\n", "line 3: want </svg>"},
	{drawHeader + "\n<g id=\"layer1\">\n<rect data-id=\"1\" x=\"0\" y=\"0\" width=\"5\" height=\"5\"/>\n</g>\n<g id=\"layer0\">\n</g>\n</svg>\n", "line 5: want <g id=\"layerN\"> of a higher layer, 0 to 3"},
	{drawHeader + "\n<g id=\"layer0\">\n<rect data-id=\"1\" x=\"9999\" y=\"0\" width=\"5\" height=\"5\"/>\n</g>\n</svg>\n", "line 3: want a width and a height that keep the rect in the world"},
	{drawHeader + "\n<g id=\"layer0\">\n<line data-id=\"2\" x1=\"0\" y1=\"0\" x2=\"a\" y2=\"0\"/>\n</g>\n</svg>\n", "line 3: want a whole number"},
	{drawHeader + "\n<g id=\"layer2\">\n<polyline data-id=\"3\" points=\"1,1 2\"/>\n</g>\n</svg>\n", "line 3: want points X,Y"},
	{drawHeader + "\n<g id=\"layer0\">\n<circle data-id=\"3\" r=\"1\"/>\n</g>\n</svg>\n", "line 3: want polyline, rect, ellipse or line"},
	{drawHeader + "\n<g id=\"layer0\">\n<line data-id=\"2\" x1=\"0\" y1=\"0\" x2=\"1\" y2=\"0\"/>\n<line data-id=\"2\" x1=\"0\" y1=\"0\" x2=\"1\" y2=\"0\"/>\n</g>\n</svg>\n", "line 4: want a new data-id of 1 or more"},
	{drawHeader + "\n<g id=\"layer0\">\n<ellipse data-id=\"4\" cx=\"9990\" cy=\"0\" rx=\"20\" ry=\"1\"/>\n</g>\n</svg>\n", "line 3: want radii that keep the ellipse in the world"},
	{drawHeader + "\n<g id=\"layer0\">\n<line data-id=\"1\" x1=\"0\" y1=\"0\" x2=\"1\"/>\n</g>\n</svg>\n", "line 3: want the attributes data-id x1 y1 x2 y2"},
	{drawHeader + "\n<g id=\"layer3\">\n</svg>\n", "line 3: want </g>"},
}

// drawFile is a seed's picture file: absent, malformed, or the export of
// seeded shapes near the screen.
func drawFile(r *rand.Rand) (string, bool) {
	switch k := r.IntN(12); {
	case k == 0:
		return "", false
	case k == 1:
		return drawBadFiles[r.IntN(len(drawBadFiles))][0], true
	}
	var shapes []drawShape
	for id := 1; id <= r.IntN(25); id++ {
		s := drawShape{id: id + r.IntN(3)*id, layer: r.IntN(4), kind: r.IntN(4)}
		at := func() drawPoint { return drawPoint{r.IntN(900) - 50, r.IntN(700) - 50} }
		s.a, s.b = at(), at()
		switch s.kind {
		case 0:
			s.pts = []drawPoint{s.a}
			for range r.IntN(6) {
				s.pts = append(s.pts, drawPoint{s.pts[len(s.pts)-1].x + r.IntN(41) - 20, s.pts[len(s.pts)-1].y + r.IntN(41) - 20})
			}
		case 1, 2:
			s.w, s.h = r.IntN(120), r.IntN(80)
		}
		if !slices.ContainsFunc(shapes, func(o drawShape) bool { return o.id == s.id }) {
			shapes = append(shapes, s)
		}
	}
	return drawExport(shapes), true
}

// drawScript is a seed's script: presses near the screen's middle and on
// shapes, so most presses hit something.
func drawScript(r *rand.Rand) string {
	var b strings.Builder
	at := func() string {
		return fmt.Sprintf("%d %d", []int{0, 5, 100, 250, 400, 401, 799, 800, 1200, -30}[r.IntN(10)]+r.IntN(9)-4, []int{0, 7, 100, 300, 599, 600, -10}[r.IntN(7)]+r.IntN(9)-4)
	}
	for i := r.IntN(90); i > 0; i-- {
		switch k := r.IntN(40); {
		case k < 5:
			b.WriteString("show\n")
		case k < 9:
			b.WriteString("tool " + []string{"pen", "rect", "ellipse", "line", "select", "select"}[r.IntN(6)] + "\n")
		case k < 20:
			b.WriteString("down " + at() + "\n")
			for range r.IntN(8) {
				b.WriteString("move " + at() + "\n")
			}
			if r.IntN(6) == 0 {
				b.WriteString("escape\n")
			}
			b.WriteString("up " + at() + "\n")
		case k < 23:
			b.WriteString("hit " + at() + "\n")
		case k < 25:
			b.WriteString([]string{"delete", "escape"}[r.IntN(2)] + "\n")
		case k < 27:
			fmt.Fprintf(&b, "layer %d\n", r.IntN(6)-1)
		case k < 29:
			fmt.Fprintf(&b, "pan %d %d\n", []int{0, 50, -50, 400, 9999}[r.IntN(5)], []int{0, 30, -30, -9999}[r.IntN(4)])
		case k < 31:
			fmt.Fprintf(&b, "zoom %d\n", []int{25, 50, 100, 133, 400, 401, 24}[r.IntN(7)])
		case k < 33:
			b.WriteString([]string{"export", "save"}[r.IntN(2)] + "\n")
		case k < 34:
			b.WriteString("move " + at() + "\n")
		default:
			b.WriteString([]string{"", "# note", "fly", "down 1", "down 1 x", "zoom", "tool brush", "show me", "down 10001 0", "hit  1 2", "tool"}[r.IntN(11)] + "\n")
		}
	}
	b.WriteString("show\nexport\n")
	return b.String()
}

func drawExport(shapes []drawShape) string {
	sorted := slices.Clone(shapes)
	slices.SortFunc(sorted, func(a, b drawShape) int {
		if a.layer != b.layer {
			return a.layer - b.layer
		}
		return a.id - b.id
	})
	var b strings.Builder
	b.WriteString(drawHeader + "\n")
	layer := -1
	for _, s := range sorted {
		if s.layer != layer {
			if layer >= 0 {
				b.WriteString("</g>\n")
			}
			layer = s.layer
			fmt.Fprintf(&b, "<g id=\"layer%d\">\n", layer)
		}
		switch s.kind {
		case 0:
			var pts []string
			for _, p := range s.pts {
				pts = append(pts, fmt.Sprintf("%d,%d", p.x, p.y))
			}
			fmt.Fprintf(&b, "<polyline data-id=\"%d\" points=\"%s\"/>\n", s.id, strings.Join(pts, " "))
		case 1:
			fmt.Fprintf(&b, "<rect data-id=\"%d\" x=\"%d\" y=\"%d\" width=\"%d\" height=\"%d\"/>\n", s.id, s.a.x, s.a.y, s.w, s.h)
		case 2:
			fmt.Fprintf(&b, "<ellipse data-id=\"%d\" cx=\"%d\" cy=\"%d\" rx=\"%d\" ry=\"%d\"/>\n", s.id, s.a.x, s.a.y, s.w, s.h)
		case 3:
			fmt.Fprintf(&b, "<line data-id=\"%d\" x1=\"%d\" y1=\"%d\" x2=\"%d\" y2=\"%d\"/>\n", s.id, s.a.x, s.a.y, s.b.x, s.b.y)
		}
	}
	if layer >= 0 {
		b.WriteString("</g>\n")
	}
	b.WriteString("</svg>\n")
	return b.String()
}

// drawReadBack is the shapes of a well-formed export, the inverse of
// drawExport for the files drawFile writes.
func drawReadBack(text string) []drawShape {
	var out []drawShape
	layer := 0
	for _, ln := range strings.Split(text, "\n") {
		var s drawShape
		switch {
		case strings.HasPrefix(ln, "<g id=\"layer"):
			fmt.Sscanf(ln, "<g id=\"layer%d\">", &layer)
			continue
		case strings.HasPrefix(ln, "<polyline"):
			fmt.Sscanf(ln, "<polyline data-id=\"%d\"", &s.id)
			pts := strings.TrimSuffix(ln[strings.Index(ln, "points=\"")+len("points=\""):], "\"/>")
			for _, xy := range strings.Split(pts, " ") {
				var p drawPoint
				fmt.Sscanf(xy, "%d,%d", &p.x, &p.y)
				s.pts = append(s.pts, p)
			}
		case strings.HasPrefix(ln, "<rect"):
			s.kind = 1
			fmt.Sscanf(ln, "<rect data-id=\"%d\" x=\"%d\" y=\"%d\" width=\"%d\" height=\"%d\"/>", &s.id, &s.a.x, &s.a.y, &s.w, &s.h)
		case strings.HasPrefix(ln, "<ellipse"):
			s.kind = 2
			fmt.Sscanf(ln, "<ellipse data-id=\"%d\" cx=\"%d\" cy=\"%d\" rx=\"%d\" ry=\"%d\"/>", &s.id, &s.a.x, &s.a.y, &s.w, &s.h)
		case strings.HasPrefix(ln, "<line"):
			s.kind = 3
			fmt.Sscanf(ln, "<line data-id=\"%d\" x1=\"%d\" y1=\"%d\" x2=\"%d\" y2=\"%d\"/>", &s.id, &s.a.x, &s.a.y, &s.b.x, &s.b.y)
		default:
			continue
		}
		s.layer = layer
		out = append(out, s)
	}
	return out
}

func drawBox(s *drawShape) (lo, hi drawPoint) {
	switch s.kind {
	case 0:
		lo, hi = s.pts[0], s.pts[0]
		for _, p := range s.pts {
			lo, hi = drawPoint{min(lo.x, p.x), min(lo.y, p.y)}, drawPoint{max(hi.x, p.x), max(hi.y, p.y)}
		}
	case 1:
		lo, hi = s.a, drawPoint{s.a.x + s.w, s.a.y + s.h}
	case 2:
		lo, hi = drawPoint{s.a.x - s.w, s.a.y - s.h}, drawPoint{s.a.x + s.w, s.a.y + s.h}
	default:
		lo, hi = drawPoint{min(s.a.x, s.b.x), min(s.a.y, s.b.y)}, drawPoint{max(s.a.x, s.b.x), max(s.a.y, s.b.y)}
	}
	return lo, hi
}

// drawNear is whether p is within 4 of the segment a b, by the squared
// distance to the nearest point of the segment, computed apart from the
// programs' scaled form: the projection is compared as a fraction.
func drawNear(a, b, p drawPoint) bool {
	dx, dy := b.x-a.x, b.y-a.y
	len2 := dx*dx + dy*dy
	t := (p.x-a.x)*dx + (p.y-a.y)*dy
	if len2 == 0 || t <= 0 {
		return (p.x-a.x)*(p.x-a.x)+(p.y-a.y)*(p.y-a.y) <= 16
	}
	if t >= len2 {
		return (p.x-b.x)*(p.x-b.x)+(p.y-b.y)*(p.y-b.y) <= 16
	}
	// The cross product is the distance times the segment's length.
	c := (p.x-a.x)*dy - (p.y-a.y)*dx
	return c*c <= 16*len2
}

func drawHits(s *drawShape, p drawPoint) bool {
	switch s.kind {
	case 0:
		if len(s.pts) == 1 {
			return drawNear(s.pts[0], s.pts[0], p)
		}
		for i := 1; i < len(s.pts); i++ {
			if drawNear(s.pts[i-1], s.pts[i], p) {
				return true
			}
		}
		return false
	case 1:
		return s.a.x-4 <= p.x && p.x <= s.a.x+s.w+4 && s.a.y-4 <= p.y && p.y <= s.a.y+s.h+4
	case 2:
		a, b := s.w+4, s.h+4
		dx, dy := p.x-s.a.x, p.y-s.a.y
		return dx*dx*b*b+dy*dy*a*a <= a*a*b*b
	default:
		return drawNear(s.a, s.b, p)
	}
}

// drawSimplify is Douglas-Peucker by recursion: keep the ends, and the
// farthest point from the chord (the first of equals) while it is farther
// than 2, then each side.
func drawSimplify(pts []drawPoint) []drawPoint {
	if len(pts) <= 2 {
		return pts
	}
	a, b := pts[0], pts[len(pts)-1]
	dx, dy := b.x-a.x, b.y-a.y
	len2 := dx*dx + dy*dy
	best, bestD := 0, 0
	for i := 1; i < len(pts)-1; i++ {
		px, py := pts[i].x-a.x, pts[i].y-a.y
		d := px*px + py*py
		if len2 != 0 {
			c := px*dy - py*dx
			d = c * c
		}
		if d > bestD {
			best, bestD = i, d
		}
	}
	if (len2 == 0 && bestD <= 4) || (len2 != 0 && bestD <= 4*len2) {
		return []drawPoint{a, b}
	}
	left := drawSimplify(pts[:best+1])
	return append(slices.Clone(left), drawSimplify(pts[best:])[1:]...)
}

// drawReference is what draw-gui prints for script over the picture file,
// and the text the last save wrote (nil when none).
func drawReference(file string, exists bool, script string) (string, *string) {
	if exists {
		for _, bad := range drawBadFiles {
			if bad[0] == file {
				return corpusOutput("", "draw-gui: pic.svg: "+bad[1]+"\n", 1), nil
			}
		}
	}
	shapes := drawReadBack(file)
	next := 1
	for _, s := range shapes {
		next = max(next, s.id+1)
	}
	var out strings.Builder
	var saved *string
	pan, zoom, toolName, layer := drawPoint{}, 100, "pen", 0
	var selected []int
	mode := "idle"
	var points []drawPoint
	var from, to, moved drawPoint
	clamp := func(v, lo, hi int) int { return min(max(v, lo), hi) }
	world := func(x, y int) drawPoint {
		return drawPoint{clamp(x*100/zoom+pan.x, -10000, 10000), clamp(y*100/zoom+pan.y, -10000, 10000)}
	}
	screen := func(p drawPoint) string { return fmt.Sprintf("%d,%d", (p.x-pan.x)*zoom/100, (p.y-pan.y)*zoom/100) }
	order := func() []drawShape {
		s := slices.Clone(shapes)
		slices.SortFunc(s, func(a, b drawShape) int {
			if a.layer != b.layer {
				return a.layer - b.layer
			}
			return a.id - b.id
		})
		return s
	}
	find := func(id int) int { return slices.IndexFunc(shapes, func(s drawShape) bool { return s.id == id }) }
	top := func(p drawPoint) int {
		best := -1
		for i := range shapes {
			s := &shapes[i]
			if drawHits(s, p) && (best < 0 || s.layer > shapes[best].layer || s.layer == shapes[best].layer && s.id > shapes[best].id) {
				best = i
			}
		}
		if best < 0 {
			return 0
		}
		return shapes[best].id
	}
	paint := func(s drawShape) string {
		head := fmt.Sprintf("%d L%d ", s.id, s.layer)
		lo, hi := drawBox(&s)
		switch s.kind {
		case 0:
			return head + fmt.Sprintf("stroke n=%d %s..%s box=%s..%s", len(s.pts), screen(s.pts[0]), screen(s.pts[len(s.pts)-1]), screen(lo), screen(hi))
		case 1:
			return head + fmt.Sprintf("rect %s %dx%d", screen(s.a), s.w*zoom/100, s.h*zoom/100)
		case 2:
			return head + fmt.Sprintf("ellipse %s r=%d,%d", screen(s.a), s.w*zoom/100, s.h*zoom/100)
		}
		return head + "line " + screen(s.a) + ".." + screen(s.b)
	}
	shift := func(ids []int, d drawPoint) {
		for _, id := range ids {
			s := &shapes[find(id)]
			s.a, s.b = drawPoint{s.a.x + d.x, s.a.y + d.y}, drawPoint{s.b.x + d.x, s.b.y + d.y}
			pts := make([]drawPoint, len(s.pts))
			for i, p := range s.pts {
				pts[i] = drawPoint{p.x + d.x, p.y + d.y}
			}
			s.pts = pts
		}
	}
	dragTo := func(w drawPoint) {
		want := drawPoint{w.x - from.x - moved.x, w.y - from.y - moved.y}
		lo, hi := drawPoint{-20000, -20000}, drawPoint{20000, 20000}
		for _, id := range selected {
			blo, bhi := drawBox(&shapes[find(id)])
			lo = drawPoint{max(lo.x, -10000-blo.x), max(lo.y, -10000-blo.y)}
			hi = drawPoint{min(hi.x, 10000-bhi.x), min(hi.y, 10000-bhi.y)}
		}
		step := drawPoint{clamp(want.x, lo.x, hi.x), clamp(want.y, lo.y, hi.y)}
		shift(selected, step)
		moved = drawPoint{moved.x + step.x, moved.y + step.y}
	}
	abs := func(v int) int { return max(v, -v) }
	finish := func() {
		a, b := points[0], points[len(points)-1]
		s := drawShape{id: next, layer: layer}
		switch toolName {
		case "pen":
			s.pts = drawSimplify(points)
		case "rect":
			s.kind, s.a, s.w, s.h = 1, drawPoint{min(a.x, b.x), min(a.y, b.y)}, abs(b.x-a.x), abs(b.y-a.y)
		case "ellipse":
			s.kind, s.a, s.w, s.h = 2, a, min(abs(b.x-a.x), 10000-abs(a.x)), min(abs(b.y-a.y), 10000-abs(a.y))
		default:
			s.kind, s.a, s.b = 3, a, b
		}
		if s.kind != 0 && a == b {
			return
		}
		next++
		shapes = append(shapes, s)
		fmt.Fprintf(&out, "added %s\n", paint(s))
	}
	bad := func(ln, want string) { fmt.Fprintf(&out, "? \"%s\": want %s\n", ln, want) }
	for _, ln := range strings.Split(strings.TrimSuffix(script, "\n"), "\n") {
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		if rest, ok := strings.CutPrefix(ln, "echo "); ok {
			// A scene's line (drawSceneAsPlay); a play script has none.
			out.WriteString(rest + "\n")
			continue
		}
		words := strings.Split(ln, " ")
		var nums []int
		numbers := func(want int, wantText string) bool {
			if len(words)-1 != want {
				bad(ln, wantText)
				return false
			}
			for _, w := range words[1:] {
				v, err := strconv.Atoi(w)
				if err != nil || v < -10000 || v > 10000 {
					bad(ln, "a number from -10000 to 10000")
					return false
				}
				nums = append(nums, v)
			}
			return true
		}
		switch words[0] {
		case "show", "export", "save", "delete", "escape":
			if len(words) != 1 {
				bad(ln, "no argument")
				continue
			}
		case "hit", "down", "move", "up", "pan":
			if !numbers(2, "two numbers") {
				continue
			}
		case "layer", "zoom":
			if !numbers(1, "a number") {
				continue
			}
		case "tool":
			if len(words) != 2 || !slices.Contains([]string{"pen", "rect", "ellipse", "line", "select"}, words[1]) {
				bad(ln, "a tool: pen rect ellipse line select")
				continue
			}
		default:
			bad(ln, "a command: show export save hit down move up pan layer zoom tool delete escape")
			continue
		}
		switch words[0] {
		case "show":
			ids := make([]string, len(selected))
			for i, id := range selected {
				ids[i] = strconv.Itoa(id)
			}
			fmt.Fprintf(&out, "%s tool=%s layer=%d zoom=%d pan=%d,%d shapes=%d selected=[%s]", mode, toolName, layer, zoom, pan.x, pan.y, len(shapes), strings.Join(ids, ","))
			switch mode {
			case "drawing":
				fmt.Fprintf(&out, " points=%d", len(points))
			case "dragging":
				fmt.Fprintf(&out, " moved=%d,%d", moved.x, moved.y)
			case "selecting":
				fmt.Fprintf(&out, " box=%d,%d..%d,%d", min(from.x, to.x), min(from.y, to.y), max(from.x, to.x), max(from.y, to.y))
			}
			out.WriteByte('\n')
			for _, s := range order() {
				lo, hi := drawBox(&s)
				lx, ly := (lo.x-pan.x)*zoom/100, (lo.y-pan.y)*zoom/100
				hx, hy := (hi.x-pan.x)*zoom/100, (hi.y-pan.y)*zoom/100
				if lx <= 800 && hx >= 0 && ly <= 600 && hy >= 0 {
					out.WriteString(paint(s) + "\n")
				}
			}
		case "export":
			out.WriteString(drawExport(shapes))
		case "save":
			text := drawExport(shapes)
			saved = &text
			fmt.Fprintf(&out, "saved pic.svg (%d bytes)\n", len(text))
		case "hit":
			if id := top(world(nums[0], nums[1])); id > 0 {
				fmt.Fprintf(&out, "hit %d,%d = %d\n", nums[0], nums[1], id)
			} else {
				fmt.Fprintf(&out, "hit %d,%d = none\n", nums[0], nums[1])
			}
		case "tool":
			if mode == "idle" {
				toolName = words[1]
			}
		case "layer":
			if mode == "idle" && nums[0] >= 0 && nums[0] < 4 {
				layer = nums[0]
			}
		case "zoom":
			if mode == "idle" && nums[0] >= 25 && nums[0] <= 400 {
				zoom = nums[0]
			}
		case "pan":
			if mode == "idle" {
				pan = drawPoint{clamp(pan.x+nums[0], -10000, 10000), clamp(pan.y+nums[1], -10000, 10000)}
			}
		case "delete":
			if mode == "idle" && len(selected) > 0 {
				ids := make([]string, len(selected))
				for i, id := range selected {
					shapes = slices.Delete(shapes, find(id), find(id)+1)
					ids[i] = strconv.Itoa(id)
				}
				selected = nil
				fmt.Fprintf(&out, "removed %s\n", strings.Join(ids, ","))
			}
		case "escape":
			switch mode {
			case "idle":
				selected = nil
			case "dragging":
				shift(selected, drawPoint{-moved.x, -moved.y})
			}
			mode = "idle"
		case "down":
			if mode != "idle" {
				continue
			}
			w := world(nums[0], nums[1])
			switch {
			case toolName != "select":
				mode, points = "drawing", []drawPoint{w}
			case top(w) > 0:
				if id := top(w); !slices.Contains(selected, id) {
					selected = []int{id}
				}
				mode, from, moved = "dragging", w, drawPoint{}
			default:
				mode, selected, from, to = "selecting", nil, w, w
			}
		case "move", "up":
			w := world(nums[0], nums[1])
			switch mode {
			case "drawing":
				if toolName != "pen" {
					points = []drawPoint{points[0], w}
				} else if points[len(points)-1] != w {
					points = append(points, w)
				}
				if words[0] == "up" {
					finish()
					mode = "idle"
				}
			case "dragging":
				dragTo(w)
				if words[0] == "up" {
					mode = "idle"
				}
			case "selecting":
				to = w
				if words[0] == "up" {
					lo := drawPoint{min(from.x, w.x), min(from.y, w.y)}
					hi := drawPoint{max(from.x, w.x), max(from.y, w.y)}
					selected = nil
					for _, s := range order() {
						blo, bhi := drawBox(&s)
						if lo.x <= blo.x && lo.y <= blo.y && bhi.x <= hi.x && bhi.y <= hi.y {
							selected = append(selected, s.id)
						}
					}
					slices.Sort(selected)
					mode = "idle"
				}
			}
		}
	}
	return corpusOutput(out.String(), "", 0), saved
}
