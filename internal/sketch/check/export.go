package check

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"go/constant"
	"math"
	"math/big"
	"slices"
	"sync"

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

// stdBase is the first segment of the export: the process-wide state that
// universe and prelude hold, and the id counter after every std package is
// checked.
type stdBase struct {
	universe  *Scope
	errorName *TypeName
	goPkg     *Package
	ref       *Named
	prelude   preludeResult
	nextID    int
}

// stdSnapshot is the export: the base, then every std package, each a
// segment of its own, in an order where a package comes after the
// packages it imports. A segment may name objects of earlier segments
// only, and records which; a reader decodes the base and then, on first
// import, a package's segment and the segments it names (lazily: a check
// decodes only the std its imports reach, as Go's importer reads the
// export data of each imported package).
type stdSnapshot struct {
	base     *stdBase
	paths    []string
	packages []*Package
}

// ExportStd checks the prelude and every std package (from source, unless
// this process installed a snapshot) and returns their snapshot. It fails
// when a std package does not check, or when the model holds a value the
// codec does not carry (a function or an error value).
func ExportStd() ([]byte, error) {
	prelude()
	imp := Std.(*stdImporter)
	for _, path := range std.Packages() {
		if _, err := Std.Import(path); err != nil {
			return nil, err
		}
	}
	s := &stdSnapshot{base: &stdBase{
		universe:  universeOnce.scope,
		errorName: universeOnce.errorName,
		goPkg:     universeOnce.goPkg,
		ref:       universeOnce.ref,
		prelude:   preludeOnce.r,
	}}
	// The order the packages became available is an order where every
	// package follows its imports.
	imp.mu.Lock()
	for _, path := range imp.order {
		s.paths = append(s.paths, path)
		s.packages = append(s.packages, imp.entries[path].pkg)
	}
	imp.mu.Unlock()
	nextID.Lock()
	s.base.nextID = nextID.n
	nextID.Unlock()
	return encodeSnapshot(s)
}

// snapshotMagic opens a snapshot, so a file of another format is refused.
const snapshotMagic = "ergo-std-snapshot-2\n"

// segment is a segment's entry in the snapshot's header.
type segment struct {
	path string // "" for the base
	// objStart and nodeStart are the first object id and syntax id of the
	// segment; the next segment's starts end it.
	objStart, nodeStart uint64
	// deps are the earlier segments whose objects the segment names.
	deps []int
	// data is the segment's bytes: its syntax table, then its objects.
	data []byte
	sum  [sha256.Size]byte
}

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
	var segs []segment
	write := func(path string, body func()) {
		seg := segment{path: path, objStart: e.next, nodeStart: e.nodes.Next()}
		e.objStarts = append(e.objStarts, seg.objStart)
		e.nodeStarts = append(e.nodeStarts, seg.nodeStart)
		e.deps = map[int]bool{}
		e.buf = nil
		body()
		objects := e.buf
		nodes := e.nodes.Bytes()
		for dep := range e.deps {
			seg.deps = append(seg.deps, dep)
		}
		slices.Sort(seg.deps)
		seg.data = binary.AppendUvarint(nil, uint64(len(nodes)))
		seg.data = append(seg.data, nodes...)
		seg.data = append(seg.data, objects...)
		seg.sum = sha256.Sum256(seg.data)
		segs = append(segs, seg)
	}
	write("", func() { e.encPtrstdBase(s.base) })
	for i, pkg := range s.packages {
		write(s.paths[i], func() { e.encPtrPackage(pkg) })
	}
	// The header: every segment's path, starts, deps, size and checksum,
	// and the ends of the last segment. Its own checksum is verified before
	// it is read; each segment's before the segment is decoded, so corrupt
	// bytes are a miss, never a changed checked model.
	h := binary.AppendUvarint(nil, uint64(len(segs)))
	for _, seg := range segs {
		h = binary.AppendUvarint(h, uint64(len(seg.path)))
		h = append(h, seg.path...)
		h = binary.AppendUvarint(h, seg.objStart)
		h = binary.AppendUvarint(h, seg.nodeStart)
		h = binary.AppendUvarint(h, uint64(len(seg.deps)))
		for _, dep := range seg.deps {
			h = binary.AppendUvarint(h, uint64(dep))
		}
		h = binary.AppendUvarint(h, uint64(len(seg.data)))
		h = append(h, seg.sum[:]...)
	}
	h = binary.AppendUvarint(h, e.next)
	h = binary.AppendUvarint(h, e.nodes.Next())
	sum := sha256.Sum256(h)
	buf := []byte(snapshotMagic)
	buf = append(buf, sum[:]...)
	buf = binary.AppendUvarint(buf, uint64(len(h)))
	buf = append(buf, h...)
	for _, seg := range segs {
		buf = append(buf, seg.data...)
	}
	return buf, nil
}

// ErrInstalled refuses a snapshot after this process has built its
// universe: the checked state is already in use.
var ErrInstalled = fmt.Errorf("check: the universe is already built; a std snapshot installs only before the first check")

// InstallStd reads a snapshot ExportStd wrote: it decodes the base and
// makes it this process's universe and prelude, and lets Std decode each
// std package on its first import, so no std package is checked from
// source. It must run before the first check; after it, or on a snapshot
// whose header or base does not decode, it returns an error and changes
// nothing. A package segment that does not decode later is checked from
// source, with every segment that names it.
func InstallStd(data []byte) error {
	snap, base, err := openSnapshot(data)
	if err != nil {
		return err
	}
	installed := false
	universeOnce.Do(func() {
		installed = true
		universeOnce.scope = base.universe
		universeOnce.errorName = base.errorName
		universeOnce.goPkg = base.goPkg
		universeOnce.ref = base.ref
	})
	if !installed {
		return ErrInstalled
	}
	preludeOnce.Do(func() { preludeOnce.r = base.prelude })
	imp := Std.(*stdImporter)
	imp.mu.Lock()
	imp.snap = snap
	imp.mu.Unlock()
	nextID.Lock()
	nextID.n = max(nextID.n, base.nextID)
	nextID.Unlock()
	return nil
}

// snapshot is an installed snapshot: its header, and the segments decoded
// so far. mu guards the decoding; it is taken after no other lock.
type snapshot struct {
	mu     sync.Mutex
	segs   []segment
	byPath map[string]int
	objs   objTable
	nodes  *syntax.NodeTable
	// done[i] is set once segment i is decoded (pkgs[i] its package);
	// failed[i] once it, or a segment it names, did not decode.
	done, failed []bool
	pkgs         []*Package
}

// objTable holds the decoded segments' objects by id.
type objTable struct {
	starts []uint64
	segs   [][]any
}

func (t *objTable) obj(id uint64) (any, bool) {
	lo, hi := 0, len(t.starts)-1
	if id < t.starts[0] || id >= t.starts[hi] {
		return nil, false
	}
	for lo+1 < hi {
		mid := (lo + hi) / 2
		if id < t.starts[mid] {
			hi = mid
		} else {
			lo = mid
		}
	}
	if t.segs[lo] == nil {
		return nil, false
	}
	return t.segs[lo][id-t.starts[lo]], true
}

// openSnapshot reads a snapshot's header and decodes its base.
func openSnapshot(data []byte) (*snapshot, *stdBase, error) {
	malformed := fmt.Errorf("check: malformed std snapshot")
	if len(data) < len(snapshotMagic) || string(data[:len(snapshotMagic)]) != snapshotMagic {
		return nil, nil, fmt.Errorf("check: not a std snapshot")
	}
	data = data[len(snapshotMagic):]
	if len(data) < sha256.Size {
		return nil, nil, fmt.Errorf("check: short std snapshot checksum")
	}
	want := data[:sha256.Size]
	data = data[sha256.Size:]
	n, k := binary.Uvarint(data)
	if k <= 0 || n > uint64(len(data)-k) {
		return nil, nil, malformed
	}
	h := data[k : k+int(n)]
	if sum := sha256.Sum256(h); !bytes.Equal(want, sum[:]) {
		return nil, nil, fmt.Errorf("check: std snapshot checksum mismatch")
	}
	body := data[k+int(n):]
	r := headerReader{buf: h}
	count := r.uint()
	if count == 0 || count > uint64(len(h)) {
		return nil, nil, malformed
	}
	s := &snapshot{byPath: map[string]int{}}
	off := uint64(0)
	for i := uint64(0); i < count && r.err == nil; i++ {
		seg := segment{path: r.string(), objStart: r.uint(), nodeStart: r.uint()}
		deps := r.uint()
		if deps > i {
			r.err = malformed
			break
		}
		for range deps {
			dep := r.uint()
			if dep >= i {
				r.err = malformed
			}
			seg.deps = append(seg.deps, int(dep))
		}
		size := r.uint()
		copy(seg.sum[:], r.bytes(sha256.Size))
		if size > uint64(len(body))-off {
			r.err = malformed
			break
		}
		seg.data = body[off : off+size]
		off += size
		if i > 0 {
			if seg.path == "" {
				r.err = malformed
			}
			s.byPath[seg.path] = int(i)
		}
		s.segs = append(s.segs, seg)
	}
	objEnd, nodeEnd := r.uint(), r.uint()
	if r.err != nil || r.pos != len(h) || off != uint64(len(body)) || s.segs[0].path != "" {
		return nil, nil, malformed
	}
	objStarts := make([]uint64, 0, len(s.segs)+1)
	nodeStarts := make([]uint64, 0, len(s.segs)+1)
	for _, seg := range s.segs {
		objStarts = append(objStarts, seg.objStart)
		nodeStarts = append(nodeStarts, seg.nodeStart)
	}
	objStarts = append(objStarts, objEnd)
	nodeStarts = append(nodeStarts, nodeEnd)
	for i := 1; i < len(objStarts); i++ {
		if objStarts[i] < objStarts[i-1] || nodeStarts[i] < nodeStarts[i-1] {
			return nil, nil, malformed
		}
	}
	if nodeStarts[0] != 1 || objStarts[0] != 0 {
		return nil, nil, malformed
	}
	s.objs = objTable{starts: objStarts, segs: make([][]any, len(s.segs))}
	s.nodes = syntax.NewNodeTable(nodeStarts)
	s.done = make([]bool, len(s.segs))
	s.failed = make([]bool, len(s.segs))
	s.pkgs = make([]*Package, len(s.segs))
	var base *stdBase
	if err := s.decode(0, func(d *decoder) { base = d.decPtrstdBase() }); err != nil {
		return nil, nil, err
	}
	return s, base, nil
}

// load returns the std package at path from the snapshot, decoding its
// segment and the segments it names, or false when the snapshot does not
// hold it or a segment does not decode (the caller checks it from source).
// count receives each package segment decoded.
func (s *snapshot) load(path string, count func(string)) (*Package, bool) {
	i, ok := s.byPath[path]
	if !ok {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.need(i, count) {
		return nil, false
	}
	return s.pkgs[i], true
}

// need decodes segment i after the segments it names, and reports whether
// it is decoded.
func (s *snapshot) need(i int, count func(string)) bool {
	if s.done[i] {
		return true
	}
	if s.failed[i] {
		return false
	}
	for _, dep := range s.segs[i].deps {
		if !s.need(dep, count) {
			s.failed[i] = true
			return false
		}
	}
	err := s.decode(i, func(d *decoder) { s.pkgs[i] = d.decPtrPackage() })
	if err != nil || s.pkgs[i] == nil || s.pkgs[i].Path != s.segs[i].path {
		s.failed[i] = true
		return false
	}
	count(s.segs[i].path)
	return true
}

// decode verifies segment i and decodes it with read. On an error the
// segment stays undecoded.
func (s *snapshot) decode(i int, read func(*decoder)) (err error) {
	seg := s.segs[i]
	if sum := sha256.Sum256(seg.data); sum != seg.sum {
		return fmt.Errorf("check: std snapshot segment %q checksum mismatch", seg.path)
	}
	n, k := binary.Uvarint(seg.data)
	if k <= 0 || n > uint64(len(seg.data)-k) {
		return fmt.Errorf("check: malformed std snapshot segment %q", seg.path)
	}
	if err := s.nodes.Decode(i, seg.data[k:k+int(n)]); err != nil {
		return err
	}
	d := &decoder{buf: seg.data[k+int(n):], ext: externals(), nodes: s.nodes, table: &s.objs, seg: i, start: seg.objStart}
	defer func() {
		if r := recover(); r != nil {
			if x, ok := r.(exportError); ok {
				err = x
				return
			}
			panic(r)
		}
	}()
	read(d)
	if d.pos != len(d.buf) {
		d.failf("%d bytes after the segment", len(d.buf)-d.pos)
	}
	if uint64(len(d.refs)) != s.objs.starts[i+1]-seg.objStart {
		d.failf("segment of %d objects, want %d", len(d.refs), s.objs.starts[i+1]-seg.objStart)
	}
	s.objs.segs[i] = d.refs
	s.done[i] = true
	d.afterDecode()
	return nil
}

// headerReader reads a snapshot header; the first error stops it.
type headerReader struct {
	buf []byte
	pos int
	err error
}

func (r *headerReader) uint() uint64 {
	if r.err != nil {
		return 0
	}
	v, n := binary.Uvarint(r.buf[r.pos:])
	if n <= 0 {
		r.err = fmt.Errorf("check: malformed std snapshot header")
		return 0
	}
	r.pos += n
	return v
}

func (r *headerReader) bytes(n int) []byte {
	if r.err != nil || n > len(r.buf)-r.pos {
		r.err = fmt.Errorf("check: malformed std snapshot header")
		return nil
	}
	b := r.buf[r.pos : r.pos+n]
	r.pos += n
	return b
}

func (r *headerReader) string() string {
	n := r.uint()
	if n > uint64(len(r.buf)) {
		r.err = fmt.Errorf("check: malformed std snapshot header")
		return ""
	}
	return string(r.bytes(int(n)))
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
	next  uint64 // the id of the next new object
	ext   map[any]uint64
	nodes *syntax.NodeEncoder
	// objStarts and nodeStarts are the first ids of the segments written,
	// and of the current one; deps collects the earlier segments the
	// current one names.
	objStarts, nodeStarts []uint64
	deps                  map[int]bool
}

func newEncoder() *encoder {
	e := &encoder{ids: map[any]uint64{}, ext: map[any]uint64{}, nodes: syntax.NewNodeEncoder()}
	for i, x := range externals() {
		e.ext[x] = uint64(i)
	}
	e.nodes.Below = func(id uint64) { e.deps[segmentOf(e.nodeStarts, id)] = true }
	return e
}

// segmentOf is the segment whose ids, from starts[i] up to starts[i+1],
// hold id; the last segment is open.
func segmentOf(starts []uint64, id uint64) int {
	i, _ := slices.BinarySearch(starts, id+1)
	return i - 1
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
		if cur := e.objStarts[len(e.objStarts)-1]; id < cur {
			e.deps[segmentOf(e.objStarts, id)] = true
		}
		e.uint(2)
		e.uint(id)
		return true
	}
	e.ids[v] = e.next
	e.next++
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

// decoder reads one segment: refs are its objects, in id order from start;
// table and nodes resolve the ids of earlier, decoded segments.
type decoder struct {
	buf      []byte
	pos      int
	refs     []any
	ext      []any
	nodes    *syntax.NodeTable
	table    *objTable
	seg      int
	start    uint64
	checkers []*checker
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
		if id >= d.start {
			if id-d.start >= uint64(len(d.refs)) {
				d.failf("object %d of %d", id, d.start+uint64(len(d.refs)))
			}
			return d.refs[id-d.start], false
		}
		x, ok := d.table.obj(id)
		if !ok {
			d.failf("object %d outside the decoded segments", id)
		}
		return x, false
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
	x, ok := d.nodes.Obj(id)
	if !ok {
		d.failf("syntax object %d outside the decoded segments", id)
	}
	return x
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
