//go:build (linux || darwin) && (amd64 || arm64)

// Package screen is the Go half of the OpenTUI terminal host: a terminal
// (or, for tests, a headless renderer) whose retained scene the Ergo
// driver (package tui) changes, as plain values an Ergo program can take
// through import go. OpenTUI's native core lays the scene out (Yoga),
// paints it and diffs the frames; screen applies the driver's commands
// and moves bytes between the core and the terminal.
//
// Every call into the library runs on one owner thread (ot.Owner): a
// context belongs to the thread that made it. The scene commands
// (Create, Move, Destroy, SetText, SetStyle, SetLook, Focus) only record
// into a batch; Present applies the batch, paints and writes the frame in
// one hop to the owner thread. No callback crosses the FFI: output is read
// with ot_session_read_output and nothing is measured from Go, so a Go
// panic never unwinds through native frames.
package screen

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"sync"
	"time"
	"unsafe"

	"ergo.local/prototype/hosts/opentui/ot"
)

// The kinds of a node.
const (
	Box  = 0
	Text = 1
	// Scroller: a box whose children scroll vertically inside it, with
	// a bar while they are taller (ot_scene_set_scroll). Its Direction,
	// Padding, Gap, AlignItems and Justify are its content's.
	Scroller = 2
	// Lister: a Scroller of rows (a virtual list). Its children lie in a
	// content box Content cells high, each placed by its Inset (Absolute);
	// with no Height of its own it takes the rows below its top.
	Lister = 4
)

// scrolls is whether a node of kind scrolls its children.
func scrolls(kind int) bool { return kind == Scroller || kind == Lister }

// The kinds of an Input.
const (
	// Closed: the input ended or the screen closed; no Input follows.
	Closed = iota
	// Key: a key pressed or repeated (Action), with Code, BaseCode, Mods
	// and Text, as OpenTUI's parser reports them.
	Key
	// Mouse: a mouse event (Action: Down, Up, Move, Drag, Scroll) at cell
	// X, Y; Code is the button (or the wheel direction).
	Mouse
	// Paste: a bracketed paste of Text.
	Paste
	// Resize: the screen is now Width by Height cells.
	Resize
	// Focus: the terminal gained (Action 1) or lost (0) the focus.
	Focus
	// Wake: Wake was called (a result waits for the reader elsewhere).
	Wake
	// None: no Input waits (Take).
	None
	// Frame: a frame is due (Tick, frames.go).
	Frame
)

// The mouse actions.
const (
	Down   = int(ot.OT_INPUT_MOUSE_DOWN)
	Up     = int(ot.OT_INPUT_MOUSE_UP)
	Move   = int(ot.OT_INPUT_MOUSE_MOVE)
	Drag   = int(ot.OT_INPUT_MOUSE_DRAG)
	Scroll = int(ot.OT_INPUT_MOUSE_SCROLL)
)

// Input is one event of the terminal.
type Input struct {
	Kind, Action   int
	Code, BaseCode int
	Mods           int
	Text           string
	X, Y           int
	Width, Height  int
}

// The style properties of SetStyle. Lengths are in cells; a NaN length is
// auto.
const (
	// Direction: 0 column, 1 row.
	Direction = iota
	Grow
	Shrink
	Width
	Height
	// Padding, Margin, Inset (the absolute position): edge 0 left, 1 top,
	// 2 right, 3 bottom, 4 all.
	Padding
	Margin
	Inset
	Gap
	// AlignItems and Justify: 0 start, 1 centre, 2 end, 3 stretch
	// (AlignItems) or space between (Justify).
	AlignItems
	Justify
	// Absolute: 1 takes the node out of the flow.
	Absolute
	// Content: the height of a Lister's content box.
	Content
)

// The attributes of SetLook.
const (
	Bold      = int(ot.OT_ATTR_BOLD)
	Dim       = int(ot.OT_ATTR_DIM)
	Underline = int(ot.OT_ATTR_UNDERLINE)
	Inverse   = int(ot.OT_ATTR_INVERSE)
)

// Cell is one cell of the screen: its text (empty for the second cell of
// a wide glyph) and its colors (0xRRGGBBAA) and attributes.
type Cell struct {
	Text   string
	Fg, Bg uint32
	Attr   int
}

// Rect is a node's box in cells.
type Rect struct {
	X, Y, Width, Height int
}

type command struct {
	op           int
	node, parent int
	index, kind  int
	edge         int
	value        float64
	text         string
	fg, bg       uint32
	attr         int
	data         []byte
}

const (
	opCreate = iota
	opMove
	opDestroy
	opText
	opStyle
	opLook
	opFocus
	opPlaceholder
	opPixels
)

// Screen is a terminal, or a headless renderer of a fixed size.
type Screen struct {
	owner   *ot.Owner
	ctx     *ot.Context
	session ot.Handle
	root    ot.Handle

	// Owner-thread state.
	nodes    map[int]ot.Handle
	kinds    map[int]int
	parent   map[int]int
	children map[int][]int
	edits    map[int]editor
	// inner is each Lister's content box; fills the height each Lister
	// with no Height of its own was given (0 before the first frame).
	inner map[int]ot.Handle
	fills map[int]int
	// pictures is each canvas's pixels (canvas.go).
	pictures map[int]picture
	focus    int
	out      io.Writer
	chunk    []byte
	records  []ot.InputEvent
	payload  []byte
	start    time.Time
	live     bool
	// inputClock is the parser's clock of a headless screen (ns); deadline
	// the parser's last timeout, or 0.
	inputClock, deadline uint64

	mu      sync.Mutex
	batch   []command
	inputs  []Input
	wake    chan struct{}
	closed  bool
	cols    int
	rows    int
	calls   int64
	written int64
	done    bool
	in      *os.File
	stop    func()
	// ticker sends the frames while the view animates (frames.go).
	ticker *ticking
	// said is each text Notify said (notify.go).
	said []string
}

// ErrClosed is the error of a call on a closed screen.
var ErrClosed = errors.New("screen: closed")

func newScreen(cols, rows int, out io.Writer) (*Screen, error) {
	if err := ot.Open(); err != nil {
		return nil, err
	}
	s := &Screen{
		owner: ot.NewOwner(), nodes: map[int]ot.Handle{}, kinds: map[int]int{}, parent: map[int]int{},
		children: map[int][]int{}, edits: map[int]editor{}, inner: map[int]ot.Handle{}, fills: map[int]int{}, pictures: map[int]picture{}, focus: -1, out: out, chunk: make([]byte, 64<<10),
		records: make([]ot.InputEvent, 256), payload: make([]byte, 64<<10), start: time.Now(),
		wake: make(chan struct{}, 1), cols: cols, rows: rows, inputClock: 1,
	}
	var err error
	s.do(func() {
		s.calls += 3
		s.ctx, err = ot.ContextCreate(ot.ContextOptions{ObjectCapacity: 1 << 16, RenderCellsMax: 1 << 22})
		if err != nil {
			return
		}
		s.session, err = ot.SessionCreate(s.ctx, ot.SessionOptions{ChunkSize: 64 << 10, SpanCapacity: 256, MaxBytes: 16 << 20, ControlCapacity: 16 << 10})
		if err != nil {
			return
		}
		err = ot.SessionAttachRenderer(s.ctx, s.session, ot.SessionRendererOptions{Width: uint32(cols), Height: uint32(rows), Remote: 1})
		if err != nil {
			return
		}
		s.calls++
		s.root, err = ot.SceneCreateNode(s.ctx, s.session, ot.OT_SCENE_ROOT, 1)
	})
	if err != nil {
		s.owner.Close()
		return nil, fmt.Errorf("screen: %w", err)
	}
	return s, nil
}

// Headless is a screen of cols by rows cells with no terminal: tests feed
// it terminal bytes (Feed), resize it (Resize) and read its cells (Rows,
// Cell).
func Headless(cols, rows int) (*Screen, error) {
	return newScreen(cols, rows, io.Discard)
}

// do runs fn on the owner thread.
func (s *Screen) do(fn func()) {
	if err := s.owner.Do(fn); err != nil {
		panic(err)
	}
}

func (s *Screen) record(c command) {
	s.mu.Lock()
	s.batch = append(s.batch, c)
	s.mu.Unlock()
}

// Create adds a node of kind (Box, Text, Scroller or Editor) numbered n (n >= 1), not yet in
// the tree.
func (s *Screen) Create(n, kind int) { s.record(command{op: opCreate, node: n, kind: kind}) }

// Move puts node n at index among the children of parent (0: the root).
func (s *Screen) Move(n, parent, index int) {
	s.record(command{op: opMove, node: n, parent: parent, index: index})
}

// Destroy removes node n and every node under it.
func (s *Screen) Destroy(n int) { s.record(command{op: opDestroy, node: n}) }

// SetText sets the text of a Text node.
func (s *Screen) SetText(n int, text string) { s.record(command{op: opText, node: n, text: text}) }

// SetStyle sets one layout property of node n (edge for Padding, Margin
// and Inset).
func (s *Screen) SetStyle(n, prop, edge int, value float64) {
	s.record(command{op: opStyle, node: n, kind: prop, edge: edge, value: value})
}

// SetLook sets a node's colors (0xRRGGBBAA; an alpha of 0 is the
// terminal's default) and, for a Text node, its attributes.
func (s *Screen) SetLook(n int, fg, bg uint32, attr int) {
	s.record(command{op: opLook, node: n, fg: fg, bg: bg, attr: attr})
}

// Focus gives the scene's focus to node n, or to none for n < 1.
func (s *Screen) Focus(n int) { s.record(command{op: opFocus, node: n}) }

// Present applies the batch, paints the frame and writes it, in one hop to
// the owner thread.
func (s *Screen) Present() error {
	s.mu.Lock()
	batch := s.batch
	s.batch = nil
	closed := s.done
	s.mu.Unlock()
	if closed {
		return ErrClosed
	}
	var err error
	s.do(func() {
		for _, c := range batch {
			if err = s.apply(c); err != nil {
				return
			}
		}
		err = s.frame()
	})
	return err
}

func color(c uint32) [4]uint16 {
	return [4]uint16{uint16(c >> 24), uint16(c >> 16 & 0xff), uint16(c >> 8 & 0xff), uint16(c & 0xff)}
}

// apply runs one command on the owner thread.
func (s *Screen) apply(c command) error {
	if c.op == opCreate {
		kind := ot.OT_SCENE_BOX
		switch c.kind {
		case Text:
			kind = ot.OT_SCENE_TEXT
		case Editor:
			kind = ot.OT_SCENE_EDITOR
		}
		if _, ok := s.nodes[c.node]; ok || c.node < 1 {
			return fmt.Errorf("screen: Create %d: the number is taken or not positive", c.node)
		}
		s.calls++
		h, err := ot.SceneCreateNode(s.ctx, s.session, kind, uint32(c.node+1))
		if err != nil {
			return fmt.Errorf("screen: Create %d: %w", c.node, err)
		}
		s.nodes[c.node], s.kinds[c.node], s.parent[c.node] = h, c.kind, -1
		if scrolls(c.kind) {
			s.calls++
			bar := ot.SceneScrollBarOptions{Policy: ot.OT_SCENE_SCROLL_BAR_AUTO, Placement: ot.OT_SCENE_SCROLL_BAR_INSET, Thickness: 1,
				TrackForeground: [4]uint16{128, 128, 128, 255}}
			if err := ot.SceneSetScroll(s.ctx, h, ot.SceneScrollOptions{Axes: ot.OT_SCENE_SCROLL_Y, Bars: [2]ot.SceneScrollBarOptions{{Policy: ot.OT_SCENE_SCROLL_BAR_NEVER, Thickness: 1}, bar}}); err != nil {
				return fmt.Errorf("screen: Create %d: scroll: %w", c.node, err)
			}
		}
		if c.kind == Lister {
			// The content box hits as the list does (its public number) and
			// is as wide as the list: its rows lie in it out of the flow, so
			// they give it no width.
			s.calls += 3
			in, err := ot.SceneCreateNode(s.ctx, s.session, ot.OT_SCENE_BOX, uint32(c.node+1))
			if err == nil {
				err = ot.SceneMoveNode(s.ctx, in, h, 0)
			}
			if err == nil {
				err = ot.SceneSetStyle(s.ctx, in, ot.OT_STYLE_DIMENSION, ot.OT_STYLE_VALUE_WIDTH, ot.OT_EDGE_NONE, ot.OT_UNIT_PERCENT, 100, 0)
			}
			if err != nil {
				return fmt.Errorf("screen: Create %d: content: %w", c.node, err)
			}
			s.inner[c.node], s.fills[c.node] = in, 0
		}
		if c.kind == Editor {
			return s.newEditor(c.node, h)
		}
		if c.kind == Text {
			s.calls++
			if err := ot.SceneSetTextOptions(s.ctx, h, ot.SceneTextOptions{Foreground: [4]uint16{255, 255, 255, 255}, WrapMode: ot.OT_SCENE_WRAP_WORD}); err != nil {
				return fmt.Errorf("screen: Create %d: %w", c.node, err)
			}
		}
		return nil
	}
	if c.op == opFocus {
		if s.focus > 0 {
			if h, ok := s.nodes[s.focus]; ok {
				s.calls++
				if err := ot.SceneSetFocus(s.ctx, h, 0); err != nil {
					return err
				}
			}
		}
		s.focus = -1
		if h, ok := s.nodes[c.node]; ok {
			s.calls++
			if err := ot.SceneSetFocus(s.ctx, h, 1); err != nil {
				return err
			}
			s.focus = c.node
		}
		return nil
	}
	h, ok := s.nodes[c.node]
	if !ok {
		return fmt.Errorf("screen: node %d is not on the screen", c.node)
	}
	switch c.op {
	case opMove:
		p := s.root
		if c.parent != 0 {
			if p, ok = s.nodes[c.parent]; !ok {
				return fmt.Errorf("screen: Move %d: parent %d is not on the screen", c.node, c.parent)
			}
			if in, ok := s.inner[c.parent]; ok {
				p = in
			}
		}
		s.calls++
		if err := ot.SceneMoveNode(s.ctx, h, p, uint32(c.index)); err != nil {
			return fmt.Errorf("screen: Move %d to %d at %d: %w", c.node, c.parent, c.index, err)
		}
		s.unlink(c.node)
		kids := s.children[c.parent]
		i := min(c.index, len(kids))
		s.children[c.parent] = append(kids[:i:i], append([]int{c.node}, kids[i:]...)...)
		s.parent[c.node] = c.parent
	case opDestroy:
		s.unlink(c.node)
		return s.destroy(c.node)
	case opText:
		if e, ok := s.edits[c.node]; ok {
			if err := s.replace(e, c.text); err != nil {
				return fmt.Errorf("screen: SetText %d: %w", c.node, err)
			}
			return nil
		}
		s.calls++
		if err := ot.SceneSetText(s.ctx, h, []byte(c.text)); err != nil {
			return fmt.Errorf("screen: SetText %d: %w", c.node, err)
		}
	case opStyle:
		return s.style(c, h)
	case opPlaceholder:
		return s.placeholder(c.node, c.text)
	case opPixels:
		return s.pixels(c)
	case opLook:
		if s.kinds[c.node] == Editor {
			return s.editorLook(c.node, c.fg, c.bg, c.attr)
		}
		s.calls++
		if s.kinds[c.node] == Text {
			return ot.SceneSetTextOptions(s.ctx, h, ot.SceneTextOptions{
				Foreground: color(c.fg), Background: color(c.bg), Attributes: uint32(c.attr), WrapMode: ot.OT_SCENE_WRAP_WORD,
			})
		}
		fill := uint32(0)
		if c.bg&0xff != 0 {
			fill = 1
		}
		return ot.SceneSetPaint(s.ctx, h, ot.ScenePaintOptions{Opacity: 1, ShouldFill: fill, Background: color(c.bg), FocusedBorderColor: [4]uint16{0, 170, 255, 255}})
	}
	return nil
}

// unlink takes n out of its parent's children in the mirror.
func (s *Screen) unlink(n int) {
	p, ok := s.parent[n]
	if !ok || p < 0 {
		return
	}
	kids := s.children[p]
	for i, k := range kids {
		if k == n {
			s.children[p] = append(kids[:i:i], kids[i+1:]...)
			break
		}
	}
	s.parent[n] = -1
}

// destroy destroys n's subtree, children first, so no destroyed node's
// handle is used again.
func (s *Screen) destroy(n int) error {
	for _, k := range s.children[n] {
		if err := s.destroy(k); err != nil {
			return err
		}
	}
	delete(s.children, n)
	delete(s.pictures, n)
	if in, ok := s.inner[n]; ok {
		delete(s.inner, n)
		delete(s.fills, n)
		s.calls++
		if err := ot.SceneDestroyNode(s.ctx, in); err != nil {
			return fmt.Errorf("screen: Destroy %d: content: %w", n, err)
		}
	}
	h := s.nodes[n]
	delete(s.nodes, n)
	delete(s.kinds, n)
	delete(s.parent, n)
	if s.focus == n {
		s.focus = -1
	}
	s.calls++
	if err := ot.SceneDestroyNode(s.ctx, h); err != nil {
		return fmt.Errorf("screen: Destroy %d: %w", n, err)
	}
	return s.dropEditor(n)
}

// placement is the OpenTUI value of a placement: 0 start, 1 centre, 2
// end, 3 stretch (AlignItems) or between (Justify); another is start.
func placement(v float64, start, centre, end, last uint32) uint32 {
	switch v {
	case 1:
		return centre
	case 2:
		return end
	case 3:
		return last
	}
	return start
}

var edges = [...]uint32{ot.OT_EDGE_LEFT, ot.OT_EDGE_TOP, ot.OT_EDGE_RIGHT, ot.OT_EDGE_BOTTOM, ot.OT_EDGE_ALL}

func (s *Screen) style(c command, h ot.Handle) error {
	unit, v := ot.OT_UNIT_POINT, float32(c.value)
	if math.IsNaN(c.value) {
		unit, v = ot.OT_UNIT_AUTO, 0
	}
	edge := ot.OT_EDGE_NONE
	if c.edge >= 0 && c.edge < len(edges) {
		edge = edges[c.edge]
	}
	var group, kind uint32
	switch c.kind {
	case Direction:
		group, kind, unit, v = ot.OT_STYLE_ENUM, ot.OT_STYLE_ENUM_FLEX_DIRECTION, ot.OT_UNIT_UNDEFINED, float32(ot.OT_FLEX_DIRECTION_COLUMN)
		if c.value == 1 {
			v = float32(ot.OT_FLEX_DIRECTION_ROW)
		}
		edge = ot.OT_EDGE_NONE
	case Grow:
		group, kind, unit, edge = ot.OT_STYLE_FLOAT, ot.OT_STYLE_FLOAT_FLEX_GROW, ot.OT_UNIT_UNDEFINED, ot.OT_EDGE_NONE
	case Shrink:
		group, kind, unit, edge = ot.OT_STYLE_FLOAT, ot.OT_STYLE_FLOAT_FLEX_SHRINK, ot.OT_UNIT_UNDEFINED, ot.OT_EDGE_NONE
	case Width:
		group, kind, edge = ot.OT_STYLE_DIMENSION, ot.OT_STYLE_VALUE_WIDTH, ot.OT_EDGE_NONE
	case Height:
		group, kind, edge = ot.OT_STYLE_DIMENSION, ot.OT_STYLE_VALUE_HEIGHT, ot.OT_EDGE_NONE
		if s.kinds[c.node] == Lister {
			if math.IsNaN(c.value) {
				s.fills[c.node] = 0
			} else {
				delete(s.fills, c.node)
			}
		}
	case Content:
		in, ok := s.inner[c.node]
		if !ok {
			return fmt.Errorf("screen: SetStyle %d: Content of a node that is no Lister", c.node)
		}
		group, kind, edge, h = ot.OT_STYLE_DIMENSION, ot.OT_STYLE_VALUE_HEIGHT, ot.OT_EDGE_NONE, in
	case Padding:
		group, kind = ot.OT_STYLE_VALUE, ot.OT_STYLE_VALUE_PADDING
	case Margin:
		group, kind = ot.OT_STYLE_VALUE, ot.OT_STYLE_VALUE_MARGIN
	case Inset:
		group, kind = ot.OT_STYLE_VALUE, ot.OT_STYLE_VALUE_POSITION
	case Gap:
		group, kind, edge = ot.OT_STYLE_VALUE, ot.OT_STYLE_VALUE_GAP, ot.OT_GUTTER_ALL
	case AlignItems:
		group, kind, unit, edge = ot.OT_STYLE_ENUM, ot.OT_STYLE_ENUM_ALIGN_ITEMS, ot.OT_UNIT_UNDEFINED, ot.OT_EDGE_NONE
		v = float32(placement(c.value, ot.OT_ALIGN_FLEX_START, ot.OT_ALIGN_CENTER, ot.OT_ALIGN_FLEX_END, ot.OT_ALIGN_STRETCH))
	case Justify:
		group, kind, unit, edge = ot.OT_STYLE_ENUM, ot.OT_STYLE_ENUM_JUSTIFY_CONTENT, ot.OT_UNIT_UNDEFINED, ot.OT_EDGE_NONE
		v = float32(placement(c.value, ot.OT_JUSTIFY_FLEX_START, ot.OT_JUSTIFY_CENTER, ot.OT_JUSTIFY_FLEX_END, ot.OT_JUSTIFY_SPACE_BETWEEN))
	case Absolute:
		group, kind, unit, edge = ot.OT_STYLE_ENUM, ot.OT_STYLE_ENUM_POSITION_TYPE, ot.OT_UNIT_UNDEFINED, ot.OT_EDGE_NONE
		v = float32(ot.OT_POSITION_RELATIVE)
		if c.value == 1 {
			v = float32(ot.OT_POSITION_ABSOLUTE)
		}
	default:
		return fmt.Errorf("screen: SetStyle %d: no property %d", c.node, c.kind)
	}
	flags := uint32(0)
	if scrolls(s.kinds[c.node]) {
		switch c.kind {
		case Direction, Padding, Gap, AlignItems, Justify:
			flags = ot.OT_STYLE_CONTENT
		}
	}
	s.calls++
	if err := ot.SceneSetStyle(s.ctx, h, group, kind, edge, unit, v, flags); err != nil {
		return fmt.Errorf("screen: SetStyle %d (property %d, edge %d, %v): %w", c.node, c.kind, c.edge, c.value, err)
	}
	return nil
}

// frame paints and commits one frame and writes its output (owner thread).
// A frame the output queue had no room for (SKIPPED) is painted again
// once the queue drained, so the last state is never dropped.
func (s *Screen) frame() error {
	for attempt := 0; attempt < 2; attempt++ {
		req, err := s.paint()
		if err != nil {
			return err
		}
		s.calls++
		st, err := ot.SceneFrameCommit(s.ctx, s.session, req, 0)
		if err != nil {
			s.calls++
			_ = ot.SceneFrameCancel(s.ctx, s.session, req.FrameId)
			return fmt.Errorf("screen: commit: %w", err)
		}
		if err := s.flush(); err != nil {
			return err
		}
		switch st {
		case ot.OT_RENDER_PRESENTED, ot.OT_RENDER_PENDING:
			return nil
		case ot.OT_RENDER_FAILED:
			return errors.New("screen: the frame failed (larger than the output queue, or encoding failed)")
		}
	}
	return errors.New("screen: the frame was skipped twice under output pressure")
}

// paint paints a frame (owner thread). When a Lister that fills the rows
// below it moved in the frame painted, it is given its new height and the
// frame is painted again, so the frame drawn has every list's height
// right; one more paint at most for each list that fills.
func (s *Screen) paint() (ot.SceneFrameRequest, error) {
	for tries := 0; ; tries++ {
		s.calls++
		req, err := ot.ScenePaint(s.ctx, s.session, [4]uint16{0, 0, 0, 0}, 1, 0)
		if err != nil {
			return req, fmt.Errorf("screen: paint: %w", err)
		}
		changed, err := s.fill()
		// A list placed by another that fills settles in as many paints
		// as there are lists; past that the frame keeps what it has.
		changed = changed && tries < len(s.fills)
		if err != nil || changed {
			s.calls++
			_ = ot.SceneFrameCancel(s.ctx, s.session, req.FrameId)
		}
		if err != nil {
			return req, err
		}
		if !changed {
			if err := s.draw(&req); err != nil {
				s.calls++
				_ = ot.SceneFrameCancel(s.ctx, s.session, req.FrameId)
				return req, err
			}
			return req, nil
		}
	}
}

// fill gives each Lister with no Height of its own the rows below its
// top, where the frame being prepared places it, and reports whether a
// height changed (owner thread).
func (s *Screen) fill() (bool, error) {
	changed := false
	if len(s.fills) == 0 {
		return false, nil
	}
	s.mu.Lock()
	rows := s.rows
	s.mu.Unlock()
	for n, was := range s.fills {
		s.calls++
		l, err := ot.SceneGetLayout(s.ctx, s.nodes[n], ot.OT_LAYOUT_PAINT)
		if err != nil {
			return false, fmt.Errorf("screen: layout of %d: %w", n, err)
		}
		want := max(rows-int(math.Round(l.ScreenY)), 1)
		if want == was {
			continue
		}
		s.fills[n], changed = want, true
		s.calls++
		if err := ot.SceneSetStyle(s.ctx, s.nodes[n], ot.OT_STYLE_DIMENSION, ot.OT_STYLE_VALUE_HEIGHT, ot.OT_EDGE_NONE, ot.OT_UNIT_POINT, float32(want), 0); err != nil {
			return false, fmt.Errorf("screen: fill %d: %w", n, err)
		}
	}
	return changed, nil
}

// flush writes every queued output byte to the terminal (owner thread).
func (s *Screen) flush() error {
	for {
		s.calls++
		t, err := ot.SessionReadOutput(s.ctx, s.session, s.chunk)
		if err != nil {
			return fmt.Errorf("screen: read output: %w", err)
		}
		if t.ByteCount == 0 {
			return nil
		}
		_, werr := s.out.Write(s.chunk[:t.ByteCount])
		ok := uint32(1)
		if werr != nil {
			ok = 0
		}
		s.calls++
		if err := ot.SessionCompleteOutput(s.ctx, s.session, t, ok); err != nil {
			return fmt.Errorf("screen: complete output: %w", err)
		}
		if werr != nil {
			return fmt.Errorf("screen: write: %w", werr)
		}
		s.mu.Lock()
		s.written += int64(t.ByteCount)
		s.mu.Unlock()
	}
}

// lease reads the current frame's cells (owner thread).
func (s *Screen) lease(fn func(snap ot.BufferLeaseSnapshot)) error {
	var err error
	s.do(func() {
		s.calls++
		var snap ot.BufferLeaseSnapshot
		snap, err = ot.SessionAcquireBufferLease(s.ctx, s.session, 0)
		if err != nil {
			return
		}
		defer func() {
			s.calls++
			if rerr := ot.BufferLeaseRelease(s.ctx, snap.Lease); err == nil {
				err = rerr
			}
		}()
		fn(snap)
	})
	return err
}

// Rows are the screen's rows of text, one string a row; the second cell
// of a wide glyph adds nothing, so a row has the glyphs a person sees.
func (s *Screen) Rows() []string {
	var rows []string
	err := s.lease(func(snap ot.BufferLeaseSnapshot) {
		n := int(snap.Width * snap.Height)
		b := make([]byte, 4*n+int(snap.Height)+16)
		s.calls++
		w, err := ot.LeaseResolvedChars(s.ctx, snap.Lease, b, true, nil)
		if err != nil {
			panic(err)
		}
		rows = strings.Split(strings.TrimSuffix(string(b[:w]), "\n"), "\n")
	})
	if err != nil {
		panic(err)
	}
	return rows
}

// Cell is the cell at column x, row y.
func (s *Screen) Cell(x, y int) Cell {
	var c Cell
	err := s.lease(func(snap ot.BufferLeaseSnapshot) {
		w, h := int(snap.Width), int(snap.Height)
		if x < 0 || y < 0 || x >= w || y >= h {
			return
		}
		n := w * h
		b := make([]byte, 4*n+h+16)
		lengths := make([]byte, n)
		s.calls++
		written, err := ot.LeaseResolvedChars(s.ctx, snap.Lease, b, false, lengths)
		if err != nil {
			panic(err)
		}
		off := 0
		for i := range y*w + x {
			off += int(lengths[i])
		}
		c.Text = string(b[off : off+int(lengths[y*w+x])])
		_ = written
		// The planes (opentui.h, ot_buffer_lease_snapshot): fg and bg
		// are four uint16 RGBA components a cell (literal channels 0..255,
		// the high byte native color metadata), attributes a uint32 a
		// cell. The pointers are the library's, valid until the release
		// below, and read only here on the owner thread.
		i := y*w + x
		fg := unsafe.Slice((*uint16)(native(&snap.FgPtr)), 4*n)
		bg := unsafe.Slice((*uint16)(native(&snap.BgPtr)), 4*n)
		at := unsafe.Slice((*uint32)(native(&snap.AttributesPtr)), n)
		c.Fg, c.Bg, c.Attr = rgba(fg[4*i:4*i+4]), rgba(bg[4*i:4*i+4]), int(at[i]&ot.OT_ATTR_BASE_MASK)
	})
	if err != nil {
		panic(err)
	}
	return c
}

// native is the native address a lease field holds (a uint64 on the
// 64-bit targets), as a pointer. It points into the library's memory,
// not Go's, so the collector neither moves nor frees it.
func native(addr *uint64) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(addr))
}

func rgba(f []uint16) uint32 {
	var c uint32
	for _, v := range f {
		c = c<<8 | uint32(v&0xff)
	}
	return c
}

// BoxOf is node n's box in cells after the last Present.
func (s *Screen) BoxOf(n int) Rect {
	var r Rect
	s.do(func() {
		h, ok := s.nodes[n]
		if n == 0 {
			h, ok = s.root, true
		}
		if !ok {
			return
		}
		s.calls++
		l, err := ot.SceneGetLayout(s.ctx, h, ot.OT_LAYOUT_PAINT)
		if err != nil {
			panic(err)
		}
		r = Rect{int(math.Round(l.ScreenX)), int(math.Round(l.ScreenY)), int(math.Round(float64(l.Width))), int(math.Round(float64(l.Height)))}
	})
	return r
}

// Reveal scrolls each Scroller that holds node n, the innermost first, so
// n's box is inside its viewport (moving it the least); the next Present
// paints it there.
func (s *Screen) Reveal(n int) error {
	var err error
	s.do(func() {
		h, ok := s.nodes[n]
		if !ok {
			return
		}
		for p := s.parent[n]; p > 0; p = s.parent[p] {
			if !scrolls(s.kinds[p]) {
				continue
			}
			s.calls++
			if _, err = ot.SceneScrollRequest(s.ctx, s.nodes[p], ot.OT_SCENE_SCROLL_REQUEST_INTO_VIEW, h, ot.OT_SCENE_SCROLL_ALIGN_NEAREST, 0); err != nil {
				err = fmt.Errorf("screen: Reveal %d in %d: %w", n, p, err)
				return
			}
		}
	})
	return err
}

// Shown reports whether the first cell of node n's box is on the screen
// and inside the box of each Scroller that holds it: a cell a person sees
// the node in, after the last Present.
func (s *Screen) Shown(n int) bool {
	b := s.BoxOf(n)
	cols, rows := s.Size()
	if b.Width <= 0 || b.Height <= 0 || b.X < 0 || b.Y < 0 || b.X >= cols || b.Y >= rows {
		return false
	}
	var outer []int
	s.do(func() {
		for p := s.parent[n]; p > 0; p = s.parent[p] {
			if scrolls(s.kinds[p]) {
				outer = append(outer, p)
			}
		}
	})
	for _, p := range outer {
		v := s.BoxOf(p)
		if b.X < v.X || b.Y < v.Y || b.X >= v.X+v.Width || b.Y >= v.Y+v.Height {
			return false
		}
	}
	return true
}

// ScrollState is a Scroller's or a Lister's vertical scroll, in cells:
// the offset, the viewport's height, and the content's height.
type ScrollState struct {
	Offset, Height, Content int
}

// ScrollOf is node n's scroll after the last Present (zeros for a node
// that does not scroll).
func (s *Screen) ScrollOf(n int) ScrollState {
	var st ScrollState
	s.do(func() {
		h, ok := s.nodes[n]
		if !ok || !scrolls(s.kinds[n]) {
			return
		}
		s.calls++
		g, err := ot.SceneGetScroll(s.ctx, h)
		if err != nil {
			panic(fmt.Errorf("screen: ScrollOf %d: %w", n, err))
		}
		st = ScrollState{int(math.Round(g.Offset[1])), int(math.Round(g.Viewport[3])), int(math.Round(g.Content[3]))}
	})
	return st
}

// ScrollTo scrolls node n to offset y (cells), inside its content; by
// scrolls it by y from where it is. The next Present paints it there.
func (s *Screen) ScrollTo(n, y int, by bool) error {
	var err error
	s.do(func() {
		h, ok := s.nodes[n]
		if !ok || !scrolls(s.kinds[n]) {
			err = fmt.Errorf("screen: ScrollTo %d: no node that scrolls", n)
			return
		}
		flags := uint32(0)
		if by {
			flags = ot.OT_SCENE_SCROLL_RELATIVE_Y
		}
		s.calls++
		_, err = ot.SceneScrollTo(s.ctx, h, 0, float64(y), flags)
	})
	return err
}

// HitTest is the number of the node painted last at column x, row y, 0
// for the root, or -1 for none.
func (s *Screen) HitTest(x, y int) int {
	n := -1
	s.do(func() {
		s.calls++
		num, err := ot.SceneHitTest(s.ctx, s.session, int32(x), int32(y))
		if err != nil {
			panic(err)
		}
		n = int(num) - 1
	})
	return n
}

// Size is the screen's size in cells.
func (s *Screen) Size() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cols, s.rows
}

// Calls is the number of library calls the screen has made.
func (s *Screen) Calls() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// Bytes is the number of output bytes the screen has written.
func (s *Screen) Bytes() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.written
}

func (s *Screen) push(in ...Input) {
	s.mu.Lock()
	s.inputs = append(s.inputs, in...)
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Wake queues an Input of kind Wake, so a reader waiting in Next looks at
// what another goroutine posted for it.
func (s *Screen) Wake() { s.push(Input{Kind: Wake}) }

// Feed parses terminal bytes into Inputs. A sequence cut at the end of b
// waits in OpenTUI's parser for the next Feed. A headless screen's input
// clock stands still, so a lone Escape stays undecided (it may start a
// sequence) until Settle.
func (s *Screen) Feed(b string) {
	var got []Input
	s.do(func() { got = s.parse([]byte(b), s.inputClock) })
	s.push(got...)
}

// Settle decides what the input parser holds as its timeout would: a lone
// Escape becomes the Escape key. The input clock moves to the parser's
// deadline; the clock is the input here, as a terminal's pause is.
func (s *Screen) Settle() {
	var got []Input
	s.do(func() {
		if s.deadline > s.inputClock {
			s.inputClock = s.deadline
		}
		got = s.parse(nil, s.inputClock)
	})
	s.push(got...)
}

// parse runs the input parser over b at now (owner thread).
func (s *Screen) parse(b []byte, now uint64) []Input {
	var got []Input
	for first := true; first || len(b) > 0; first = false {
		s.calls++
		d, err := ot.SessionInputFeed(s.ctx, s.session, b, now, s.records, s.payload)
		if err != nil {
			panic(fmt.Errorf("screen: input: %w", err))
		}
		for _, e := range s.records[:d.Count] {
			in := Input{Action: int(e.Action), Code: int(e.Code), BaseCode: int(e.BaseCode), Mods: int(e.Modifiers), X: int(e.X), Y: int(e.Y)}
			in.Text = string(s.payload[e.TextOffset : e.TextOffset+e.TextLen])
			switch uint32(e.Kind) {
			case ot.OT_INPUT_KEY:
				if e.Action == uint8(ot.OT_INPUT_KEY_RELEASE) {
					continue
				}
				in.Kind = Key
			case ot.OT_INPUT_MOUSE:
				in.Kind = Mouse
			case ot.OT_INPUT_PASTE:
				in.Kind = Paste
			case ot.OT_INPUT_FOCUS:
				in.Kind = Focus
			case ot.OT_INPUT_REPLY:
				// A terminal's reply to the setup's queries (notify.go).
				s.reply(b[e.RawOffset : e.RawOffset+e.RawLen])
				continue
			default:
				continue
			}
			got = append(got, in)
		}
		s.deadline = d.DeadlineNs
		b = b[d.Consumed:]
		if d.Consumed == 0 && d.Count == 0 {
			break
		}
	}
	return got
}

// Resize makes the screen cols by rows cells; the next Present lays the
// scene out again. An Input of kind Resize follows.
func (s *Screen) Resize(cols, rows int) error {
	var err error
	s.do(func() {
		s.calls++
		err = ot.SessionResizeRenderer(s.ctx, s.session, uint32(cols), uint32(rows))
	})
	if err != nil {
		return fmt.Errorf("screen: resize: %w", err)
	}
	s.mu.Lock()
	s.cols, s.rows = cols, rows
	s.mu.Unlock()
	s.push(Input{Kind: Resize, Width: cols, Height: rows})
	return nil
}

// Poll returns the next Input, if one waits.
func (s *Screen) Poll() (Input, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.inputs) == 0 {
		if s.closed {
			return Input{Kind: Closed}, true
		}
		return Input{}, false
	}
	in := s.inputs[0]
	s.inputs = s.inputs[1:]
	s.took(in)
	return in, true
}

// Take returns the next Input if one waits, else an Input of kind None.
// (Poll's two results, as one: Ergo's IR lowering refuses a tuple result
// of a method on a Go value in the driver, "tdef of a value that is not a
// package call".)
func (s *Screen) Take() Input {
	if in, ok := s.Poll(); ok {
		return in
	}
	return Input{Kind: None}
}

// Count is how many nodes the scene holds, the root aside, after the
// last Present.
func (s *Screen) Count() int {
	n := 0
	s.do(func() { n = len(s.nodes) })
	return n
}

// Cols is the screen's width in cells.
func (s *Screen) Cols() int {
	c, _ := s.Size()
	return c
}

// Lines is the screen's height in cells.
func (s *Screen) Lines() int {
	_, r := s.Size()
	return r
}

// Next waits for the next Input. After the input ends it returns Closed.
func (s *Screen) Next(ctx context.Context) (Input, error) {
	for {
		if in, ok := s.Poll(); ok {
			return in, nil
		}
		select {
		case <-s.wake:
		case <-ctx.Done():
			return Input{}, ctx.Err()
		}
	}
}

// EndInput ends the input: once the queued Inputs are read, Next returns
// Closed.
func (s *Screen) EndInput() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Quit restores the terminal (a live screen's output ends with the
// sequences that leave the alternate screen and show the cursor), then
// frees the scene. A second Quit does nothing. It is not named Close: the
// Ergo driver keeps the screen in its Driver, which a closable resource
// (a Go Closer) cannot be stored in; the binary that opens a screen quits
// it on every exit (scope.OnExit).
func (s *Screen) Quit() error {
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		return nil
	}
	s.done = true
	stop := s.stop
	if s.ticker != nil {
		close(s.ticker.stop)
		s.ticker = nil
	}
	s.mu.Unlock()
	if stop != nil {
		stop()
	}
	var err error
	s.do(func() {
		if s.live {
			err = s.restore()
		}
		s.calls += 2
		if derr := ot.SessionDestroy(s.ctx, s.session); err == nil && derr != nil {
			err = derr
		}
		if derr := ot.ContextDestroy(s.ctx); err == nil && derr != nil {
			err = derr
		}
	})
	if cerr := s.owner.Close(); err == nil {
		err = cerr
	}
	s.EndInput()
	return err
}
