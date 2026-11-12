// Command control is the hand Go control of examples/draw-gui (corpus
// wave GP3a): the same drawing tool written directly in Go, shapes as
// structs with a kind tag in a map by id, a uniform grid of cells in a Go
// map, and frames written to one buffered writer. It implements
// `play FILE` only, the command TestDrawGuiAgreesWithControl drives; its
// output is byte for byte the Ergo program's.
package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

const (
	limit     = 10000
	tolerance = 4
	epsilon   = 2
	cellSize  = 64
	layers    = 4
	header    = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="-10000 -10000 20000 20000">`
)

type point struct{ x, y int }

type kind int

const (
	stroke kind = iota
	rect
	ellipse
	line
)

// shape is one shape: a stroke's points; a rect's corner and size; an
// ellipse's centre and radii; a line's two ends (a and b).
type shape struct {
	kind   kind
	points []point
	a, b   point
	w, h   int
}

type placed struct {
	id, layer int
	s         shape
}

type box struct{ lo, hi point }

func clamp(v, lo, hi int) int { return min(max(v, lo), hi) }
func bound(p point) point     { return point{clamp(p.x, -limit, limit), clamp(p.y, -limit, limit)} }
func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func boxOf(s *shape) box {
	switch s.kind {
	case stroke:
		lo, hi := s.points[0], s.points[0]
		for _, p := range s.points {
			lo = point{min(lo.x, p.x), min(lo.y, p.y)}
			hi = point{max(hi.x, p.x), max(hi.y, p.y)}
		}
		return box{lo, hi}
	case rect:
		return box{s.a, point{s.a.x + s.w, s.a.y + s.h}}
	case ellipse:
		return box{point{s.a.x - s.w, s.a.y - s.h}, point{s.a.x + s.w, s.a.y + s.h}}
	default:
		return box{point{min(s.a.x, s.b.x), min(s.a.y, s.b.y)}, point{max(s.a.x, s.b.x), max(s.a.y, s.b.y)}}
	}
}

func grow(b box, d int) box {
	return box{point{b.lo.x - d, b.lo.y - d}, point{b.hi.x + d, b.hi.y + d}}
}
func inside(p point, b box) bool {
	return b.lo.x <= p.x && p.x <= b.hi.x && b.lo.y <= p.y && p.y <= b.hi.y
}

func near(a, b, p point) bool {
	ax, ay, bx, by := p.x-a.x, p.y-a.y, b.x-a.x, b.y-a.y
	len2, t, tol2 := bx*bx+by*by, ax*bx+ay*by, tolerance*tolerance
	if len2 == 0 || t <= 0 {
		return ax*ax+ay*ay <= tol2
	}
	if t >= len2 {
		cx, cy := p.x-b.x, p.y-b.y
		return cx*cx+cy*cy <= tol2
	}
	return (ax*ax+ay*ay)*len2-t*t <= tol2*len2
}

func hits(s *shape, p point) bool {
	switch s.kind {
	case stroke:
		if len(s.points) == 1 {
			return near(s.points[0], s.points[0], p)
		}
		for i := 1; i < len(s.points); i++ {
			if near(s.points[i-1], s.points[i], p) {
				return true
			}
		}
		return false
	case rect:
		return inside(p, grow(boxOf(s), tolerance))
	case ellipse:
		a, b := s.w+tolerance, s.h+tolerance
		dx, dy := p.x-s.a.x, p.y-s.a.y
		return dx*dx*b*b+dy*dy*a*a <= a*a*b*b
	default:
		return near(s.a, s.b, p)
	}
}

// simplify keeps the points of a Douglas-Peucker simplification.
func simplify(points []point) []point {
	if len(points) <= 2 {
		return points
	}
	keep := make([]bool, len(points))
	keep[0], keep[len(points)-1] = true, true
	spans := [][2]int{{0, len(points) - 1}}
	for len(spans) > 0 {
		sp := spans[len(spans)-1]
		spans = spans[:len(spans)-1]
		lo, hi := sp[0], sp[1]
		a, b := points[lo], points[hi]
		bx, by := b.x-a.x, b.y-a.y
		len2 := bx*bx + by*by
		best, bestD := 0, 0
		for i := lo + 1; i < hi; i++ {
			px, py := points[i].x-a.x, points[i].y-a.y
			var d int
			if len2 == 0 {
				d = px*px + py*py
			} else {
				c := px*by - py*bx
				d = c * c
			}
			if d > bestD {
				best, bestD = i, d
			}
		}
		lim := epsilon * epsilon
		if len2 != 0 {
			lim *= len2
		}
		if bestD > lim {
			keep[best] = true
			spans = append(spans, [2]int{lo, best}, [2]int{best, hi})
		}
	}
	out := make([]point, 0, len(points))
	for i, p := range points {
		if keep[i] {
			out = append(out, p)
		}
	}
	return out
}

func cellOf(v int) int {
	if v >= 0 {
		return v / cellSize
	}
	return -((-v + cellSize - 1) / cellSize)
}

type picture struct {
	shapes map[int]*placed
	next   int
	grid   map[[2]int][]int
}

func newPicture() *picture {
	return &picture{shapes: map[int]*placed{}, next: 1, grid: map[[2]int][]int{}}
}

func (pc *picture) index(id int, s *shape) {
	b := grow(boxOf(s), tolerance)
	for cx := cellOf(b.lo.x); cx <= cellOf(b.hi.x); cx++ {
		for cy := cellOf(b.lo.y); cy <= cellOf(b.hi.y); cy++ {
			k := [2]int{cx, cy}
			pc.grid[k] = append(pc.grid[k], id)
		}
	}
}

func (pc *picture) unindex(id int, s *shape) {
	b := grow(boxOf(s), tolerance)
	for cx := cellOf(b.lo.x); cx <= cellOf(b.hi.x); cx++ {
		for cy := cellOf(b.lo.y); cy <= cellOf(b.hi.y); cy++ {
			k := [2]int{cx, cy}
			ids := slices.DeleteFunc(pc.grid[k], func(i int) bool { return i == id })
			if len(ids) == 0 {
				delete(pc.grid, k)
			} else {
				pc.grid[k] = ids
			}
		}
	}
}

func (pc *picture) add(layer int, s shape) *placed {
	p := &placed{id: pc.next, layer: clamp(layer, 0, layers-1), s: s}
	pc.shapes[p.id] = p
	pc.next++
	pc.index(p.id, &p.s)
	return p
}

func (pc *picture) place(p *placed) {
	pc.remove(p.id)
	p.layer = clamp(p.layer, 0, layers-1)
	pc.shapes[p.id] = p
	pc.next = max(pc.next, p.id+1)
	pc.index(p.id, &p.s)
}

func (pc *picture) remove(id int) {
	if p, ok := pc.shapes[id]; ok {
		pc.unindex(id, &p.s)
		delete(pc.shapes, id)
	}
}

func (pc *picture) room(ids []int, want point) point {
	lo, hi := point{-2 * limit, -2 * limit}, point{2 * limit, 2 * limit}
	for _, id := range ids {
		if p, ok := pc.shapes[id]; ok {
			b := boxOf(&p.s)
			lo = point{max(lo.x, -limit-b.lo.x), max(lo.y, -limit-b.lo.y)}
			hi = point{min(hi.x, limit-b.hi.x), min(hi.y, limit-b.hi.y)}
		}
	}
	return point{clamp(want.x, lo.x, hi.x), clamp(want.y, lo.y, hi.y)}
}

func (pc *picture) move(ids []int, d point) {
	if d.x == 0 && d.y == 0 {
		return
	}
	for _, id := range ids {
		p, ok := pc.shapes[id]
		if !ok {
			continue
		}
		pc.unindex(id, &p.s)
		switch p.s.kind {
		case stroke:
			pts := make([]point, len(p.s.points))
			for i, q := range p.s.points {
				pts[i] = point{q.x + d.x, q.y + d.y}
			}
			p.s.points = pts
		default:
			p.s.a = point{p.s.a.x + d.x, p.s.a.y + d.y}
			p.s.b = point{p.s.b.x + d.x, p.s.b.y + d.y}
		}
		pc.index(id, &p.s)
	}
}

func above(a, b *placed) bool { return a.layer > b.layer || a.layer == b.layer && a.id > b.id }

func (pc *picture) top(p point) (int, bool) {
	var best *placed
	for _, id := range pc.grid[[2]int{cellOf(p.x), cellOf(p.y)}] {
		s := pc.shapes[id]
		if hits(&s.s, p) && (best == nil || above(s, best)) {
			best = s
		}
	}
	if best == nil {
		return 0, false
	}
	return best.id, true
}

func (pc *picture) order() []*placed {
	out := make([]*placed, 0, len(pc.shapes))
	for _, p := range pc.shapes {
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b *placed) int {
		if a.layer != b.layer {
			return a.layer - b.layer
		}
		return a.id - b.id
	})
	return out
}

type mode int

const (
	idle mode = iota
	drawing
	dragging
	selecting
)

var toolNames = []string{"pen", "rect", "ellipse", "line", "select"}

const (
	pen = iota
	rectTool
	ellipseTool
	lineTool
	selectTool
)

type tool struct {
	pic      *picture
	pan      point
	zoom     int
	tool     int
	layer    int
	selected []int
	mode     mode
	points   []point
	from     point
	to       point
	moved    point
	name     string
	out      *bufio.Writer
}

func (t *tool) world(s point) point {
	return bound(point{s.x*100/t.zoom + t.pan.x, s.y*100/t.zoom + t.pan.y})
}
func (t *tool) screen(w point) point {
	return point{(w.x - t.pan.x) * t.zoom / 100, (w.y - t.pan.y) * t.zoom / 100}
}

func (t *tool) pair(p point) string { return strconv.Itoa(p.x) + "," + strconv.Itoa(p.y) }

func (t *tool) paint(p *placed) string {
	head := fmt.Sprintf("%d L%d ", p.id, p.layer)
	s := &p.s
	z := t.zoom
	switch s.kind {
	case stroke:
		b := boxOf(s)
		return head + fmt.Sprintf("stroke n=%d %s..%s box=%s..%s", len(s.points), t.pair(t.screen(s.points[0])), t.pair(t.screen(s.points[len(s.points)-1])), t.pair(t.screen(b.lo)), t.pair(t.screen(b.hi)))
	case rect:
		return head + fmt.Sprintf("rect %s %dx%d", t.pair(t.screen(s.a)), s.w*z/100, s.h*z/100)
	case ellipse:
		return head + fmt.Sprintf("ellipse %s r=%d,%d", t.pair(t.screen(s.a)), s.w*z/100, s.h*z/100)
	default:
		return head + "line " + t.pair(t.screen(s.a)) + ".." + t.pair(t.screen(s.b))
	}
}

func between(a, b point) box {
	return box{point{min(a.x, b.x), min(a.y, b.y)}, point{max(a.x, b.x), max(a.y, b.y)}}
}

func (t *tool) frame() {
	w := t.out
	ids := make([]string, len(t.selected))
	for i, id := range t.selected {
		ids[i] = strconv.Itoa(id)
	}
	names := [...]string{"idle", "drawing", "dragging", "selecting"}
	fmt.Fprintf(w, "%s tool=%s layer=%d zoom=%d pan=%s shapes=%d selected=[%s]", names[t.mode], toolNames[t.tool], t.layer, t.zoom, t.pair(t.pan), len(t.pic.shapes), strings.Join(ids, ","))
	switch t.mode {
	case drawing:
		fmt.Fprintf(w, " points=%d", len(t.points))
	case dragging:
		fmt.Fprintf(w, " moved=%s", t.pair(t.moved))
	case selecting:
		b := between(t.from, t.to)
		fmt.Fprintf(w, " box=%s..%s", t.pair(b.lo), t.pair(b.hi))
	}
	w.WriteByte('\n')
	for _, p := range t.pic.order() {
		b := boxOf(&p.s)
		on := between(t.screen(b.lo), t.screen(b.hi))
		if on.lo.x <= 800 && 0 <= on.hi.x && on.lo.y <= 600 && 0 <= on.hi.y {
			w.WriteString(t.paint(p))
			w.WriteByte('\n')
		}
	}
}

func export(pc *picture) string {
	var b strings.Builder
	b.WriteString(header + "\n")
	layer := -1
	for _, p := range pc.order() {
		if p.layer != layer {
			if layer >= 0 {
				b.WriteString("</g>\n")
			}
			layer = p.layer
			fmt.Fprintf(&b, "<g id=\"layer%d\">\n", layer)
		}
		s := &p.s
		switch s.kind {
		case stroke:
			pts := make([]string, len(s.points))
			for i, q := range s.points {
				pts[i] = fmt.Sprintf("%d,%d", q.x, q.y)
			}
			fmt.Fprintf(&b, "<polyline data-id=\"%d\" points=\"%s\"/>\n", p.id, strings.Join(pts, " "))
		case rect:
			fmt.Fprintf(&b, "<rect data-id=\"%d\" x=\"%d\" y=\"%d\" width=\"%d\" height=\"%d\"/>\n", p.id, s.a.x, s.a.y, s.w, s.h)
		case ellipse:
			fmt.Fprintf(&b, "<ellipse data-id=\"%d\" cx=\"%d\" cy=\"%d\" rx=\"%d\" ry=\"%d\"/>\n", p.id, s.a.x, s.a.y, s.w, s.h)
		default:
			fmt.Fprintf(&b, "<line data-id=\"%d\" x1=\"%d\" y1=\"%d\" x2=\"%d\" y2=\"%d\"/>\n", p.id, s.a.x, s.a.y, s.b.x, s.b.y)
		}
	}
	if layer >= 0 {
		b.WriteString("</g>\n")
	}
	b.WriteString("</svg>\n")
	return b.String()
}

type badSvg struct {
	line int
	want string
}

func (b *badSvg) Error() string { return fmt.Sprintf("line %d: want %s", b.line, b.want) }

func parse(text string) (*picture, error) {
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if len(lines) < 2 || lines[0] != header {
		return nil, &badSvg{1, "the svg header"}
	}
	if lines[len(lines)-1] != "</svg>" {
		return nil, &badSvg{len(lines), "</svg>"}
	}
	pc := newPicture()
	layer, last := -1, -1
	for i := 1; i < len(lines)-1; i++ {
		ln, n := lines[i], i+1
		if layer < 0 {
			l := -1
			for k := 0; k < layers; k++ {
				if ln == fmt.Sprintf("<g id=\"layer%d\">", k) {
					l = k
				}
			}
			if l <= last {
				return nil, &badSvg{n, `<g id="layerN"> of a higher layer, 0 to 3`}
			}
			layer, last = l, l
			continue
		}
		if ln == "</g>" {
			layer = -1
			continue
		}
		p, err := shapeOf(ln, n, layer)
		if err != nil {
			return nil, err
		}
		if _, ok := pc.shapes[p.id]; p.id < 1 || ok {
			return nil, &badSvg{n, "a new data-id of 1 or more"}
		}
		pc.place(p)
	}
	if layer >= 0 {
		return nil, &badSvg{len(lines), "</g>"}
	}
	return pc, nil
}

func shapeOf(ln string, n, layer int) (*placed, error) {
	parts := strings.Split(ln, " ")
	if len(parts) < 2 || !strings.HasSuffix(ln, `"/>`) {
		return nil, &badSvg{n, `an element <tag data-id="N" ..."/>`}
	}
	tag := parts[0]
	var names, values []string
	for _, a := range strings.Split(strings.TrimSuffix(ln[len(tag)+1:], "/>"), `" `) {
		name, value, ok := strings.Cut(strings.TrimSuffix(a, `"`), `="`)
		if !ok {
			return nil, &badSvg{n, `name="value" attributes`}
		}
		names, values = append(names, name), append(values, value)
	}
	want := func(w ...string) error {
		if !slices.Equal(names, w) {
			return &badSvg{n, "the attributes " + strings.Join(w, " ")}
		}
		return nil
	}
	coord := func(s string) (int, error) {
		v, err := strconv.Atoi(s)
		if err != nil {
			return 0, &badSvg{n, "a whole number"}
		}
		if v < -limit || v > limit {
			return 0, &badSvg{n, "a coordinate from -10000 to 10000"}
		}
		return v, nil
	}
	coords := func(from int, count int) ([]int, error) {
		out := make([]int, count)
		for i := range out {
			v, err := coord(values[from+i])
			if err != nil {
				return nil, err
			}
			out[i] = v
		}
		return out, nil
	}
	id := func() (int, error) {
		v, err := strconv.Atoi(values[0])
		if err != nil {
			return 0, &badSvg{n, "a whole number"}
		}
		return v, nil
	}
	var s shape
	switch tag {
	case "<polyline":
		if err := want("data-id", "points"); err != nil {
			return nil, err
		}
		s.kind = stroke
		for _, xy := range strings.Split(values[1], " ") {
			xs, ys, ok := strings.Cut(xy, ",")
			if !ok {
				return nil, &badSvg{n, "points X,Y"}
			}
			x, err := coord(xs)
			if err != nil {
				return nil, err
			}
			y, err := coord(ys)
			if err != nil {
				return nil, err
			}
			s.points = append(s.points, point{x, y})
		}
	case "<rect":
		if err := want("data-id", "x", "y", "width", "height"); err != nil {
			return nil, err
		}
		v, err := coords(1, 4)
		if err != nil {
			return nil, err
		}
		if v[2] < 0 || v[3] < 0 || v[0]+v[2] > limit || v[1]+v[3] > limit {
			return nil, &badSvg{n, "a width and a height that keep the rect in the world"}
		}
		s = shape{kind: rect, a: point{v[0], v[1]}, w: v[2], h: v[3]}
	case "<ellipse":
		if err := want("data-id", "cx", "cy", "rx", "ry"); err != nil {
			return nil, err
		}
		v, err := coords(1, 4)
		if err != nil {
			return nil, err
		}
		if v[2] < 0 || v[3] < 0 || v[2] > limit-abs(v[0]) || v[3] > limit-abs(v[1]) {
			return nil, &badSvg{n, "radii that keep the ellipse in the world"}
		}
		s = shape{kind: ellipse, a: point{v[0], v[1]}, w: v[2], h: v[3]}
	case "<line":
		if err := want("data-id", "x1", "y1", "x2", "y2"); err != nil {
			return nil, err
		}
		v, err := coords(1, 4)
		if err != nil {
			return nil, err
		}
		s = shape{kind: line, a: point{v[0], v[1]}, b: point{v[2], v[3]}}
	default:
		return nil, &badSvg{n, "polyline, rect, ellipse or line"}
	}
	i, err := id()
	if err != nil {
		return nil, err
	}
	return &placed{id: i, layer: layer, s: s}, nil
}

func (t *tool) bad(ln, want string) { fmt.Fprintf(t.out, "? \"%s\": want %s\n", ln, want) }

func (t *tool) line(ln string) {
	if ln == "" || strings.HasPrefix(ln, "#") {
		return
	}
	words := strings.Split(ln, " ")
	args := len(words) - 1
	whole := func(s string) (int, bool) {
		v, err := strconv.Atoi(s)
		if err != nil || v < -limit || v > limit {
			t.bad(ln, "a number from -10000 to 10000")
			return 0, false
		}
		return v, true
	}
	switch words[0] {
	case "show", "export", "save", "delete", "escape":
		if args != 0 {
			t.bad(ln, "no argument")
			return
		}
		switch words[0] {
		case "show":
			t.frame()
		case "export":
			t.out.WriteString(export(t.pic))
		case "save":
			t.save()
		case "delete":
			t.delete()
		default:
			t.escape()
		}
	case "hit", "down", "move", "up", "pan":
		if args != 2 {
			t.bad(ln, "two numbers")
			return
		}
		x, ok := whole(words[1])
		if !ok {
			return
		}
		y, ok := whole(words[2])
		if !ok {
			return
		}
		p := point{x, y}
		switch words[0] {
		case "hit":
			if id, ok := t.pic.top(t.world(p)); ok {
				fmt.Fprintf(t.out, "hit %d,%d = %d\n", x, y, id)
			} else {
				fmt.Fprintf(t.out, "hit %d,%d = none\n", x, y)
			}
		case "down":
			t.down(p)
		case "move":
			t.drag(p, false)
		case "up":
			t.drag(p, true)
		default:
			if t.mode == idle {
				t.pan = point{clamp(t.pan.x+x, -limit, limit), clamp(t.pan.y+y, -limit, limit)}
			}
		}
	case "layer", "zoom":
		if args != 1 {
			t.bad(ln, "a number")
			return
		}
		v, ok := whole(words[1])
		if !ok || t.mode != idle {
			return
		}
		if words[0] == "layer" && v >= 0 && v < layers {
			t.layer = v
		}
		if words[0] == "zoom" && v >= 25 && v <= 400 {
			t.zoom = v
		}
	case "tool":
		k := -1
		if args == 1 {
			k = slices.Index(toolNames, words[1])
		}
		if k < 0 {
			t.bad(ln, "a tool: pen rect ellipse line select")
			return
		}
		if t.mode == idle {
			t.tool = k
		}
	default:
		t.bad(ln, "a command: show export save hit down move up pan layer zoom tool delete escape")
	}
}

func (t *tool) down(s point) {
	if t.mode != idle {
		return
	}
	w := t.world(s)
	if t.tool != selectTool {
		t.mode, t.points = drawing, []point{w}
		return
	}
	if id, ok := t.pic.top(w); ok {
		if !slices.Contains(t.selected, id) {
			t.selected = []int{id}
		}
		t.mode, t.from, t.moved = dragging, w, point{}
		return
	}
	t.selected = nil
	t.mode, t.from, t.to = selecting, w, w
}

func (t *tool) follow(w point) {
	if t.tool == pen {
		if t.points[len(t.points)-1] != w {
			t.points = append(t.points, w)
		}
		return
	}
	t.points = []point{t.points[0], w}
}

func (t *tool) drag(s point, up bool) {
	w := t.world(s)
	switch t.mode {
	case drawing:
		t.follow(w)
		if up {
			t.finish()
			t.mode = idle
		}
	case dragging:
		want := point{w.x - t.from.x - t.moved.x, w.y - t.from.y - t.moved.y}
		step := t.pic.room(t.selected, want)
		t.pic.move(t.selected, step)
		t.moved = point{t.moved.x + step.x, t.moved.y + step.y}
		if up {
			t.mode = idle
		}
	case selecting:
		t.to = w
		if up {
			b := between(t.from, w)
			var ids []int
			for _, p := range t.pic.order() {
				bb := boxOf(&p.s)
				if inside(bb.lo, b) && inside(bb.hi, b) {
					ids = append(ids, p.id)
				}
			}
			slices.Sort(ids)
			t.selected = ids
			t.mode = idle
		}
	}
}

func (t *tool) finish() {
	a, b := t.points[0], t.points[len(t.points)-1]
	var s shape
	switch t.tool {
	case pen:
		s = shape{kind: stroke, points: simplify(t.points)}
	case rectTool:
		if a == b {
			return
		}
		s = shape{kind: rect, a: point{min(a.x, b.x), min(a.y, b.y)}, w: abs(b.x - a.x), h: abs(b.y - a.y)}
	case ellipseTool:
		if a == b {
			return
		}
		s = shape{kind: ellipse, a: a, w: min(abs(b.x-a.x), limit-abs(a.x)), h: min(abs(b.y-a.y), limit-abs(a.y))}
	default:
		if a == b {
			return
		}
		s = shape{kind: line, a: a, b: b}
	}
	p := t.pic.add(t.layer, s)
	fmt.Fprintf(t.out, "added %s\n", t.paint(p))
}

func (t *tool) delete() {
	if t.mode != idle || len(t.selected) == 0 {
		return
	}
	ids := make([]string, len(t.selected))
	for i, id := range t.selected {
		t.pic.remove(id)
		ids[i] = strconv.Itoa(id)
	}
	t.selected = nil
	fmt.Fprintf(t.out, "removed %s\n", strings.Join(ids, ","))
}

func (t *tool) escape() {
	switch t.mode {
	case idle:
		t.selected = nil
	case drawing, selecting:
		t.mode = idle
	case dragging:
		t.pic.move(t.selected, point{-t.moved.x, -t.moved.y})
		t.mode = idle
	}
}

func (t *tool) save() {
	text := export(t.pic)
	tmp, err := os.CreateTemp(filepath.Dir(t.name), ".draw-*")
	if err == nil {
		_, err = tmp.WriteString(text)
		if cerr := tmp.Close(); err == nil {
			err = cerr
		}
		if err == nil {
			err = os.Rename(tmp.Name(), t.name)
		}
		if err != nil {
			os.Remove(tmp.Name())
		}
	}
	if err != nil {
		fmt.Fprintf(t.out, "cannot save %s: %v\n", t.name, err)
		return
	}
	fmt.Fprintf(t.out, "saved %s (%d bytes)\n", t.name, len(text))
}

func main() {
	if len(os.Args) != 3 || os.Args[1] != "play" {
		fmt.Fprintln(os.Stderr, "usage: control play FILE")
		os.Exit(2)
	}
	name := os.Args[2]
	pic := newPicture()
	text, err := os.ReadFile(name)
	switch {
	case err == nil:
		pic, err = parse(string(text))
		if err != nil {
			fmt.Fprintf(os.Stderr, "draw-gui: %s: %v\n", name, err)
			os.Exit(1)
		}
	case !errors.Is(err, fs.ErrNotExist):
		fmt.Fprintf(os.Stderr, "draw-gui: %s: %v\n", name, err)
		os.Exit(1)
	}
	out := bufio.NewWriterSize(os.Stdout, 1<<16)
	t := &tool{pic: pic, zoom: 100, name: name, out: out}
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	lines := strings.Split(string(input), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	for _, ln := range lines {
		t.line(ln)
	}
	out.Flush()
}
