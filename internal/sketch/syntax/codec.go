package syntax

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// The syntax codec (CHK1b): the binary form of syntax trees in a checked
// std snapshot (internal/sketch/check, export.go). Every object of a tree
// (a node, a field list, a comment group) is an id in one table; a field
// that points to another object holds its id. The encoder gives an object
// an id the first time a caller or a field names it and writes each
// object's fields once, in id order; the decoder allocates every object
// from its type tag first, then reads the fields, so shared objects and
// any order of references survive. The field codec is generated
// (codec_gen.go, internal/sketch/exportgen).

// The table is written in segments (one per checked package of the
// snapshot), so a reader decodes only the segments it needs. Ids are global
// and grow from segment to segment: a segment's objects are the ones first
// named while it was written, and a reference to an earlier segment's object
// is that object's id. Below reports each such reference, so the caller can
// record which earlier segments a segment needs.

// NodeEncoder writes a table of syntax objects.
type NodeEncoder struct {
	ids  map[any]uint64
	objs []any
	// start is the number of objects of the segments already written.
	start int
	// Below, when set, receives every id of an earlier segment that the
	// current segment names.
	Below func(id uint64)
}

// NewNodeEncoder returns an empty table.
func NewNodeEncoder() *NodeEncoder { return &NodeEncoder{ids: map[any]uint64{}} }

// ID returns the id of the syntax object x (a non-nil pointer to a syntax
// struct), giving it the next id the first time.
func (n *NodeEncoder) ID(x any) uint64 {
	if id, ok := n.ids[x]; ok {
		if id <= uint64(n.start) && n.Below != nil {
			n.Below(id)
		}
		return id
	}
	if objTag(x) == 0 {
		panic(fmt.Sprintf("syntax: a %T is no syntax object", x))
	}
	n.objs = append(n.objs, x)
	id := uint64(len(n.objs))
	n.ids[x] = id
	return id
}

// Lookup returns the id of x when x is a syntax object, giving it one the
// first time, and false when it is not.
func (n *NodeEncoder) Lookup(x any) (uint64, bool) {
	if objTag(x) == 0 {
		return 0, false
	}
	return n.ID(x), true
}

// Next is the id the next new object of the table gets.
func (n *NodeEncoder) Next() uint64 { return uint64(len(n.objs)) + 1 }

// Bytes writes the current segment and starts the next: the count, each
// new object's tag, then each new object's fields. A field may name an
// object no caller named; it joins the segment and is written in turn.
func (n *NodeEncoder) Bytes() []byte {
	body := &encoder{table: n}
	for i := n.start; i < len(n.objs); i++ {
		body.encObj(n.objs[i])
	}
	head := &encoder{}
	head.uint(uint64(len(n.objs) - n.start))
	for _, x := range n.objs[n.start:] {
		head.uint(objTag(x))
	}
	n.start = len(n.objs)
	return append(head.buf, body.buf...)
}

// NodeTable holds the decoded segments of a table that NodeEncoder wrote.
type NodeTable struct {
	// starts[i] is the first id of segment i; starts[len] ends the last.
	starts []uint64
	segs   [][]any
}

// NewNodeTable returns a table whose segment i holds the ids from
// starts[i] up to starts[i+1]; no segment is decoded yet.
func NewNodeTable(starts []uint64) *NodeTable {
	return &NodeTable{starts: starts, segs: make([][]any, len(starts)-1)}
}

// Obj returns the object of id and whether its segment is decoded.
func (t *NodeTable) Obj(id uint64) (any, bool) {
	i := segmentOf(t.starts, id)
	if i < 0 || t.segs[i] == nil {
		return nil, false
	}
	return t.segs[i][id-t.starts[i]], true
}

// segmentOf is the segment whose ids hold id, or -1.
func segmentOf(starts []uint64, id uint64) int {
	lo, hi := 0, len(starts)-1
	if hi < 0 || id < starts[0] || id >= starts[hi] {
		return -1
	}
	for lo+1 < hi {
		mid := (lo + hi) / 2
		if id < starts[mid] {
			hi = mid
		} else {
			lo = mid
		}
	}
	return lo
}

// Decode reads segment seg, which NodeEncoder.Bytes wrote. Its references
// to earlier segments must name decoded segments. On an error the table
// is unchanged.
func (t *NodeTable) Decode(seg int, data []byte) (err error) {
	d := &decoder{buf: data, table: t, start: t.starts[seg]}
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(decodeError); ok {
				err = e
				return
			}
			panic(r)
		}
	}()
	count := d.uint()
	if count != t.starts[seg+1]-t.starts[seg] {
		d.failf("segment of %d objects, want %d", count, t.starts[seg+1]-t.starts[seg])
	}
	if count > uint64(len(data)) {
		d.failf("syntax table of %d objects in %d bytes", count, len(data))
	}
	d.objs = make([]any, count)
	for i := range d.objs {
		x := newObj(d.uint())
		if x == nil {
			d.failf("bad object tag")
		}
		d.objs[i] = x
	}
	for _, x := range d.objs {
		d.decObj(x)
	}
	if d.pos != len(d.buf) {
		d.failf("%d bytes after the syntax table", len(d.buf)-d.pos)
	}
	t.segs[seg] = d.objs
	return nil
}

type encoder struct {
	buf   []byte
	table *NodeEncoder
}

func (e *encoder) id(x any) uint64 { return e.table.ID(x) }

func (e *encoder) uint(v uint64) { e.buf = binary.AppendUvarint(e.buf, v) }

func (e *encoder) int(v int64) { e.buf = binary.AppendVarint(e.buf, v) }

func (e *encoder) bool(v bool) {
	if v {
		e.buf = append(e.buf, 1)
	} else {
		e.buf = append(e.buf, 0)
	}
}

func (e *encoder) float(v float64) { e.uint(math.Float64bits(v)) }

func (e *encoder) string(v string) {
	e.uint(uint64(len(v)))
	e.buf = append(e.buf, v...)
}

func (e *encoder) failf(format string, args ...any) {
	panic(fmt.Sprintf("syntax: "+format, args...))
}

type decoder struct {
	buf  []byte
	pos  int
	objs []any
	// table and start resolve ids below this segment's first id, start.
	table *NodeTable
	start uint64
}

// decodeError is a malformed table.
type decodeError struct{ error }

func (d *decoder) failf(format string, args ...any) {
	panic(decodeError{errors.New(fmt.Sprintf("syntax: malformed table: "+format, args...))})
}

func (d *decoder) obj(id uint64) any {
	if id == 0 {
		return nil
	}
	if id >= d.start && id-d.start < uint64(len(d.objs)) {
		return d.objs[id-d.start]
	}
	if id < d.start {
		if x, ok := d.table.Obj(id); ok {
			return x
		}
	}
	d.failf("object id %d outside the decoded segments", id)
	return nil
}

func (d *decoder) uint() uint64 {
	v, n := binary.Uvarint(d.buf[d.pos:])
	if n <= 0 {
		d.failf("bad uvarint at %d", d.pos)
	}
	d.pos += n
	return v
}

func (d *decoder) int() int64 {
	v, n := binary.Varint(d.buf[d.pos:])
	if n <= 0 {
		d.failf("bad varint at %d", d.pos)
	}
	d.pos += n
	return v
}

func (d *decoder) bool() bool {
	if d.pos >= len(d.buf) {
		d.failf("short bool")
	}
	b := d.buf[d.pos]
	d.pos++
	return b != 0
}

func (d *decoder) float() float64 { return math.Float64frombits(d.uint()) }

func (d *decoder) string() string {
	n := d.uint()
	if n > uint64(len(d.buf)-d.pos) {
		d.failf("short string")
	}
	s := string(d.buf[d.pos : d.pos+int(n)])
	d.pos += int(n)
	return s
}
