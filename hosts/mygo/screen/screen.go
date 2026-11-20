// Package screen is the Go half of the MyGo desktop host: a window that
// MyGo drives through its drawing layer, as plain values an Ergo program
// can take through import go. The program lays out, hit tests and paints
// (Ergo package desktop); screen turns the window's surface events into
// Inputs, and turns the program's paint calls into MyGo's display list,
// drawn by the GPU renderer MyGo has for the surface (Metal, OpenGL or
// OpenGL ES, render.go) or by its CPU renderer where there is none. It
// uses no MyGo widget.
//
// Threading follows MyGo's rules (v1-gui plan §1.3): the surface's events
// and frames arrive on the main thread, which only queues Inputs and
// draws the last list Present gave; the program's task calls Next, the
// paint calls and Present from any goroutine.
package screen

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/maphash"
	"math"
	"reflect"
	"slices"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/drawing/fake"
	"github.com/egoist/mygo/drawing/platform"
	"github.com/egoist/mygo/drawing/raster"
	"github.com/egoist/mygo/drawing/scene"
	"github.com/egoist/mygo/drawing/surface"
	"github.com/egoist/mygo/drawing/text"
	"github.com/egoist/mygo/drawing/vec"
)

// The kinds of an Input.
const (
	// Closed: the window closed; no Input follows.
	Closed = iota
	// Down, Move and Up: the primary pointer at X, Y.
	Down
	Move
	Up
	// Scroll: a wheel or touchpad scrolled by DX, DY at X, Y.
	Scroll
	// Key: the key named Key pressed (as the web's KeyboardEvent.key
	// names it), with the modifiers.
	Key
	// Text: Text typed or committed by an input method.
	Text
	// Resize: the window's content is now Width by Height.
	Resize
	// Frame: a frame at At nanoseconds since the window opened, sent
	// while the program asks for frames.
	Frame
	// Woken: Wake was called; the program has work of its own queued.
	Woken
)

// Input is one event of the window, in device-independent pixels from
// the content's top left corner.
type Input struct {
	Kind                   int
	X, Y, DX, DY           float64
	Key, Text              string
	Shift, Ctrl, Alt, Meta bool
	Width, Height          float64
	At                     int64
}

// The roles of Access.
const (
	RoleGroup = iota
	RoleText
	RoleButton
	RoleCheck
	RoleSwitch
	RoleField
)

// Window is a window whose content the program draws.
type Window struct {
	mu      sync.Mutex
	wake    chan struct{}
	inputs  []Input
	closed  bool
	pending []scene.Op
	glyphs  []glyphText
	clips   int
	shown   []scene.Op
	texts   []glyphText
	access  []platform.AccessNode
	focus   uint64
	field   platform.TextInputState

	// The strokes of the list being made and of the shown one (a stroke's
	// op is a placeholder that draw turns into its mask) and their points
	// (each list's arena, which its strokes index), the one StrokeBegin
	// opened (inStroke), how many masks draw rasterized, and how many
	// strokes it drew as Lines since the atlas left their mask out.
	strokes, shownStrokes, refStrokes []stroke
	pts, shownPts, refPts             []float32
	drawing                           stroke
	inStroke                          bool
	// published is the accessibility tree last given the surface, and
	// publishedFocus its focus; pix, q and disc rasterize's buffers.
	published      []platform.AccessNode
	publishedFocus uint64
	pix            []byte
	q              []float32
	disc           [][2]float32
	masks, lined   int
	// vw and vh are the frame being built's size in device pixels.
	vw, vh  float32
	z       vec.Rasterizer
	frames  bool
	start   time.Time
	width   float64
	height  float64
	painted int
	// opacity multiplies the alpha of what is painted from now on
	// (SetOpacity); 1 at Begin.
	opacity float64

	// The damage paint (Mark, Seal, Again): the parts of the list being
	// made and of the shown one each key drew, the parts still open, and
	// the shown list's accessibility nodes. ref is the list Hold put
	// aside, which Differs holds the list made since to.
	segs, prev  map[int]seg
	open        []seg
	shownAccess []platform.AccessNode
	refOps      []scene.Op
	refGlyphs   []glyphText
	refAccess   []platform.AccessNode

	conn *surface.Conn
	r    raster.Renderer
	sc   scene.Scene
	sys  *text.System
	// gpu is the surface's GPU renderer, nil where frames draw in memory;
	// gpuTried tells that the surface's native objects were looked at.
	// The main thread owns both.
	gpu      gpuRenderer
	gpuTried bool

	// laid and lastLaid are the layouts of text the list being made and
	// the one before used, by text and size, under tmu: a text the next
	// list uses too is not shaped again, and one it does not use goes.
	tmu            sync.Mutex
	laid, lastLaid map[textKey]*text.Layout

	// fake is the surface of a Headless window.
	fake *fake.Surface
	// app quits when a desktop window closes.
	desktop bool
}

// glyphText is a line of text Present draws: at op index at of the list.
type glyphText struct {
	at          int
	x, y        float32
	s           string
	size        float32
	color       scene.Color
	clip        scene.Rect
	transformed bool
}

// seg is the part of a list one key drew: its ops, its lines of text and
// its accessibility nodes, each from the first index to the one past the
// last.
type seg struct {
	key                int
	op0, op1, gl0, gl1 int
	ac0, ac1, st0, st1 int
}

// stroke is a polyline Present draws as one mask, at op index at of the
// list (its placeholder): its points in DIPs, x then y, at [p0, p1) of
// its list's arena, its width and its color.
type stroke struct {
	at, p0, p1 int
	width      float32
	color      scene.Color
}

// maskSeed keys the masks of strokes: one process, one seed.
var maskSeed = maphash.MakeSeed()

func newWindow(width, height float64) *Window {
	w := &Window{wake: make(chan struct{}, 1), start: time.Now(), width: width, height: height, sys: text.Shared(), segs: map[int]seg{}, prev: map[int]seg{}, opacity: 1}
	fastPango(func() { w.sys.Metrics(text.Style{Size: 13}) })
	return w
}

// Open opens a desktop window of the size, titled title, once MyGo's app
// is ready. Run then runs MyGo's loop on the calling thread, which must
// be the main thread (Ergo's main runs on it).
func Open(title string, width, height float64) (*Window, error) {
	if width <= 0 || height <= 0 {
		return nil, errors.New("screen: a window needs a positive size")
	}
	w := newWindow(width, height)
	w.desktop = true
	mygo.App.WhenReady(func() {
		mw := mygo.NewWindow(mygo.WindowOptions{Title: title, Width: int(width), Height: int(height), Content: w})
		mw.OnClosed(w.Closing)
	})
	return w, nil
}

// Run runs MyGo's loop until the app quits: the last window closed, or
// Quit. Call it from the main thread, after Open.
func (w *Window) Run() error { return mygo.App.Run() }

// Quit ends Run.
func (w *Window) Quit() {
	if w.desktop {
		mygo.App.Quit()
	}
}

// Headless is a window of MyGo's fake backend: no display, no thread
// rule. A test sends it input (Press, Release, Type, PressKey) and reads
// its pixels after Render.
func Headless(width, height float64) (*Window, error) {
	if width <= 0 || height <= 0 {
		return nil, errors.New("screen: a window needs a positive size")
	}
	w := newWindow(width, height)
	b := fake.New()
	h := &handler{w: w}
	nw, err := b.NewWindow(&platform.WindowOptions{Surface: true, Width: int(width), Height: int(height)}, h)
	if err != nil {
		return nil, err
	}
	fw := nw.(*fake.Window)
	conn := &surface.Conn{Surface: fw.Surface()}
	conn.Invalidate = conn.Surface.RequestFrame
	conn.Post = func(fn func()) { fn() }
	w.fake = fw.FakeSurface()
	w.AttachContent(conn)
	return w, nil
}

// handler hands a fake window's surface events to the content.
type handler struct {
	platform.WindowHandler
	w *Window
}

func (h *handler) SurfaceEvent(ev platform.SurfaceEvent) bool { return h.w.event(ev) }

// AttachContent connects the window to its surface: it makes Window a
// mygo.Content. MyGo calls it on the main thread.
func (w *Window) AttachContent(conn *surface.Conn) {
	w.conn = conn
	conn.Event = w.event
	conn.Surface.RequestFrame()
}

var _ mygo.Content = (*Window)(nil)

// event takes one surface event, on the main thread.
func (w *Window) event(ev platform.SurfaceEvent) bool {
	switch ev.Kind {
	case platform.SurfaceFrame:
		w.draw()
		w.mu.Lock()
		on := w.frames
		w.mu.Unlock()
		if on {
			w.push(Input{Kind: Frame, At: time.Since(w.start).Nanoseconds()})
		}
	case platform.SurfaceRenew, platform.SurfaceLowMemory:
		// The surface's native objects are new (a phone app back from the
		// background), or the system wants memory back: the next frame
		// makes the renderer again, with its textures and atlases.
		w.dropGPU()
		w.conn.Surface.RequestFrame()
	case platform.SurfaceResize:
		width, height, _ := w.conn.Surface.Size()
		w.mu.Lock()
		w.width, w.height = width, height
		w.mu.Unlock()
		w.push(Input{Kind: Resize, Width: width, Height: height})
	case platform.PointerDown, platform.PointerMove, platform.PointerUp:
		if ev.Button != 0 {
			return false
		}
		kind := map[platform.SurfaceEventKind]int{platform.PointerDown: Down, platform.PointerMove: Move, platform.PointerUp: Up}[ev.Kind]
		w.push(withMods(Input{Kind: kind, X: ev.X, Y: ev.Y}, ev.Mods))
	case platform.PointerScroll:
		w.push(withMods(Input{Kind: Scroll, X: ev.X, Y: ev.Y, DX: ev.DX, DY: ev.DY}, ev.Mods))
	case platform.KeyPressed:
		name := keyName(ev.Key)
		if name == "" && ev.Mods&(platform.ModCtrl|platform.ModAlt|platform.ModSuper) != 0 {
			// A letter or a digit with Ctrl, Alt or the system key is a
			// shortcut, which types nothing: the program gets the key.
			name = chordName(ev.Key)
		}
		if name == "" {
			return false
		}
		w.push(withMods(Input{Kind: Key, Key: name}, ev.Mods))
		return true
	case platform.TextInput:
		if ev.Text == "" {
			return false
		}
		w.push(Input{Kind: Text, Text: ev.Text})
		return true
	case platform.AccessibilityOn:
		w.mu.Lock()
		w.publishAccess(true)
		w.mu.Unlock()
	}
	return false
}

// Wake makes Next return a Woken input, from any goroutine: a task of the
// program that queued work for the one waiting in Next calls it.
func (w *Window) Wake() {
	w.push(Input{Kind: Woken})
}

// Closing tells the program the window closed: a desktop window's
// handler calls it, and a test may.
func (w *Window) Closing() {
	w.push(Input{Kind: Closed})
}

func withMods(in Input, m platform.Modifiers) Input {
	in.Shift, in.Ctrl, in.Alt, in.Meta = m&platform.ModShift != 0, m&platform.ModCtrl != 0, m&platform.ModAlt != 0, m&platform.ModSuper != 0
	return in
}

// keyName is the web's name of the keys a program handles; "" for the
// rest, which a TextInput types.
func keyName(k platform.Key) string {
	switch k {
	case platform.KeyEnter:
		return "Enter"
	case platform.KeyEscape:
		return "Escape"
	case platform.KeyBackspace:
		return "Backspace"
	case platform.KeyTab:
		return "Tab"
	case platform.KeySpace:
		return " "
	case platform.KeyDelete:
		return "Delete"
	case platform.KeyHome:
		return "Home"
	case platform.KeyEnd:
		return "End"
	case platform.KeyPageUp:
		return "PageUp"
	case platform.KeyPageDown:
		return "PageDown"
	case platform.KeyLeft:
		return "ArrowLeft"
	case platform.KeyRight:
		return "ArrowRight"
	case platform.KeyUp:
		return "ArrowUp"
	case platform.KeyDown:
		return "ArrowDown"
	}
	return ""
}

// chordName is the name of a letter or digit key in a shortcut, its
// character in lower case ("z", "5"); "" for the other keys.
func chordName(k platform.Key) string {
	switch {
	case k >= platform.KeyA && k <= platform.KeyZ:
		return string(rune('a' + int(k-platform.KeyA)))
	case k >= platform.Key0 && k <= platform.Key9:
		return string(rune('0' + int(k-platform.Key0)))
	}
	return ""
}

// push queues an input for Next and wakes it.
func (w *Window) push(in Input) {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	if in.Kind == Closed {
		w.closed = true
	}
	w.inputs = append(w.inputs, in)
	w.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Next waits for the next input, in the order they came. The Closed input
// comes last. It fails when ctx is done (the program's task stopped).
func (w *Window) Next(ctx context.Context) (Input, error) {
	for {
		if in, ok := w.Poll(); ok {
			return in, nil
		}
		select {
		case <-w.wake:
		case <-ctx.Done():
			return Input{}, ctx.Err()
		}
	}
}

// Poll takes the next input when one is queued, without waiting.
func (w *Window) Poll() (Input, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.inputs) == 0 {
		return Input{}, false
	}
	in := w.inputs[0]
	w.inputs = w.inputs[1:]
	return in, true
}

// Size is the content's width and height.
func (w *Window) Size() (float64, float64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.width, w.height
}

// Frames asks for a Frame input at each frame (on), or for none.
func (w *Window) Frames(on bool) {
	w.mu.Lock()
	w.frames = on
	w.mu.Unlock()
	if on {
		w.invalidate()
	}
}

func (w *Window) invalidate() {
	if w.conn != nil && w.conn.Invalidate != nil {
		w.conn.Invalidate()
	}
}

// rgba is a color of 0xRRGGBBAA.
func rgba(c uint32) scene.Color {
	return scene.Color{R: uint8(c >> 24), G: uint8(c >> 16), B: uint8(c >> 8), A: uint8(c)}
}

func rect(x, y, width, height float64) scene.Rect {
	return scene.Rect{X: float32(x), Y: float32(y), W: float32(width), H: float32(height)}
}

// Begin starts the next display list; Present shows it.
func (w *Window) Begin() {
	w.mu.Lock()
	w.begin()
	w.mu.Unlock()
}

// begin is Begin with w.mu held.
func (w *Window) begin() {
	w.pending, w.glyphs, w.clips, w.access = w.pending[:0], w.glyphs[:0], 0, w.access[:0]
	w.strokes, w.pts, w.inStroke = w.strokes[:0], w.pts[:0], false
	w.focus, w.field = 0, platform.TextInputState{}
	w.opacity = 1
	clear(w.segs)
	w.open = w.open[:0]
}

// Mark starts the part of the list that key draws; Seal ends it. Marks
// nest as the tree does. The next list may take the part again (Again).
func (w *Window) Mark(key int) {
	w.mu.Lock()
	w.open = append(w.open, seg{key: key, op0: len(w.pending), gl0: len(w.glyphs), ac0: len(w.access), st0: len(w.strokes)})
	w.mu.Unlock()
}

// Seal ends the part of the list key's Mark started.
func (w *Window) Seal(key int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(w.open) - 1
	if n < 0 || w.open[n].key != key {
		return
	}
	s := w.open[n]
	w.open = w.open[:n]
	s.op1, s.gl1, s.ac1, s.st1 = len(w.pending), len(w.glyphs), len(w.access), len(w.strokes)
	w.segs[key] = s
}

// Again draws again what key drew in the list shown now, as it was, and
// reports whether there was such a part: the damage paint's way to leave a
// box whose inputs did not change as it is.
func (w *Window) Again(key int) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	s, ok := w.prev[key]
	if !ok {
		return false
	}
	t := seg{key: key, op0: len(w.pending), gl0: len(w.glyphs), ac0: len(w.access), st0: len(w.strokes)}
	w.pending = append(w.pending, w.shown[s.op0:s.op1]...)
	for _, g := range w.texts[s.gl0:s.gl1] {
		g.at += t.op0 - s.op0
		w.glyphs = append(w.glyphs, g)
	}
	for _, k := range w.shownStrokes[s.st0:s.st1] {
		k.at += t.op0 - s.op0
		p0 := len(w.pts)
		w.pts = append(w.pts, w.shownPts[k.p0:k.p1]...)
		k.p0, k.p1 = p0, len(w.pts)
		w.strokes = append(w.strokes, k)
	}
	w.access = append(w.access, w.shownAccess[s.ac0:s.ac1]...)
	t.op1, t.gl1, t.ac1, t.st1 = len(w.pending), len(w.glyphs), len(w.access), len(w.strokes)
	w.segs[key] = t
	return true
}

// Hold puts the list made since Begin aside as the reference and begins
// another; Differs then compares the two.
func (w *Window) Hold() {
	w.mu.Lock()
	w.refOps = append(w.refOps[:0], w.pending...)
	w.refGlyphs = append(w.refGlyphs[:0], w.glyphs...)
	w.refAccess = append(w.refAccess[:0], w.access...)
	w.refStrokes = append(w.refStrokes[:0], w.strokes...)
	w.refPts = append(w.refPts[:0], w.pts...)
	w.begin()
	w.mu.Unlock()
}

// Differs is where the list made since Hold differs from the one Hold put
// aside, "" when they are the same.
func (w *Window) Differs() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.pending) != len(w.refOps) {
		return fmt.Sprintf("%d ops, the reference %d", len(w.pending), len(w.refOps))
	}
	for i := range w.pending {
		if w.pending[i] != w.refOps[i] {
			return fmt.Sprintf("op %d is %+v, the reference's %+v", i, w.pending[i], w.refOps[i])
		}
	}
	if len(w.glyphs) != len(w.refGlyphs) {
		return fmt.Sprintf("%d lines of text, the reference %d", len(w.glyphs), len(w.refGlyphs))
	}
	for i := range w.glyphs {
		if w.glyphs[i] != w.refGlyphs[i] {
			return fmt.Sprintf("text %d is %+v, the reference's %+v", i, w.glyphs[i], w.refGlyphs[i])
		}
	}
	if len(w.strokes) != len(w.refStrokes) {
		return fmt.Sprintf("%d strokes, the reference %d", len(w.strokes), len(w.refStrokes))
	}
	for i, k := range w.strokes {
		r := w.refStrokes[i]
		if k.at != r.at || k.width != r.width || k.color != r.color || !slices.Equal(w.pts[k.p0:k.p1], w.refPts[r.p0:r.p1]) {
			return fmt.Sprintf("stroke %d is %+v %v, the reference's %+v %v", i, k, w.pts[k.p0:k.p1], r, w.refPts[r.p0:r.p1])
		}
	}
	if len(w.access) != len(w.refAccess) {
		return fmt.Sprintf("%d accessibility nodes, the reference %d", len(w.access), len(w.refAccess))
	}
	for i := range w.access {
		if !reflect.DeepEqual(w.access[i], w.refAccess[i]) {
			return fmt.Sprintf("accessibility node %d is %+v, the reference's %+v", i, w.access[i], w.refAccess[i])
		}
	}
	return ""
}

// Fill paints a rectangle of color 0xRRGGBBAA with corners of radius.
func (w *Window) Fill(x, y, width, height, radius float64, color uint32) {
	w.mu.Lock()
	w.add(scene.Op{Kind: scene.OpFill, Rect: rect(x, y, width, height), Transform: scene.Translation(0, 0), Radii: scene.Uniform(float32(radius)), Color: rgba(color)})
	w.mu.Unlock()
}

// Border paints a border of width line inside a rectangle.
func (w *Window) Border(x, y, width, height, radius, line float64, color uint32) {
	w.mu.Lock()
	w.add(scene.Op{Kind: scene.OpFill, Rect: rect(x, y, width, height), Transform: scene.Translation(0, 0), Radii: scene.Uniform(float32(radius)), Border: scene.Uniform(float32(line)), BorderColor: rgba(color)})
	w.mu.Unlock()
}

// Line paints the segment from x0, y0 to x1, y1, width wide, with round
// caps, in color 0xRRGGBBAA: a rounded rectangle turned onto the segment,
// as MyGo strokes an opaque polyline on a GPU. A canvas's stroke is these
// segments, one a line of its flattened path.
func (w *Window) Line(x0, y0, x1, y1, width float64, color uint32) {
	if width <= 0 || math.IsNaN(width) {
		return
	}
	dx, dy := x1-x0, y1-y0
	length := math.Hypot(dx, dy)
	if math.IsNaN(length) || math.IsInf(length, 0) {
		return
	}
	w.mu.Lock()
	w.add(lineOp(x0, y0, x1, y1, width, rgba(color)))
	w.mu.Unlock()
}

// lineOp is Line's op: the segment's rounded rectangle, turned onto it.
func lineOp(x0, y0, x1, y1, width float64, color scene.Color) scene.Op {
	dx, dy := x1-x0, y1-y0
	length := math.Hypot(dx, dy)
	m := scene.Translation(float32(x0), float32(y0))
	if length > 0 {
		dx, dy = dx/length, dy/length
		m = scene.Matrix(float32(dx), float32(dy), float32(-dy), float32(dx), float32(x0), float32(y0))
	}
	hw := width / 2
	return scene.Op{Kind: scene.OpFill, Rect: rect(-hw, -hw, length+width, width), Transform: m, Radii: scene.Uniform(float32(hw)), Color: color}
}

// StrokeBegin opens a polyline width wide, with round caps and joins, in
// color 0xRRGGBBAA; StrokeTo adds its points and StrokeEnd draws it. It
// paints what a Line a segment paints, but the frame rasterizes the whole
// polyline once into a mask the text system's atlas keeps, keyed by its
// points relative to the pixel grid, so a stroke whose points do not
// change is not rasterized again (MasksDrawn), as MyGo's
// Painter.StrokePath draws.
func (w *Window) StrokeBegin(width float64, color uint32) {
	w.mu.Lock()
	w.drawing, w.inStroke = stroke{width: float32(width), color: w.faded(rgba(color)), p0: len(w.pts)}, true
	w.mu.Unlock()
}

// StrokeTo adds the point x, y to the polyline StrokeBegin opened.
func (w *Window) StrokeTo(x, y float64) {
	w.mu.Lock()
	if w.inStroke {
		w.pts = append(w.pts, float32(x), float32(y))
	}
	w.mu.Unlock()
}

// StrokeEnd ends the polyline and puts it in the list, where an empty
// fill holds its place; one of no point, or of no width, draws nothing.
func (w *Window) StrokeEnd() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.inStroke {
		return
	}
	k := w.drawing
	w.inStroke = false
	k.p1 = len(w.pts)
	if k.p1 == k.p0 || !(k.width > 0) {
		w.pts = w.pts[:k.p0]
		return
	}
	k.at = len(w.pending)
	w.pending = append(w.pending, scene.Op{Kind: scene.OpFill, Transform: scene.Translation(0, 0)})
	w.strokes = append(w.strokes, k)
}

// MasksDrawn is how many stroke masks the frames drawn so far
// rasterized: a stroke drawn again with the same points takes its mask
// from the atlas.
func (w *Window) MasksDrawn() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.masks
}

// add appends a painting op under the opacity in effect; w.mu is held.
func (w *Window) add(o scene.Op) {
	if w.opacity < 1 {
		o.Opacity = float32(max(w.opacity, 1e-6))
	}
	w.pending = append(w.pending, o)
}

// faded is c under the opacity in effect; w.mu is held.
func (w *Window) faded(c scene.Color) scene.Color {
	if w.opacity < 1 {
		c.A = uint8(math.Round(float64(c.A) * w.opacity))
	}
	return c
}

// SetOpacity sets the opacity, from 0 to 1, that multiplies the alpha of
// what is painted until the next SetOpacity or Begin.
func (w *Window) SetOpacity(o float64) {
	w.mu.Lock()
	w.opacity = min(max(o, 0), 1)
	w.mu.Unlock()
}

// Opacity is the opacity in effect.
func (w *Window) Opacity() float64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.opacity
}

// Box paints a rectangle with corners of the radii tl, tr, br and bl (top
// left, top right, bottom right, bottom left), filled with fill and, when
// line is positive, a border of width line in lineColor inside its edges,
// colors 0xRRGGBBAA.
func (w *Window) Box(x, y, width, height, tl, tr, br, bl float64, fill uint32, line float64, lineColor uint32) {
	o := scene.Op{Kind: scene.OpFill, Rect: rect(x, y, width, height), Transform: scene.Translation(0, 0), Radii: radii(tl, tr, br, bl), Color: rgba(fill)}
	if line > 0 {
		o.Border, o.BorderColor = scene.Uniform(float32(line)), rgba(lineColor)
	}
	w.mu.Lock()
	w.add(o)
	w.mu.Unlock()
}

// Shadow paints the shadow of the box at x, y with corner radii tl, tr,
// br and bl, as CSS's box-shadow does: the box offset by dx, dy, grown by
// spread, blurred by blur (CSS's blur radius), in color; outside the box,
// or, inset, inside it.
func (w *Window) Shadow(x, y, width, height, tl, tr, br, bl, dx, dy, blur, spread float64, color uint32, inset bool) {
	grow := spread
	if inset {
		grow = -spread
	}
	r := func(v float64) float64 { return max(v+grow, 0) }
	o := scene.Op{
		Kind: scene.OpShadow, Transform: scene.Translation(0, 0),
		Rect:  rect(x+dx-grow, y+dy-grow, max(width+2*grow, 0), max(height+2*grow, 0)),
		Radii: radii(r(tl), r(tr), r(br), r(bl)), Blur: float32(blur), Color: rgba(color), Inset: inset,
		Cast: rect(x, y, width, height), CastRadii: radii(tl, tr, br, bl),
	}
	w.mu.Lock()
	w.add(o)
	w.mu.Unlock()
}

func radii(tl, tr, br, bl float64) [4]float32 {
	return [4]float32{float32(tl), float32(tr), float32(br), float32(bl)}
}

// Clip clips what follows to a rectangle, within the clip in effect,
// until the matching Unclip.
func (w *Window) Clip(x, y, width, height float64) {
	w.mu.Lock()
	w.pending = append(w.pending, scene.Op{Kind: scene.OpPushClip, Rect: rect(x, y, width, height), Transform: scene.Translation(0, 0)})
	w.clips++
	w.mu.Unlock()
}

// Unclip ends the last Clip.
func (w *Window) Unclip() {
	w.mu.Lock()
	if w.clips > 0 {
		w.pending = append(w.pending, scene.Op{Kind: scene.OpPopClip})
		w.clips--
	}
	w.mu.Unlock()
}

// Text paints one line of s at font size size, its line box's top left
// corner at x, y.
func (w *Window) Text(x, y float64, s string, size float64, color uint32) {
	w.mu.Lock()
	w.glyphs = append(w.glyphs, glyphText{at: len(w.pending), x: float32(x), y: float32(y), s: s, size: float32(size), color: w.faded(rgba(color))})
	w.mu.Unlock()
}

// Access adds a node of the accessibility tree: id names it from frame to
// frame (1 or more), parent is the index of its parent among the nodes
// added since Begin (-1 at the top).
func (w *Window) Access(id int, parent int, role int, label string, value string, x, y, width, height float64, checked bool, focused bool) {
	roles := []platform.AccessRole{platform.RoleGroup, platform.RoleText, platform.RoleButton, platform.RoleCheckBox, platform.RoleToggleButton, platform.RoleTextField}
	n := platform.AccessNode{ID: uint64(id), Parent: parent, Label: label, Value: value, Bounds: platform.RectF{X: x, Y: y, W: width, H: height}}
	if role >= 0 && role < len(roles) {
		n.Role = roles[role]
	}
	if role >= RoleButton {
		n.States |= platform.AccessFocusable
	}
	if checked {
		n.States |= platform.AccessChecked
	}
	w.mu.Lock()
	w.access = append(w.access, n)
	if focused {
		w.focus = uint64(id)
	}
	w.mu.Unlock()
}

// Editing tells the input methods that a text field has the keyboard:
// its text, and its caret's box. Begin ends it.
func (w *Window) Editing(value string, x, y, width, height float64) {
	n := len([]rune(value))
	w.mu.Lock()
	w.field = platform.TextInputState{Active: true, Caret: platform.RectF{X: x, Y: y, W: width, H: height}, Text: value, Start: n, End: n}
	w.mu.Unlock()
}

// Present shows the list made since Begin: the next frame draws it.
func (w *Window) Present() {
	w.mu.Lock()
	for ; w.clips > 0; w.clips-- {
		w.pending = append(w.pending, scene.Op{Kind: scene.OpPopClip})
	}
	w.shown = append(w.shown[:0], w.pending...)
	// The shown list's buffers become the next list's (begin empties
	// them): two of each, not a new one a frame.
	w.texts, w.glyphs = w.glyphs, w.texts
	w.shownStrokes, w.strokes = w.strokes, w.shownStrokes
	w.shownPts, w.pts = w.pts, w.shownPts
	w.shownAccess, w.access = w.access, w.shownAccess
	w.segs, w.prev = w.prev, w.segs
	clear(w.segs)
	w.painted++
	w.turnLayouts()
	field := w.field
	w.publishAccess(false)
	w.mu.Unlock()
	if w.conn != nil {
		post := func() { w.conn.Surface.SetTextInput(field) }
		if w.conn.Post != nil {
			w.conn.Post(post)
		}
	}
	w.invalidate()
}

// sameNode reports whether two accessibility nodes Access made are the
// same: the fields it sets (reflect.DeepEqual would allocate a frame).
func sameNode(a, b platform.AccessNode) bool {
	return a.ID == b.ID && a.Parent == b.Parent && a.Role == b.Role && a.Label == b.Label && a.Value == b.Value && a.Bounds == b.Bounds && a.States == b.States
}

// publishAccess gives the surface the shown list's accessibility tree,
// when it differs from the one last given or force; w.mu is held.
func (w *Window) publishAccess(force bool) {
	if w.conn == nil {
		return
	}
	if !force && w.focus == w.publishedFocus && slices.EqualFunc(w.shownAccess, w.published, sameNode) {
		return
	}
	w.published, w.publishedFocus = append(w.published[:0], w.shownAccess...), w.focus
	tree := &platform.AccessTree{Nodes: append([]platform.AccessNode(nil), w.shownAccess...), Focus: w.focus}
	if w.conn.Post != nil {
		w.conn.Post(func() { w.conn.Surface.UpdateAccessibility(tree) })
	}
}

// TextWidth is the width of s set on one line at font size size. Its
// layout, as glyphLine's, comes from the text system's cache (Layout, as
// MyGo's ui lays text out), which keeps what recent frames used, not
// shaped anew (Shape) each call.
func (w *Window) TextWidth(s string, size float64) float64 {
	l := w.layout(s, float32(size))
	return float64(l.Width)
}

// textKey is a text set on one line at a size.
type textKey struct {
	s    string
	size float32
}

// layout is s set on one line at size: from the layouts of the last two
// lists, else shaped, as MyGo's text system shapes it.
func (w *Window) layout(s string, size float32) *text.Layout {
	k := textKey{s, size}
	w.tmu.Lock()
	defer w.tmu.Unlock()
	if l, ok := w.laid[k]; ok {
		return l
	}
	l, ok := w.lastLaid[k]
	if !ok {
		shaping()
		l = w.sys.Shape(text.Params{Text: s, Style: text.Style{Size: size}, NoWrap: true, KeepSpaces: true})
	}
	if w.laid == nil {
		w.laid = map[textKey]*text.Layout{}
	}
	w.laid[k] = l
	return l
}

// turnLayouts starts the next list's layouts: the list made's are the
// last, and those of the list before go.
func (w *Window) turnLayouts() {
	w.tmu.Lock()
	clear(w.lastLaid)
	w.laid, w.lastLaid = w.lastLaid, w.laid
	w.tmu.Unlock()
}

// LineHeight is the distance between lines at font size size.
func (w *Window) LineHeight(size float64) float64 {
	_, _, h := w.sys.Metrics(text.Style{Size: float32(size)})
	return math.Ceil(float64(h))
}

// draw renders the shown list into the surface, on the main thread: with
// the surface's GPU renderer where it has one (render.go), else with the
// CPU renderer, whose pixels the surface presents. A phone's surface
// presents no pixels, so there only the GPU shows a frame.
func (w *Window) draw() {
	if w.conn == nil {
		return
	}
	width, height, scale := w.conn.Surface.Size()
	if scale <= 0 {
		scale = 1
	}
	w.sys.BeginFrame()
	w.mu.Lock()
	lined := w.lined
	w.build(width, height, scale)
	for try := 0; try < 2 && w.sys.Full(); try++ {
		w.sys.MakeRoom()
		w.lined = lined
		w.build(width, height, scale)
	}
	w.mu.Unlock()
	if !w.renderGPU(&w.sc) {
		w.r.Render(&w.sc)
		// A Headless window's frame stays in the renderer's image, which
		// Pixel reads: the fake surface would copy it into a new buffer a
		// frame.
		if w.fake == nil {
			w.conn.Surface.PresentPixels(w.r.Image.Pix, w.r.Image.Stride, w.r.Image.W, w.r.Image.H)
		}
	}
	w.sys.EndFrame()
}

// build makes the scene of the shown list in w.sc; w.mu is held. The text
// system's frame has begun (BeginFrame): a mask or glyph an atlas leaves
// out, it makes room for (MakeRoom), and draw builds the scene again,
// since what the atlas kept moved.
func (w *Window) build(width, height, scale float64) {
	s := &w.sc
	s.Reset(int(width*scale), int(height*scale), scene.Color{R: 255, G: 255, B: 255, A: 255})
	s.Scale, s.Zoom = float32(scale), 1
	s.MaskAtlas, s.ColorAtlas = w.sys.MaskAtlas, w.sys.ColorAtlas
	s.Text = w.sys.TextParams()
	w.vw, w.vh = float32(width*scale), float32(height*scale)
	ops := w.shown
	texts := w.texts
	strokes := w.shownStrokes
	k, j := 0, 0
	for i, op := range ops {
		for ; k < len(texts) && texts[k].at == i; k++ {
			w.glyphLine(s, texts[k], float32(scale))
		}
		if j < len(strokes) && strokes[j].at == i {
			w.strokeMask(s, strokes[j], w.shownPts[strokes[j].p0:strokes[j].p1], float32(scale))
			j++
			continue
		}
		s.Ops = append(s.Ops, op)
	}
	for ; k < len(texts); k++ {
		w.glyphLine(s, texts[k], float32(scale))
	}
}

// strokeMask adds stroke t to s as one glyph of the mask atlas, as MyGo's
// Painter.StrokePath does (ui/path.go): its points, in device pixels, are
// moved to quarters of a pixel from the mask's corner, and the mask is
// keyed by them and the width, so the atlas keeps it while they do not
// change. The mask covers each segment's rectangle and a disc at each
// point, as Line's rounded rectangles do. A mask wider or taller than
// maskTile pixels draws as tiles (strokeTiles). When the atlas cannot hold
// it, the stroke is a Line a segment.
func (w *Window) strokeMask(s *scene.Scene, t stroke, pts []float32, scale float32) {
	hw := t.width * scale / 2
	n := len(pts) / 2
	minX, minY := float32(math.MaxFloat32), float32(math.MaxFloat32)
	maxX, maxY := float32(-math.MaxFloat32), float32(-math.MaxFloat32)
	for i := 0; i < n; i++ {
		x, y := pts[2*i]*scale, pts[2*i+1]*scale
		minX, maxX = min(minX, x), max(maxX, x)
		minY, maxY = min(minY, y), max(maxY, y)
	}
	x0 := float32(math.Floor(float64(minX-hw))) - 1
	y0 := float32(math.Floor(float64(minY-hw))) - 1
	mw := int(math.Ceil(float64(maxX+hw-x0))) + 2
	mh := int(math.Ceil(float64(maxY+hw-y0))) + 2
	if mw <= 0 || mh <= 0 || mw > maskSpanMax || mh > maskSpanMax || math.IsNaN(float64(x0+y0)) {
		w.strokeLines(s, t, pts)
		return
	}
	if cap(w.q) < 2*n {
		w.q = make([]float32, 2*n)
	}
	q := w.q[:2*n]
	var hs maphash.Hash
	hs.SetSeed(maskSeed)
	var buf [8]byte
	binary.LittleEndian.PutUint32(buf[:4], math.Float32bits(hw))
	binary.LittleEndian.PutUint32(buf[4:], uint32(n))
	hs.Write(buf[:])
	for i := 0; i < n; i++ {
		q[2*i] = float32(math.Round(float64((pts[2*i]*scale-x0)*4))) / 4
		q[2*i+1] = float32(math.Round(float64((pts[2*i+1]*scale-y0)*4))) / 4
		binary.LittleEndian.PutUint32(buf[:4], math.Float32bits(q[2*i]))
		binary.LittleEndian.PutUint32(buf[4:], math.Float32bits(q[2*i+1]))
		hs.Write(buf[:])
	}
	key := hs.Sum64()
	start := int32(len(s.Glyphs))
	if mw > maskTile || mh > maskTile {
		if !w.strokeTiles(s, t, key, q, hw, x0, y0, mw, mh) {
			s.Glyphs = s.Glyphs[:start]
			w.strokeLines(s, t, pts)
			return
		}
	} else if !w.maskGlyph(s, key, q, hw, x0, y0, 0, 0, mw, mh, t.color) {
		w.strokeLines(s, t, pts)
		return
	}
	if end := int32(len(s.Glyphs)); end > start {
		s.Ops = append(s.Ops, scene.Op{Kind: scene.OpGlyphs, Start: start, End: end, Transform: scene.Translation(0, 0)})
	}
}

// maskGlyph adds to s the glyph of the tw×th part at (ox, oy) of the mask
// of polyline q, whose corner is at (x0, y0), cached under key; false when
// the atlas left it out.
func (w *Window) maskGlyph(s *scene.Scene, key uint64, q []float32, hw, x0, y0 float32, ox, oy, tw, th int, color scene.Color) bool {
	gi := w.sys.Mask(key, func() (int, int, []byte) {
		w.masks++
		return tw, th, w.rasterize(q, hw, ox, oy, tw, th)
	})
	if !gi.OK {
		return false
	}
	s.Glyphs = append(s.Glyphs, scene.Glyph{X: x0 + float32(ox), Y: y0 + float32(oy), W: float32(gi.W), H: float32(gi.H), U: gi.X, V: gi.Y, UW: gi.W, VH: gi.H, Color: color})
	return true
}

// strokeTiles adds to s the mw×mh mask of polyline q at (x0, y0), too
// large for one, as tiles of maskTile pixels a side from its corner, as
// MyGo's Painter.pathTiles does: each a mask of its own, of the whole
// polyline clipped to it, cached under the stroke's key and its place, so
// that tiles meet as the pixels of one mask do. It draws the tiles that
// show, within a pixel, and that a segment comes within the half width
// and two pixels of (a pixel of antialiasing and one of rounding). It reports false when more than maskTilesMax
// tiles show or the atlas left one out.
func (w *Window) strokeTiles(s *scene.Scene, t stroke, key uint64, q []float32, hw, x0, y0 float32, mw, mh int) bool {
	tile := maskTile
	cols, rows := (mw+tile-1)/tile, (mh+tile-1)/tile
	span := func(from, to, origin float32, n int) (int, int) {
		a := math.Floor(float64((from - origin - 1) / float32(tile)))
		b := math.Ceil(float64((to - origin + 1) / float32(tile)))
		return int(max(a, 0)), int(min(b, float64(n)))
	}
	c0, c1 := span(0, w.vw, x0, cols)
	r0, r1 := span(0, w.vh, y0, rows)
	vc, vr := c1-c0, r1-r0
	if vc <= 0 || vr <= 0 {
		return true
	}
	if vc*vr > maskTilesMax {
		return false
	}
	reached := make([]bool, vc*vr)
	m := hw + 2
	n := len(q) / 2
	for i := 0; i < n; i++ {
		ax, ay := q[2*i], q[2*i+1]
		bx, by := ax, ay
		if i > 0 {
			ax, ay = q[2*i-2], q[2*i-1]
		}
		size := float32(tile)
		rl := max(int(math.Floor(float64((min(ay, by)-m)/size))), r0)
		rh := min(int(math.Floor(float64((max(ay, by)+m)/size))), r1-1)
		for r := rl; r <= rh; r++ {
			// The part of the segment within m of the row, as pathTiles
			// takes it.
			xl, xh := min(ax, bx), max(ax, bx)
			if ay != by {
				k0 := (float32(r*tile) - m - ay) / (by - ay)
				k1 := (float32((r+1)*tile) + m - ay) / (by - ay)
				k0, k1 = max(min(k0, k1), 0), min(max(k0, k1), 1)
				xa, xb := ax+(bx-ax)*k0, ax+(bx-ax)*k1
				xl, xh = min(xa, xb), max(xa, xb)
			}
			cl := max(int(math.Floor(float64((xl-m)/size))), c0)
			ch := min(int(math.Floor(float64((xh+m)/size))), c1-1)
			for c := cl; c <= ch; c++ {
				reached[(r-r0)*vc+c-c0] = true
			}
		}
	}
	for r := r0; r < r1; r++ {
		for c := c0; c < c1; c++ {
			if !reached[(r-r0)*vc+c-c0] {
				continue
			}
			ox, oy := c*tile, r*tile
			tw, th := min(tile, mw-ox), min(tile, mh-oy)
			if !w.maskGlyph(s, tileKey(key, ox, oy, tw, th), q, hw, x0, y0, ox, oy, tw, th, t.color) {
				return false
			}
		}
	}
	return true
}

// tileKey is the key of the tw×th tile at (ox, oy) of the mask of key.
func tileKey(key uint64, ox, oy, tw, th int) uint64 {
	var buf [24]byte
	binary.LittleEndian.PutUint64(buf[:], key)
	binary.LittleEndian.PutUint32(buf[8:], uint32(ox))
	binary.LittleEndian.PutUint32(buf[12:], uint32(oy))
	binary.LittleEndian.PutUint32(buf[16:], uint32(tw))
	binary.LittleEndian.PutUint32(buf[20:], uint32(th))
	return maphash.Bytes(maskSeed, buf[:])
}

// maskTile is the side of a stroke's mask tile, in pixels: 512 with the
// atlas's padding, as MyGo's pathTile. A variable, so that tests can tile
// small strokes.
var maskTile = 511

// maskTilesMax is how many tiles of a stroke that show strokeTiles draws
// at most; maskSpanMax the widest and tallest mask, beyond which float32
// no longer holds the quarter pixels its points move to (MyGo's
// pathSpanMax).
const (
	maskTilesMax = 64 * 64
	maskSpanMax  = 1 << 22
)

// strokeLines adds t to s as a Line a segment (a point, a dot).
func (w *Window) strokeLines(s *scene.Scene, t stroke, pts []float32) {
	w.lined++
	n := len(pts) / 2
	if n == 1 {
		s.Ops = append(s.Ops, lineOp(float64(pts[0]), float64(pts[1]), float64(pts[0]), float64(pts[1]), float64(t.width), t.color))
	}
	for i := 1; i < n; i++ {
		s.Ops = append(s.Ops, lineOp(float64(pts[2*i-2]), float64(pts[2*i-1]), float64(pts[2*i]), float64(pts[2*i+1]), float64(t.width), t.color))
	}
}

// rasterize is the coverage, tw by th from (ox, oy) of the mask, of the
// polyline q (pixels from the mask's corner) hw to each side: each
// segment's rectangle and a disc at each point, all wound one way, so
// where they overlap they cover once.
func (w *Window) rasterize(q []float32, hw float32, ox, oy, tw, th int) []byte {
	z := &w.z
	z.Reset(tw, th)
	offX, offY := float32(ox), float32(oy)
	poly := func(p [][2]float32) {
		area := float32(0)
		for i := range p {
			a, b := p[i], p[(i+1)%len(p)]
			area += a[0]*b[1] - b[0]*a[1]
		}
		if area < 0 {
			for i, j := 0, len(p)-1; i < j; i, j = i+1, j-1 {
				p[i], p[j] = p[j], p[i]
			}
		}
		z.MoveTo(p[0][0]-offX, p[0][1]-offY)
		for _, v := range p[1:] {
			z.LineTo(v[0]-offX, v[1]-offY)
		}
		z.ClosePath()
	}
	sides := max(8, int(math.Ceil(float64(2*math.Pi*hw/1.5))))
	if cap(w.disc) < sides {
		w.disc = make([][2]float32, sides)
	}
	disc := w.disc[:sides]
	n := len(q) / 2
	for i := 0; i < n; i++ {
		cx, cy := q[2*i], q[2*i+1]
		for k := range disc {
			a := 2 * math.Pi * float64(k) / float64(sides)
			disc[k] = [2]float32{cx + hw*float32(math.Cos(a)), cy + hw*float32(math.Sin(a))}
		}
		poly(disc)
		if i == 0 {
			continue
		}
		ax, ay := q[2*i-2], q[2*i-1]
		dx, dy := cx-ax, cy-ay
		l := float32(math.Hypot(float64(dx), float64(dy)))
		if l == 0 {
			continue
		}
		nx, ny := -dy/l*hw, dx/l*hw
		poly([][2]float32{{ax + nx, ay + ny}, {cx + nx, cy + ny}, {cx - nx, cy - ny}, {ax - nx, ay - ny}})
	}
	// The text system copies the mask into its atlas (Mask), so the
	// buffer is the next mask's, as MyGo's rasterizePath reuses one.
	if cap(w.pix) < tw*th {
		w.pix = make([]byte, tw*th)
	}
	pix := w.pix[:tw*th]
	clear(pix)
	z.Mask(pix, tw)
	return pix
}

// glyphLine adds the glyphs of one line of text to s, as MyGo's own
// Painter.Glyphs does (ui/glyphs.go), one glyph at a time.
func (w *Window) glyphLine(s *scene.Scene, t glyphText, scale float32) {
	style := text.Style{Size: t.size}
	ascent, _, lineHeight := w.sys.Metrics(style)
	l := w.layout(t.s, t.size)
	shade := text.ShadeOf(t.color.R, t.color.G, t.color.B)
	top := t.y + (float32(math.Ceil(float64(lineHeight)))-lineHeight)/2
	baseline := w.sys.Baseline((top + ascent) * scale)
	start := int32(len(s.Glyphs))
	for _, line := range l.Lines {
		for _, g := range line.Glyphs {
			if g.Font == nil {
				continue
			}
			pen := (t.x + line.X + g.X) * scale
			gi := w.sys.Glyph(g.Font, g.ID, scale, pen, shade, false)
			if !gi.OK {
				continue
			}
			s.Glyphs = append(s.Glyphs, scene.Glyph{
				X: float32(math.Floor(float64(pen))) + gi.Left, Y: baseline + gi.Top, W: float32(gi.W), H: float32(gi.H),
				U: gi.X, V: gi.Y, UW: gi.W, VH: gi.H,
				Color: t.color, Colored: gi.Colored, Subpixel: gi.Subpixel, Thin: gi.Thin,
			})
		}
	}
	if end := int32(len(s.Glyphs)); end > start {
		s.Ops = append(s.Ops, scene.Op{Kind: scene.OpGlyphs, Start: start, End: end, Transform: scene.Translation(0, 0)})
	}
}

// Painted is how many lists Present has given.
func (w *Window) Painted() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.painted
}

// The input of a Headless window, as its fake surface sends it.

// Press presses the primary button at x, y.
func (w *Window) Press(x, y float64) {
	w.send(platform.SurfaceEvent{Kind: platform.PointerDown, X: x, Y: y})
}

// Release releases the primary button at x, y.
func (w *Window) Release(x, y float64) {
	w.send(platform.SurfaceEvent{Kind: platform.PointerUp, X: x, Y: y})
}

// Hover moves the pointer to x, y.
func (w *Window) Hover(x, y float64) {
	w.send(platform.SurfaceEvent{Kind: platform.PointerMove, X: x, Y: y})
}

// Wheel scrolls by dx, dy at x, y.
func (w *Window) Wheel(x, y, dx, dy float64) {
	w.send(platform.SurfaceEvent{Kind: platform.PointerScroll, X: x, Y: y, DX: dx, DY: dy})
}

// Type types s as an input method commits it.
func (w *Window) Type(s string) { w.send(platform.SurfaceEvent{Kind: platform.TextInput, Text: s}) }

// PressKey presses the key named name ("Enter", "Tab", "Backspace",
// "ArrowUp", " " and the others Input names), with Shift when shift.
func (w *Window) PressKey(name string, shift bool) {
	w.Chord(name, shift, false, false, false)
}

// Chord presses the key named name with the modifiers given: a key
// PressKey names, or a letter or digit ("z", "5") as a shortcut takes it.
func (w *Window) Chord(name string, shift, ctrl, alt, meta bool) {
	var m platform.Modifiers
	for _, on := range []struct {
		set bool
		mod platform.Modifiers
	}{{shift, platform.ModShift}, {ctrl, platform.ModCtrl}, {alt, platform.ModAlt}, {meta, platform.ModSuper}} {
		if on.set {
			m |= on.mod
		}
	}
	for k := platform.KeyUnknown; k <= platform.KeyForward; k++ {
		if keyName(k) == name || chordName(k) == name {
			w.send(platform.SurfaceEvent{Kind: platform.KeyPressed, Key: k, Mods: m})
			return
		}
	}
}

func (w *Window) send(ev platform.SurfaceEvent) {
	if w.fake != nil {
		w.fake.Send(ev)
	}
}

// Render draws a Headless window's frame when one was asked for, and
// reports whether it drew.
func (w *Window) Render() bool {
	if w.fake == nil {
		return false
	}
	return w.fake.Frame()
}

// Pixel is a Headless window's pixel at x, y of the last frame, as
// 0xRRGGBBAA.
func (w *Window) Pixel(x, y int) uint32 {
	if w.fake == nil {
		return 0
	}
	m := &w.r.Image
	if x < 0 || y < 0 || x >= m.W || y >= m.H {
		return 0
	}
	p := m.Pix[y*m.Stride+x*4:] // BGRA
	return uint32(p[2])<<24 | uint32(p[1])<<16 | uint32(p[0])<<8 | uint32(p[3])
}

// Focused is the id the last accessibility tree gave the focus, 0 for
// none.
func (w *Window) Focused() int {
	if w.fake == nil {
		return 0
	}
	t, _ := w.fake.Accessibility()
	if t == nil {
		return 0
	}
	return int(t.Focus)
}

// Editing reports whether a Headless window's surface was told that a
// text field has the keyboard.
func (w *Window) EditingField() bool {
	if w.fake == nil {
		return false
	}
	on, _ := w.fake.TextInput()
	return on
}
