// Package screen is the Go half of the MyGo desktop host: a window that
// MyGo drives through its drawing layer, as plain values an Ergo program
// can take through import go. The program lays out, hit tests and paints
// (Ergo package desktop); screen turns the window's surface events into
// Inputs, and turns the program's paint calls into MyGo's display list,
// rasterized by MyGo's CPU renderer. It uses no MyGo widget.
//
// Threading follows MyGo's rules (v1-gui plan §1.3): the surface's events
// and frames arrive on the main thread, which only queues Inputs and
// draws the last list Present gave; the program's task calls Next, the
// paint calls and Present from any goroutine.
package screen

import (
	"context"
	"errors"
	"math"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/drawing/fake"
	"github.com/egoist/mygo/drawing/platform"
	"github.com/egoist/mygo/drawing/raster"
	"github.com/egoist/mygo/drawing/scene"
	"github.com/egoist/mygo/drawing/surface"
	"github.com/egoist/mygo/drawing/text"
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
	frames  bool
	start   time.Time
	width   float64
	height  float64
	painted int

	conn *surface.Conn
	r    raster.Renderer
	sc   scene.Scene
	sys  *text.System

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

func newWindow(width, height float64) *Window {
	return &Window{wake: make(chan struct{}, 1), start: time.Now(), width: width, height: height, sys: text.Shared()}
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
		w.publishAccess()
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
	w.pending, w.glyphs, w.clips, w.access = w.pending[:0], nil, 0, nil
	w.focus, w.field = 0, platform.TextInputState{}
	w.mu.Unlock()
}

// Fill paints a rectangle of color 0xRRGGBBAA with corners of radius.
func (w *Window) Fill(x, y, width, height, radius float64, color uint32) {
	w.mu.Lock()
	w.pending = append(w.pending, scene.Op{Kind: scene.OpFill, Rect: rect(x, y, width, height), Transform: scene.Translation(0, 0), Radii: scene.Uniform(float32(radius)), Color: rgba(color)})
	w.mu.Unlock()
}

// Border paints a border of width line inside a rectangle.
func (w *Window) Border(x, y, width, height, radius, line float64, color uint32) {
	w.mu.Lock()
	w.pending = append(w.pending, scene.Op{Kind: scene.OpFill, Rect: rect(x, y, width, height), Transform: scene.Translation(0, 0), Radii: scene.Uniform(float32(radius)), Border: scene.Uniform(float32(line)), BorderColor: rgba(color)})
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
	m := scene.Translation(float32(x0), float32(y0))
	if length > 0 {
		dx, dy = dx/length, dy/length
		m = scene.Matrix(float32(dx), float32(dy), float32(-dy), float32(dx), float32(x0), float32(y0))
	}
	hw := width / 2
	w.mu.Lock()
	w.pending = append(w.pending, scene.Op{Kind: scene.OpFill, Rect: rect(-hw, -hw, length+width, width), Transform: m, Radii: scene.Uniform(float32(hw)), Color: rgba(color)})
	w.mu.Unlock()
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
	w.glyphs = append(w.glyphs, glyphText{at: len(w.pending), x: float32(x), y: float32(y), s: s, size: float32(size), color: rgba(color)})
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
	w.texts = w.glyphs
	w.painted++
	field := w.field
	w.publishAccess()
	w.mu.Unlock()
	if w.conn != nil {
		post := func() { w.conn.Surface.SetTextInput(field) }
		if w.conn.Post != nil {
			w.conn.Post(post)
		}
	}
	w.invalidate()
}

// publishAccess gives the surface the accessibility tree; w.mu is held.
func (w *Window) publishAccess() {
	if w.conn == nil {
		return
	}
	tree := &platform.AccessTree{Nodes: append([]platform.AccessNode(nil), w.access...), Focus: w.focus}
	if w.conn.Post != nil {
		w.conn.Post(func() { w.conn.Surface.UpdateAccessibility(tree) })
	}
}

// TextWidth is the width of s set on one line at font size size.
func (w *Window) TextWidth(s string, size float64) float64 {
	l := w.sys.Shape(text.Params{Text: s, Style: text.Style{Size: float32(size)}, NoWrap: true, KeepSpaces: true})
	return float64(l.Width)
}

// LineHeight is the distance between lines at font size size.
func (w *Window) LineHeight(size float64) float64 {
	_, _, h := w.sys.Metrics(text.Style{Size: float32(size)})
	return math.Ceil(float64(h))
}

// draw renders the shown list into the surface, on the main thread.
func (w *Window) draw() {
	if w.conn == nil {
		return
	}
	width, height, scale := w.conn.Surface.Size()
	if scale <= 0 {
		scale = 1
	}
	s := &w.sc
	s.Reset(int(width*scale), int(height*scale), scene.Color{R: 255, G: 255, B: 255, A: 255})
	s.Scale, s.Zoom = float32(scale), 1
	s.MaskAtlas, s.ColorAtlas = w.sys.MaskAtlas, w.sys.ColorAtlas
	s.Text = w.sys.TextParams()
	w.mu.Lock()
	ops := w.shown
	texts := w.texts
	k := 0
	for i, op := range ops {
		for ; k < len(texts) && texts[k].at == i; k++ {
			w.glyphLine(s, texts[k], float32(scale))
		}
		s.Ops = append(s.Ops, op)
	}
	for ; k < len(texts); k++ {
		w.glyphLine(s, texts[k], float32(scale))
	}
	w.mu.Unlock()
	w.r.Render(s)
	w.conn.Surface.PresentPixels(w.r.Image.Pix, w.r.Image.Stride, w.r.Image.W, w.r.Image.H)
	w.sys.EndFrame()
}

// glyphLine adds the glyphs of one line of text to s, as MyGo's own
// Painter.Glyphs does (ui/glyphs.go), one glyph at a time.
func (w *Window) glyphLine(s *scene.Scene, t glyphText, scale float32) {
	style := text.Style{Size: t.size}
	ascent, _, lineHeight := w.sys.Metrics(style)
	l := w.sys.Shape(text.Params{Text: t.s, Style: style, NoWrap: true, KeepSpaces: true})
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
	for k := platform.KeyUnknown; k <= platform.KeyForward; k++ {
		if keyName(k) == name {
			var m platform.Modifiers
			if shift {
				m = platform.ModShift
			}
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
	p := w.fake.Pixel(x, y) // BGRA
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
