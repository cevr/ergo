package mygo

import (
	"bufio"
	"flag"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"
)

var layoutSeed = flag.Uint64("layout.seed", 0, "run TestLayoutMatchesMyGo on this seed only")

// length is a size: automatic (unit 0), DIPs (1) or a percentage (2).
type length struct {
	unit int
	v    float32
}

func (l length) code() string {
	switch l.unit {
	case 1:
		return "p" + num(l.v)
	case 2:
		return "%" + num(l.v)
	}
	return "a"
}

// style is a node's style as the oracle reads it and as MyGo is told it.
// Aligns are s, c, e, t (stretch); self "-" is the parent's.
type style struct {
	row                  bool
	width, height, basis length
	grow, shrink         float32
	pad, margin          [4]float32
	gap                  float32
	items, self, justify string
	absolute             bool
	left, top            float32
}

func num(v float32) string { return strconv.FormatFloat(float64(v), 'g', -1, 32) }

func (s style) code() string {
	f := []string{"c"}
	if s.row {
		f[0] = "r"
	}
	f = append(f, s.width.code(), s.height.code(), s.basis.code(), num(s.grow), num(s.shrink))
	for _, v := range s.pad {
		f = append(f, num(v))
	}
	for _, v := range s.margin {
		f = append(f, num(v))
	}
	f = append(f, num(s.gap), s.items, s.self, s.justify)
	if s.absolute {
		f = append(f, "a", num(s.left), num(s.top))
	} else {
		f = append(f, "f", "0", "0")
	}
	return strings.Join(f, " ")
}

// tree is a generated box tree in a w×h window: parent[i] is -1 for a
// child of the window, and parents come before their children.
type tree struct {
	w, h   float32
	parent []int
	styles []style
}

// genTree draws a tree of up to 14 boxes, four deep at most, whose leaves
// have fixed sizes, so neither side measures content.
func genTree(r *rand.Rand) tree {
	t := tree{w: float32(300 + r.IntN(1200)), h: float32(300 + r.IntN(1200))}
	var grow func(parent, depth int)
	grow = func(parent, depth int) {
		i := len(t.parent)
		t.parent = append(t.parent, parent)
		t.styles = append(t.styles, style{})
		kids := 0
		if depth < 4 && len(t.parent) < 14 {
			kids = r.IntN(4)
			if depth == 0 {
				kids = 1 + r.IntN(3)
			}
		}
		t.styles[i] = genStyle(r, kids == 0, depth == 0)
		for range kids {
			if len(t.parent) < 14 {
				grow(i, depth+1)
			}
		}
	}
	for range 1 + r.IntN(2) {
		grow(-1, 0)
	}
	return t
}

// pick is one of vs.
func pick[T any](r *rand.Rand, vs ...T) T { return vs[r.IntN(len(vs))] }

func genStyle(r *rand.Rand, leaf, top bool) style {
	small := func() float32 { return pick[float32](r, 0, 0, 2, 5, 8, 12.5) }
	size := func() length {
		switch r.IntN(6) {
		case 0, 1, 2:
			return length{}
		case 3, 4:
			return length{1, float32(10 + r.IntN(120))}
		}
		return length{2, pick[float32](r, 25, 50, 100, 33.3)}
	}
	s := style{
		row: r.IntN(2) == 0, width: size(), height: size(), basis: length{},
		shrink: pick[float32](r, 1, 1, 0, 2),
		gap:    small(), items: pick(r, "s", "c", "e", "t", "t"), self: pick(r, "-", "-", "-", "s", "c", "e", "t"),
		justify: pick(r, "s", "c", "e", "b", "a", "v"),
	}
	if leaf {
		s.width = length{1, float32(4 + r.IntN(90))}
		s.height = length{1, float32(4 + r.IntN(60))}
	}
	if r.IntN(3) == 0 {
		s.grow = pick[float32](r, 1, 2, 0.5)
	}
	if r.IntN(4) == 0 {
		s.basis = pick(r, length{1, float32(r.IntN(100))}, length{2, 40})
	}
	if s.grow > 0 && s.basis.unit == 0 {
		// MyGo's Grow sets an automatic basis to 0%, as CSS's flex: 1.
		s.basis = length{2, 0}
	}
	if r.IntN(2) == 0 {
		s.pad = [4]float32{small(), small(), small(), small()}
	}
	if r.IntN(2) == 0 {
		s.margin = [4]float32{small(), small(), small(), small()}
	}
	if !top && r.IntN(8) == 0 {
		s.absolute, s.left, s.top = true, float32(r.IntN(50)), float32(r.IntN(50))
	}
	return s
}

func (t tree) commands() string {
	var b strings.Builder
	fmt.Fprintf(&b, "tree %s %s\n", num(t.w), num(t.h))
	for i, s := range t.styles {
		fmt.Fprintf(&b, "node %d %d %s\n", i, t.parent[i], s.code())
	}
	return b.String()
}

var aligns = map[string]ui.Align{"s": ui.Start, "c": ui.Center, "e": ui.End, "t": ui.Stretch}

// myGoBoxes lays t out with MyGo's flexbox under ui.Tester and reads each
// box by its label.
func myGoBoxes(t tree) ([][4]float32, error) {
	var build func(c *ui.Context, parent int)
	build = func(c *ui.Context, parent int) {
		for i, p := range t.parent {
			if p != parent {
				continue
			}
			s := t.styles[i]
			e := ui.Box(c).Label("n" + strconv.Itoa(i))
			if s.row {
				e = e.Row()
			}
			// Grow first: it sets an automatic basis to 0%, even Grow(0),
			// and Basis then sets the basis asked for.
			if s.grow > 0 {
				e = e.Grow(s.grow)
			}
			e = e.Shrink(s.shrink)
			switch s.width.unit {
			case 1:
				e = e.Width(s.width.v)
			case 2:
				e = e.WidthPercent(s.width.v)
			}
			switch s.height.unit {
			case 1:
				e = e.Height(s.height.v)
			case 2:
				e = e.HeightPercent(s.height.v)
			}
			switch s.basis.unit {
			case 1:
				e = e.Basis(s.basis.v)
			case 2:
				e = e.BasisPercent(s.basis.v)
			}
			e = e.Padding(s.pad[:]...).Margin(s.margin[:]...).Gap(s.gap).AlignItems(aligns[s.items])
			if s.self != "-" {
				e = e.AlignSelf(aligns[s.self])
			}
			e = e.Justify(map[string]ui.Align{"s": ui.Start, "c": ui.Center, "e": ui.End, "b": ui.SpaceBetween, "a": ui.SpaceAround, "v": ui.SpaceEvenly}[s.justify])
			if s.absolute {
				e = e.Absolute().Left(s.left).Top(s.top)
			}
			e.Children(func() { build(c, i) })
		}
	}
	tt := ui.NewTester(func(c *ui.Context) { build(c, -1) }, int(t.w), int(t.h))
	out := make([][4]float32, len(t.styles))
	for i := range out {
		r, ok := tt.Find("n" + strconv.Itoa(i))
		if !ok {
			return nil, fmt.Errorf("MyGo shows no box labeled n%d", i)
		}
		out[i] = [4]float32{r.X, r.Y, r.W, r.H}
	}
	return out, nil
}

// layout is what the oracle printed for one layout command.
type layout struct {
	boxes [][4]float32
	work  int
}

var buildOracle = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp("", "layoutoracle")
	if err != nil {
		return "", err
	}
	ergo := filepath.Join(dir, "ergo")
	if out, err := bounded("go", "build", "-C", "../..", "-o", ergo, "./cmd/ergo").CombinedOutput(); err != nil {
		return "", fmt.Errorf("go build ergo: %v\n%s", err, out)
	}
	oracle := filepath.Join(dir, "oracle")
	if out, err := bounded(ergo, "build", "testdata/layoutoracle/main.ergo", "-o", oracle).CombinedOutput(); err != nil {
		return "", fmt.Errorf("ergo build oracle: %v\n%s", err, out)
	}
	return oracle, nil
})

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

// runOracle lays out the commands with std ui/layout.
func runOracle(t *testing.T, commands string) []layout {
	t.Helper()
	oracle, err := buildOracle()
	if err != nil {
		t.Fatal(err)
	}
	cmd := bounded(oracle)
	cmd.Stdin = strings.NewReader(commands)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("oracle: %v", err)
	}
	var got []layout
	cur := layout{}
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		switch {
		case len(f) == 6 && f[0] == "box":
			var b [4]float32
			for k := range b {
				v, err := strconv.ParseFloat(f[2+k], 32)
				if err != nil {
					t.Fatalf("oracle printed %q", sc.Text())
				}
				b[k] = float32(v)
			}
			cur.boxes = append(cur.boxes, b)
		case len(f) == 2 && f[0] == "work":
			cur.work, _ = strconv.Atoi(f[1])
		case len(f) == 1 && f[0] == "end":
			got = append(got, cur)
			cur = layout{}
		default:
			t.Fatalf("oracle printed %q", sc.Text())
		}
	}
	return got
}

// TestLayoutMatchesMyGo is H2L's acceptance: std ui/layout lays out
// generated box trees as MyGo's flexbox does under ui.Tester, every box
// within 0.01 DIP, at first and again after each of three style edits
// laid out on the boxes kept from before (so a change that does not reach
// the boxes it moves shows). It builds and runs a Go binary because the
// kernel is Ergo source: the oracle program is the only way to run it
// from this module, which keeps the Ergo module free of MyGo.
//
// A failing seed prints the oracle's input, both boxes, and the replay
// command.
func TestLayoutMatchesMyGo(t *testing.T) {
	seeds := []uint64{}
	if *layoutSeed != 0 {
		seeds = append(seeds, *layoutSeed)
	} else {
		for s := uint64(1); s <= 300; s++ {
			seeds = append(seeds, s)
		}
	}
	type step struct {
		seed  uint64
		input string // the commands up to this layout
		tree  tree
	}
	var steps []step
	var all strings.Builder
	for _, seed := range seeds {
		r := rand.New(rand.NewPCG(seed, 0x6c61796f7574))
		tr := genTree(r)
		input := tr.commands() + "layout\n"
		all.WriteString(input)
		steps = append(steps, step{seed, input, tr})
		for range 3 {
			k := r.IntN(len(tr.styles))
			kids := false
			for _, p := range tr.parent {
				kids = kids || p == k
			}
			tr.styles = append([]style(nil), tr.styles...)
			tr.styles[k] = genStyle(r, !kids, tr.parent[k] == -1)
			edit := fmt.Sprintf("set %d %s\nlayout\n", k, tr.styles[k].code())
			all.WriteString(edit)
			input += edit
			steps = append(steps, step{seed, input, tr})
		}
	}
	got := runOracle(t, all.String())
	if len(got) != len(steps) {
		t.Fatalf("the oracle laid out %d times, want %d", len(got), len(steps))
	}
	failed, compared, clipped := 0, 0, 0
	for i, st := range steps {
		want, err := myGoBoxes(st.tree)
		if err != nil {
			t.Fatal(err)
		}
		if len(got[i].boxes) != len(want) {
			t.Fatalf("seed %d: %d boxes, want %d", st.seed, len(got[i].boxes), len(want))
		}
		var diffs []string
		for k := range want {
			g := visible(got[i].boxes[k], st.tree.w, st.tree.h)
			if g != got[i].boxes[k] {
				clipped++
			}
			compared++
			for d := range 4 {
				if math.Abs(float64(g[d]-want[k][d])) > 0.01 {
					diffs = append(diffs, fmt.Sprintf("  n%d: ui/layout %v (shown %v), MyGo shows %v", k, got[i].boxes[k], g, want[k]))
					break
				}
			}
		}
		if len(diffs) > 0 {
			failed++
			if failed <= 3 {
				t.Errorf("seed %d: boxes differ\n%s\ninput:\n%s\nreplay: go test -run TestLayoutMatchesMyGo -layout.seed=%d", st.seed, strings.Join(diffs, "\n"), st.input, st.seed)
			}
		}
	}
	if failed > 3 {
		t.Errorf("%d more layouts differ", failed-3)
	}
	t.Logf("%d layouts, %d boxes compared, %d of them cut by the window", len(steps), compared, clipped)
}

// visible is the part of box b inside a w×h window, as MyGo's Find gives
// a box (intersect, ui/runtime.go): MyGo shows no more of a box, so the
// test compares what both show.
func visible(b [4]float32, w, h float32) [4]float32 {
	x0, y0 := max(b[0], 0), max(b[1], 0)
	x1, y1 := min(b[0]+b[2], w), min(b[1]+b[3], h)
	if x1 <= x0 || y1 <= y0 {
		return [4]float32{x0, y0, 0, 0}
	}
	return [4]float32{x0, y0, x1 - x0, y1 - y0}
}

// TestLayoutRelaysOutAlongThePath is H2L's growth guard: in a tree of N
// boxes, changing one leaf's size lays out again the boxes on its path to
// the root and no others, so the work (boxes computed, counted by
// layout.Work) grows with the depth, not with N. Its leaves start their
// parents' lines, so moving one moves no sibling's size.
func TestLayoutRelaysOutAlongThePath(t *testing.T) {
	for _, depth := range []int{2, 4, 6} {
		// A tree of fanout 3 with depth levels below the top box.
		tr := tree{w: 2000, h: 2000}
		var leaves []int
		var add func(parent, d int)
		add = func(parent, d int) {
			i := len(tr.parent)
			s := style{row: d%2 == 0, shrink: 0, items: "s", self: "-", justify: "s"}
			tr.parent = append(tr.parent, parent)
			tr.styles = append(tr.styles, s)
			if d == depth {
				tr.styles[i].width, tr.styles[i].height = length{1, 2}, length{1, 2}
				leaves = append(leaves, i)
				return
			}
			for range 3 {
				add(i, d+1)
			}
		}
		add(-1, 0)
		leaf := leaves[len(leaves)/2]
		s := tr.styles[leaf]
		s.width, s.height = length{1, 3}, length{1, 3}
		got := runOracle(t, tr.commands()+"layout\n"+fmt.Sprintf("set %d %s\nlayout\nlayout\n", leaf, s.code()))
		n := len(tr.parent) + 1 // and the window's root
		if got[0].work != n {
			t.Errorf("depth %d: the first layout computed %d boxes, want all %d", depth, got[0].work, n)
		}
		// The leaf, its depth ancestors and the root.
		if got[1].work != depth+2 {
			t.Errorf("depth %d, %d boxes: a leaf's change computed %d boxes, want the %d on its path", depth, n, got[1].work, depth+2)
		}
		if got[2].work != 0 {
			t.Errorf("depth %d: a layout with no change computed %d boxes", depth, got[2].work)
		}
	}
}
