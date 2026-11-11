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

// NodeEncoder writes a table of syntax objects.
type NodeEncoder struct {
	ids  map[any]uint64
	objs []any
}

// NewNodeEncoder returns an empty table.
func NewNodeEncoder() *NodeEncoder { return &NodeEncoder{ids: map[any]uint64{}} }

// ID returns the id of the syntax object x (a non-nil pointer to a syntax
// struct), giving it the next id the first time.
func (n *NodeEncoder) ID(x any) uint64 {
	if id, ok := n.ids[x]; ok {
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

// Bytes writes the table: the count, each object's tag, then each
// object's fields. A field may name an object no caller named; it joins
// the table and is written in turn.
func (n *NodeEncoder) Bytes() []byte {
	body := &encoder{table: n}
	for i := 0; i < len(n.objs); i++ {
		body.encObj(n.objs[i])
	}
	head := &encoder{}
	head.uint(uint64(len(n.objs)))
	for _, x := range n.objs {
		head.uint(objTag(x))
	}
	return append(head.buf, body.buf...)
}

// DecodeNodes reads a table that NodeEncoder.Bytes wrote, and returns its
// objects by id: the object of id i is at index i-1.
func DecodeNodes(data []byte) (objs []any, err error) {
	d := &decoder{buf: data}
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(decodeError); ok {
				objs, err = nil, e
				return
			}
			panic(r)
		}
	}()
	count := d.uint()
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
	return d.objs, nil
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
	if id > uint64(len(d.objs)) {
		d.failf("object id %d of %d", id, len(d.objs))
	}
	return d.objs[id-1]
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
