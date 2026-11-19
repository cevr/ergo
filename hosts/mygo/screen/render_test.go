package screen

import (
	"errors"
	"flag"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/egoist/mygo/drawing/platform"
	"github.com/egoist/mygo/drawing/scene"
	"github.com/egoist/mygo/drawing/surface"
	"github.com/egoist/mygo/drawing/text"
)

// testSurface is a surface of a size whose native objects are native (a
// handle, or none) and whose PresentPixels keeps the last frame drawn in
// memory. What else a surface does is not called.
type testSurface struct {
	platform.Surface
	native         platform.SurfaceNative
	width, height  float64
	scale          float64
	presents       int
	pix            []byte
	stride, pw, ph int
}

func (s *testSurface) Native() platform.SurfaceNative           { return s.native }
func (s *testSurface) Size() (float64, float64, float64)        { return s.width, s.height, s.scale }
func (s *testSurface) RequestFrame()                            {}
func (s *testSurface) SetTextInput(platform.TextInputState)     {}
func (s *testSurface) UpdateAccessibility(*platform.AccessTree) {}
func (s *testSurface) PresentPixels(pix []byte, stride, w, h int) {
	s.presents++
	s.pix, s.stride, s.pw, s.ph = append(s.pix[:0], pix...), stride, w, h
}

// testGPU is a GPU renderer that keeps the scenes it was given, and fails
// while fail is set.
type testGPU struct {
	fail     bool
	ops      []int
	released int
}

func (g *testGPU) Render(s *scene.Scene) error {
	if g.fail {
		return errors.New("the device went away")
	}
	g.ops = append(g.ops, len(s.Ops))
	return nil
}

func (g *testGPU) Release() { g.released++ }

// withGPU has the surfaces' GPU renderers made by make while t runs.
func withGPU(t *testing.T, make func(platform.SurfaceNative) (gpuRenderer, error)) {
	t.Setenv("MYGO_GPU", "")
	newGPU = make
	t.Cleanup(func() { newGPU = newGPURenderer })
}

// attached is a window on surface s, as MyGo attaches its content, with a
// text system of its own: the shared one's atlases would carry its masks
// and glyphs into the next test (TestStrokeMaskIsKept counts masks).
func attached(s *testSurface) *Window {
	w := newWindow(s.width, s.height)
	w.sys = text.New()
	w.AttachContent(&surface.Conn{Surface: s})
	return w
}

// surfaceFrame is the surface asking w for a frame.
func surfaceFrame(w *Window) { w.event(platform.SurfaceEvent{Kind: platform.SurfaceFrame}) }

// TestFrameNeedsNoPixelPresent draws frames on a surface with a native
// handle, as a phone's (whose PresentPixels shows nothing): the GPU
// renderer made for the handle draws each frame and the surface is given
// no pixels. A renderer that fails gives way to the CPU's, and a renewed
// surface (a phone app back from the background) gets a new renderer.
//
// Mutants it kills (testdata/mutants.json): frames drawn in memory when
// the surface has a handle; a failing renderer kept, so nothing shows;
// SurfaceRenew not making the renderer again.
func TestFrameNeedsNoPixelPresent(t *testing.T) {
	var made []*testGPU
	withGPU(t, func(n platform.SurfaceNative) (gpuRenderer, error) {
		if n.Window == 0 {
			t.Fatalf("a renderer made for %+v", n)
		}
		g := &testGPU{}
		made = append(made, g)
		return g, nil
	})
	s := &testSurface{native: platform.SurfaceNative{Window: 7}, width: 100, height: 80, scale: 2}
	w := attached(s)
	w.Begin()
	w.Fill(10, 10, 40, 30, 4, 0x3366ccff)
	w.Present()
	surfaceFrame(w)
	surfaceFrame(w)
	if s.presents != 0 || len(made) != 1 || len(made[0].ops) != 2 || made[0].ops[0] == 0 {
		t.Fatalf("after two frames: %d pixel presents, %d renderers, scenes %v; want 0, 1 and two scenes with the fill", s.presents, len(made), opsOf(made))
	}
	made[0].fail = true
	surfaceFrame(w)
	if s.presents != 1 || made[0].released != 1 {
		t.Fatalf("a frame the renderer failed: %d pixel presents, renderer released %d times; want 1 and 1", s.presents, made[0].released)
	}
	surfaceFrame(w)
	if s.presents != 2 || len(made) != 1 {
		t.Fatalf("the frame after a failure: %d pixel presents, %d renderers; want 2 and 1 (in memory until the surface renews)", s.presents, len(made))
	}
	w.event(platform.SurfaceEvent{Kind: platform.SurfaceRenew})
	surfaceFrame(w)
	if s.presents != 2 || len(made) != 2 || len(made[1].ops) != 1 {
		t.Fatalf("after SurfaceRenew: %d pixel presents, %d renderers; want 2 and 2, the new one drawing the frame", s.presents, len(made))
	}
}

func opsOf(gs []*testGPU) [][]int {
	var out [][]int
	for _, g := range gs {
		out = append(out, g.ops)
	}
	return out
}

var renderersSeed = flag.Uint64("renderers.seed", 0, "run TestRenderersAgree on this seed only")

// paintCall is one call of a generated paint list.
type paintCall struct {
	kind                      string // fill, border, span, line, clip, unclip, text
	x, y, w, h, radius, width float64
	color                     uint32
	s                         string
}

func (p paintCall) String() string {
	switch p.kind {
	case "unclip":
		return "unclip"
	case "text":
		return fmt.Sprintf("text %q at %g,%g size %g color %08x", p.s, p.x, p.y, p.h, p.color)
	case "line":
		return fmt.Sprintf("line %g,%g to %g,%g width %g color %08x", p.x, p.y, p.w, p.h, p.width, p.color)
	}
	return fmt.Sprintf("%s %g,%g %gx%g radius %g line %g color %08x", p.kind, p.x, p.y, p.w, p.h, p.radius, p.width, p.color)
}

// genPaints is the paint list of a seed, on a window of width by height
// DIPs: fills, borders, canvas spans (a fill one DIP high), lines, clips
// and lines of text, on whole DIPs.
func genPaints(seed uint64, width, height int) []paintCall {
	r := rand.New(rand.NewPCG(seed, 0x9e3779b97f4a7c15))
	colors := []uint32{0x1f6febff, 0xd73a49ff, 0x2da44eff, 0xfbca04ff, 0x24292fff, 0x8250dfff, 0xffffffff, 0x80808080}
	words := []string{"Save", "todo", "Ergo", "a list", "Done 3/7", "ünïcödé", "WWW"}
	var ps []paintCall
	clips := 0
	for range 6 + r.IntN(14) {
		p := paintCall{color: colors[r.IntN(len(colors))]}
		p.x, p.y, p.w, p.h = boxIn(r, width, height)
		switch k := r.IntN(10); {
		case k < 3:
			p.kind, p.radius = "fill", float64(r.IntN(3)*3)
		case k < 4:
			p.kind, p.radius, p.width = "border", float64(r.IntN(3)*2), float64(1+r.IntN(3))
		case k < 5:
			p.kind, p.h = "span", 1
		case k < 6:
			p.kind, p.width = "line", float64(1+r.IntN(4))
			p.w, p.h = p.x+p.w, p.y+p.h // the end point
		case k < 7:
			p.kind = "clip"
			clips++
		case k < 8 && clips > 0:
			p.kind = "unclip"
			clips--
		default:
			p.kind, p.s, p.h = "text", words[r.IntN(len(words))], float64(12+2*r.IntN(5))
			p.color = colors[r.IntN(2)*4]
		}
		ps = append(ps, p)
	}
	return ps
}

// boxIn is a box of whole DIPs inside the window.
func boxIn(r *rand.Rand, width, height int) (x, y, w, h float64) {
	w = float64(4 + r.IntN(width/2))
	h = float64(4 + r.IntN(height/2))
	x = float64(r.IntN(width - int(w)))
	y = float64(r.IntN(height - int(h)))
	return
}

// paintOn gives w the list ps, as the desktop driver does, and presents it.
func paintOn(w *Window, ps []paintCall) {
	w.Begin()
	for _, p := range ps {
		switch p.kind {
		case "fill", "span":
			w.Fill(p.x, p.y, p.w, p.h, p.radius, p.color)
		case "border":
			w.Border(p.x, p.y, p.w, p.h, p.radius, p.width, p.color)
		case "line":
			w.Line(p.x, p.y, p.w, p.h, p.width, p.color)
		case "clip":
			w.Clip(p.x, p.y, p.w, p.h)
		case "unclip":
			w.Unclip()
		case "text":
			w.Text(p.x, p.y, p.s, p.h, p.color)
		}
	}
	w.Present()
}

// TestRenderersAgree draws seeded paint lists (fills, borders, clips,
// canvas spans, lines and lines of text) through a window's frame twice:
// on a surface with no native handle, by the CPU renderer whose pixels
// the surface presents, and on one with a handle, by the platform's GPU
// renderer drawing offscreen (OpenGL through Mesa's surfaceless EGL on
// Linux, Metal on a Mac). At the centre of each fill and span, and at its
// corners inset by one DIP (and out of its rounding), both frames have
// the same color within 3 of 255 a channel, and each line of text has the
// same ink: the mean of its box within 2. A failing seed prints its list
// and both pixels; -renderers.seed=N replays it. On a host with no GPU
// context it skips with the reason, and the Mac leg must show it ran.
//
// Both renderers draw the one scene the frame makes, so a fault of this
// package shows on both. What this test catches is a pinned MyGo whose
// GPU path draws our lists otherwise than its CPU renderer. Only the GPU
// path draws on phones, and every other host test draws on the CPU.
// Mutant it kills: MyGo's GPU path keeping every clip pushed
// (internal/gpu/gpu.go, the OpPopClip case), applied to a copy of the
// pinned module by a replace. The ledger takes edits of this repository
// only, so the row is not in testdata/mutants.json.
func TestRenderersAgree(t *testing.T) {
	const width, height = 240, 180
	seeds := []uint64{*renderersSeed}
	if *renderersSeed == 0 {
		seeds = seeds[:0]
		for s := uint64(1); s <= 40; s++ {
			seeds = append(seeds, s)
		}
	}
	for _, seed := range seeds {
		scale := float64(1 + seed%2)
		ps := genPaints(seed, width, height)
		draw := func(native platform.SurfaceNative) *testSurface {
			s := &testSurface{native: native, width: width, height: height, scale: scale}
			w := attached(s)
			paintOn(w, ps)
			surfaceFrame(w)
			return s
		}
		pw, ph := int(width*scale), int(height*scale)
		cpu := draw(platform.SurfaceNative{}).pix
		got := gpuFrame(t, pw, ph, func() {
			if s := draw(platform.SurfaceNative{Window: 1, Layer: 1, GLArea: 1}); s.presents != 0 {
				t.Fatalf("seed %d: the frame on a surface with a handle was drawn in memory", seed)
			}
		})
		if len(cpu) != pw*ph*4 || len(got) != pw*ph*4 {
			t.Fatalf("seed %d: frames of %d and %d bytes, want %d", seed, len(cpu), len(got), pw*ph*4)
		}
		at := func(pix []byte, x, y float64) [4]byte {
			px, py := min(int(x*scale), pw-1), min(int(y*scale), ph-1)
			var c [4]byte
			copy(c[:], pix[(py*pw+px)*4:])
			return c
		}
		fail := func(what string, a, b any) {
			var list []string
			for _, p := range ps {
				list = append(list, "  "+p.String())
			}
			t.Fatalf("seed %d (scale %g): %s: CPU %v, GPU %v\nlist:\n%s\nreplay: go test -run TestRenderersAgree -renderers.seed=%d ./screen", seed, scale, what, a, b, strings.Join(list, "\n"), seed)
		}
		for _, p := range ps {
			switch p.kind {
			case "fill", "span":
				in := 1 + 0.3*p.radius
				pts := [][2]float64{{p.x + p.w/2, p.y + p.h/2}}
				if p.w > 2*in+1 && p.h > 2*in+1 {
					pts = append(pts, [2]float64{p.x + in, p.y + in}, [2]float64{p.x + p.w - in, p.y + in},
						[2]float64{p.x + in, p.y + p.h - in}, [2]float64{p.x + p.w - in, p.y + p.h - in})
				}
				for _, pt := range pts {
					a, b := at(cpu, pt[0], pt[1]), at(got, pt[0], pt[1])
					if channelDiff(a, b) > 3 {
						fail(fmt.Sprintf("%s, pixel at %g,%g", p, pt[0], pt[1]), a, b)
					}
				}
			case "text":
				a, b := mean(cpu, pw, p, scale), mean(got, pw, p, scale)
				if d := a - b; d > 2 || d < -2 {
					fail(fmt.Sprintf("%s, the mean of its box", p), a, b)
				}
			}
		}
	}
}

func channelDiff(a, b [4]byte) int {
	d := 0
	for i := range 4 {
		d = max(d, int(a[i])-int(b[i]), int(b[i])-int(a[i]))
	}
	return d
}

// mean is the mean of the channels of the pixels of a text's box: its
// width a DIP a rune at most, its line's height.
func mean(pix []byte, pw int, p paintCall, scale float64) float64 {
	x0, y0 := int(p.x*scale), int(p.y*scale)
	x1 := min(int((p.x+p.h*float64(len([]rune(p.s))))*scale), pw)
	y1 := min(int((p.y+p.h*1.2)*scale), len(pix)/4/pw)
	sum, n := 0, 0
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			for _, c := range pix[(y*pw+x)*4:][:4] {
				sum += int(c)
			}
			n += 4
		}
	}
	if n == 0 {
		return 0
	}
	return float64(sum) / float64(n)
}
