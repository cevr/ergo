package check

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"go/constant"
	"math"
	"math/big"

	"ergo.local/prototype/internal/sketch/std"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The checked std snapshot (CHK1b): the universe, the prelude and every std
// package as the checker leaves them, in a binary form a later process
// installs instead of checking std from source. The codec is generated
// (export_gen.go and syntax's codec_gen.go, internal/sketch/exportgen);
// this file holds its primitives, the snapshot's root and the install.
// The bytes are only valid for the compiler that wrote them: the caller
// keys them on the std sources and the compiler's identity
// (internal/sketchfront, stdcache.go), and a snapshot that does not decode
// is refused, never half installed.

// stdSnapshot is the root of the export: the process-wide state that
// universe, prelude and Std hold after every std package is checked.
type stdSnapshot struct {
	universe  *Scope
	errorName *TypeName
	goPkg     *Package
	ref       *Named
	prelude   preludeResult
	paths     []string
	packages  []*Package
	nextID    int
}

// ExportStd checks the prelude and every std package (from source, unless
// this process installed a snapshot) and returns their snapshot. It fails
// when a std package does not check, or when the model holds a value the
// codec does not carry (a function or an error value).
func ExportStd() ([]byte, error) {
	prelude()
	s := &stdSnapshot{}
	for _, path := range std.Packages() {
		pkg, err := Std.Import(path)
		if err != nil {
			return nil, err
		}
		s.paths = append(s.paths, path)
		s.packages = append(s.packages, pkg)
	}
	s.universe = universeOnce.scope
	s.errorName = universeOnce.errorName
	s.goPkg = universeOnce.goPkg
	s.ref = universeOnce.ref
	s.prelude = preludeOnce.r
	nextID.Lock()
	s.nextID = nextID.n
	nextID.Unlock()
	return encodeSnapshot(s)
}

// snapshotMagic opens a snapshot, so a file of another format is refused.
const snapshotMagic = "ergo-std-snapshot-1\n"

func encodeSnapshot(s *stdSnapshot) (out []byte, err error) {
	e := newEncoder()
	defer func() {
		if r := recover(); r != nil {
			if x, ok := r.(exportError); ok {
				out, err = nil, x
				return
			}
			panic(r)
		}
	}()
	e.encPtrstdSnapshot(s)
	nodes := e.nodes.Bytes()
	buf := []byte(snapshotMagic)
	// Verify the complete payload before decoding sizes or graph edges.
	// Corrupt cache bytes must be a miss, not a changed checked model.
	buf = append(buf, make([]byte, sha256.Size)...)
	buf = binary.AppendUvarint(buf, uint64(len(nodes)))
	buf = append(buf, nodes...)
	buf = append(buf, e.buf...)
	sum := sha256.Sum256(buf[len(snapshotMagic)+sha256.Size:])
	copy(buf[len(snapshotMagic):], sum[:])
	return buf, nil
}

// ErrInstalled refuses a snapshot after this process has built its
// universe: the checked state is already in use.
var ErrInstalled = fmt.Errorf("check: the universe is already built; a std snapshot installs only before the first check")

// InstallStd decodes a snapshot ExportStd wrote and makes it this process's
// universe, prelude and std, so no std package is checked from source. It
// must run before the first check; after it, or on a snapshot that does not
// decode, it returns an error and changes nothing.
func InstallStd(data []byte) error {
	s, err := decodeSnapshot(data)
	if err != nil {
		return err
	}
	installed := false
	universeOnce.Do(func() {
		installed = true
		universeOnce.scope = s.universe
		universeOnce.errorName = s.errorName
		universeOnce.goPkg = s.goPkg
		universeOnce.ref = s.ref
	})
	if !installed {
		return ErrInstalled
	}
	preludeOnce.Do(func() { preludeOnce.r = s.prelude })
	imp := Std.(*stdImporter)
	imp.mu.Lock()
	for i, path := range s.paths {
		e := &stdEntry{pkg: s.packages[i]}
		e.once.Do(func() {})
		imp.entries[path] = e
	}
	imp.mu.Unlock()
	nextID.Lock()
	nextID.n = max(nextID.n, s.nextID)
	nextID.Unlock()
	return nil
}

func decodeSnapshot(data []byte) (s *stdSnapshot, err error) {
	if len(data) < len(snapshotMagic) || string(data[:len(snapshotMagic)]) != snapshotMagic {
		return nil, fmt.Errorf("check: not a std snapshot")
	}
	data = data[len(snapshotMagic):]
	if len(data) < sha256.Size {
		return nil, fmt.Errorf("check: short std snapshot checksum")
	}
	sum := sha256.Sum256(data[sha256.Size:])
	if !bytes.Equal(data[:sha256.Size], sum[:]) {
		return nil, fmt.Errorf("check: std snapshot checksum mismatch")
	}
	data = data[sha256.Size:]
	n, k := binary.Uvarint(data)
	if k <= 0 || n > uint64(len(data)-k) {
		return nil, fmt.Errorf("check: malformed std snapshot")
	}
	nodes, err := syntax.DecodeNodes(data[k : k+int(n)])
	if err != nil {
		return nil, err
	}
	d := newDecoder(data[k+int(n):], nodes)
	defer func() {
		if r := recover(); r != nil {
			if x, ok := r.(exportError); ok {
				s, err = nil, x
				return
			}
			panic(r)
		}
	}()
	s = d.decPtrstdSnapshot()
	if d.pos != len(d.buf) {
		d.failf("%d bytes after the snapshot", len(d.buf)-d.pos)
	}
	d.afterDecode()
	return s, nil
}

// ResetStdForTest forgets this process's universe, prelude, std packages
// and id counter, so a test can check the same program from source and
// from a snapshot in one process (the equality property of CHK1b). Only
// tests call it, while no check runs; the compiler never does.
func ResetStdForTest() {
	universeOnce = universeState{}
	preludeOnce = preludeState{}
	Std = newStdImporter()
	nextID.Lock()
	nextID.n = 0
	nextID.Unlock()
}

// exportError is a value the codec cannot carry, or malformed bytes.
type exportError struct{ error }

// externals are the process-wide singletons a snapshot names instead of
// copying: the basic types, NoValue and the std importer.
func externals() []any {
	out := []any{NoValue, Std}
	for _, b := range Typ {
		out = append(out, b)
	}
	return out
}

type encoder struct {
	buf   []byte
	ids   map[any]uint64
	ext   map[any]uint64
	nodes *syntax.NodeEncoder
}

func newEncoder() *encoder {
	e := &encoder{ids: map[any]uint64{}, ext: map[any]uint64{}, nodes: syntax.NewNodeEncoder()}
	for i, x := range externals() {
		e.ext[x] = uint64(i)
	}
	return e
}

func (e *encoder) failf(format string, args ...any) {
	panic(exportError{fmt.Errorf("check: export: "+format, args...)})
}

// ref writes a pointer's reference and reports whether it is complete: nil
// (0), an external (1, index), an object written before (2, id). Otherwise
// it writes 3, gives the object the next id, and the caller writes its
// contents.
func (e *encoder) ref(v any, isNil bool) bool {
	if isNil {
		e.uint(0)
		return true
	}
	if i, ok := e.ext[v]; ok {
		e.uint(1)
		e.uint(i)
		return true
	}
	if id, ok := e.ids[v]; ok {
		e.uint(2)
		e.uint(id)
		return true
	}
	e.ids[v] = uint64(len(e.ids))
	e.uint(3)
	return false
}

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

func (e *encoder) bytes(v []byte) {
	e.uint(uint64(len(v)))
	e.buf = append(e.buf, v...)
}

func (e *encoder) gob(v interface{ GobEncode() ([]byte, error) }) {
	b, err := v.GobEncode()
	if err != nil {
		e.failf("%v", err)
	}
	e.bytes(b)
}

func (e *encoder) bigInt(v *big.Int) {
	if v == nil {
		e.bool(false)
		return
	}
	e.bool(true)
	e.gob(v)
}

// constant writes a go/constant value as its kind and its exact value.
func (e *encoder) constant(v constant.Value) {
	if v == nil {
		e.uint(0)
		return
	}
	switch x := constant.Val(v).(type) {
	case bool:
		e.uint(1)
		e.bool(x)
	case string:
		e.uint(2)
		e.string(x)
	case int64:
		e.uint(3)
		e.int(x)
	case *big.Int:
		e.uint(4)
		e.gob(x)
	case *big.Rat:
		e.uint(5)
		e.gob(x)
	case *big.Float:
		e.uint(6)
		e.gob(x)
	default:
		if v.Kind() == constant.Unknown {
			e.uint(7)
			return
		}
		e.failf("a %s constant is not exported", v.Kind())
	}
}

type decoder struct {
	buf      []byte
	pos      int
	refs     []any
	ext      []any
	nodes    []any
	checkers []*checker
}

func newDecoder(buf []byte, nodes []any) *decoder {
	return &decoder{buf: buf, nodes: nodes, ext: externals()}
}

func (d *decoder) failf(format string, args ...any) {
	panic(exportError{fmt.Errorf("check: malformed std snapshot: "+format, args...)})
}

// ref reads a reference: the object, or fresh when the caller allocates
// the next object (add) and reads its contents.
func (d *decoder) ref() (any, bool) {
	switch d.uint() {
	case 0:
		return nil, false
	case 1:
		i := d.uint()
		if i >= uint64(len(d.ext)) {
			d.failf("external %d", i)
		}
		return d.ext[i], false
	case 2:
		id := d.uint()
		if id >= uint64(len(d.refs)) {
			d.failf("object %d of %d", id, len(d.refs))
		}
		return d.refs[id], false
	case 3:
		return nil, true
	}
	d.failf("bad reference tag")
	return nil, false
}

// add records the object a fresh reference allocated.
func (d *decoder) add(v any) {
	d.refs = append(d.refs, v)
	if c, ok := v.(*checker); ok {
		d.checkers = append(d.checkers, c)
	}
}

// afterDecode rebuilds what the codec does not carry: each checker's type
// table substitutes rows through its checker (newChecker).
func (d *decoder) afterDecode() {
	for _, c := range d.checkers {
		if c.types != nil {
			c.types.substRow = c.substRow
		}
	}
}

func (d *decoder) node() any {
	id := d.uint()
	if id == 0 {
		return nil
	}
	if id > uint64(len(d.nodes)) {
		d.failf("syntax object %d of %d", id, len(d.nodes))
	}
	return d.nodes[id-1]
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

func (d *decoder) bytes() []byte {
	n := d.uint()
	if n > uint64(len(d.buf)-d.pos) {
		d.failf("short bytes")
	}
	b := d.buf[d.pos : d.pos+int(n)]
	d.pos += int(n)
	return b
}

func (d *decoder) string() string { return string(d.bytes()) }

func (d *decoder) gob(v interface{ GobDecode([]byte) error }) {
	if err := v.GobDecode(d.bytes()); err != nil {
		d.failf("%v", err)
	}
}

func (d *decoder) bigInt() *big.Int {
	if !d.bool() {
		return nil
	}
	v := new(big.Int)
	d.gob(v)
	return v
}

func (d *decoder) constant() constant.Value {
	switch d.uint() {
	case 0:
		return nil
	case 1:
		return constant.MakeBool(d.bool())
	case 2:
		return constant.MakeString(d.string())
	case 3:
		return constant.MakeInt64(d.int())
	case 4:
		v := new(big.Int)
		d.gob(v)
		return constant.Make(v)
	case 5:
		v := new(big.Rat)
		d.gob(v)
		return constant.Make(v)
	case 6:
		v := new(big.Float)
		d.gob(v)
		return constant.Make(v)
	case 7:
		return constant.MakeUnknown()
	}
	d.failf("bad constant tag")
	return nil
}
