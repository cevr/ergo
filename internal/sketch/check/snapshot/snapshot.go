// Package snapshot is the checked std snapshot (CHK1b): the universe, the
// prelude and every std package as the checker leaves them, in a binary
// form a later process installs instead of checking std from source. The
// codec is generated (codec_gen.go and syntax's codec_gen.go,
// internal/sketch/exportgen); this file holds its primitives, the
// snapshot's root and the install. The bytes are only valid for the
// compiler that wrote them: the caller keys them on the compiler's
// identity, whose bytes embed the std sources (internal/sketchfront,
// stdcache.go), and a snapshot that does not decode is refused, never half
// installed.
//
// It is a package of its own (SELF-CODEC), not a part of package check:
// the codec is a third of check's compile, and only sketchfront uses it,
// so out of check it compiles beside the packages that import check
// instead of before them, off the critical path of ergo's own build
// (research GRAPH-PERF, SELF-SPLIT). The codec reaches check's unexported
// names through check's codec_access_gen.go; check reaches an installed
// snapshot through check.SnapshotSource.
package snapshot

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"go/constant"
	"io"
	"math"
	"math/big"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/reflectread"
	"ergo.local/prototype/internal/sketch/syntax"
)

// stdSnapshot is the export: the base, then every std package, each a
// segment of its own, in an order where a package comes after the
// packages it imports. A segment may name objects of earlier segments
// only, and records which; a reader decodes the base and then, on first
// import, a package's segment and the segments it names (lazily: a check
// decodes only the std its imports reach, as Go's importer reads the
// export data of each imported package).
type stdSnapshot struct {
	base     *check.Codec_stdBase
	paths    []string
	packages []*check.Package
}

// ExportStd checks the prelude and every std package (from source, unless
// this process installed a snapshot) and returns their snapshot. It fails
// when a std package does not check, or when the model holds a value the
// codec does not carry (a function or an error value).
func ExportStd() ([]byte, error) { return ExportSnapshot(nil, nil) }

// ExportSnapshot is ExportStd with the checked packages pkgs of required
// modules after std, pkgs[i] a segment named keys[i] (check.ModuleSegment),
// in an order where a package follows the packages it imports (NP5: std is
// the toolchain's module, and a required module, read-only and named by
// its content hash, gets the same snapshot). A package must be checked
// with check.ModuleImporter, which the codec carries, and declare no
// derive.
func ExportSnapshot(pkgs []*check.Package, keys []string) ([]byte, error) {
	if len(pkgs) != len(keys) {
		return nil, fmt.Errorf("check: %d packages for %d segment keys", len(pkgs), len(keys))
	}
	seen := map[string]bool{}
	for i, pkg := range pkgs {
		switch {
		case pkg == nil || pkg.Std():
			return nil, fmt.Errorf("check: segment %q is not a checked module package", keys[i])
		case check.SegmentPackage(keys[i]) != pkg.Path || !strings.Contains(keys[i], "\x00") || seen[keys[i]]:
			return nil, fmt.Errorf("check: %q is not the one segment key of package %s", keys[i], pkg.Path)
		case !pkg.Derive.Empty():
			return nil, fmt.Errorf("check: package %s has a derive record, which a snapshot does not carry", pkg.Path)
		}
		seen[keys[i]] = true
	}
	base, paths, packages, err := check.StdSnapshotNow()
	if err != nil {
		return nil, err
	}
	s := &stdSnapshot{base: base, paths: append(paths, keys...), packages: append(packages, pkgs...)}
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
	// data is the segment's bytes when it is written: its syntax table,
	// then its objects. A reader has off and size instead, the bytes'
	// place in the snapshot, and reads them when it decodes the segment.
	data      []byte
	off, size uint64
	sum       [sha256.Size]byte
}

// segment writes one segment: its starts, the objects body encodes and
// the syntax nodes they name, and the earlier segments it names.
func (e *encoder) segment(path string, body func()) segment {
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
	return seg
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
		segs = append(segs, e.segment(path, body))
	}
	// Each segment first reaches its files, so its syntax ids follow the
	// trees, and a map keyed by nodes is written in tree order
	// (order.go).
	reach := func(pkg *check.Package) {
		if pkg != nil {
			for _, f := range pkg.Files() {
				e.nodes.Reach(f)
			}
			// Then the declarations the checker made (an inline variant's,
			// whose type parameters are a copy of its union's), in the
			// package's order: the copies of one origin tie in a map's
			// order (c.origins), so they get their ids here.
			for _, tn := range pkg.Types {
				if tn.Decl != nil {
					e.nodes.Reach(tn.Decl)
				}
			}
		}
	}
	write("", func() { reach(check.BasePrelude(s.base)); e.encPtrstdBase(s.base) })
	for i, pkg := range s.packages {
		write(s.paths[i], func() { reach(pkg); e.encPtrPackage(pkg) })
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

// InstallStd reads a snapshot ExportStd wrote, of size bytes, from r: it
// reads the header and decodes the base and makes it this process's
// universe and prelude, and lets check.Std read and decode each std
// package on its first import, so no std package is checked from source
// and a check reads only the segments its imports reach (CHK1c: memory,
// like work, grows with the imports, never with the std). r must stay
// readable and unchanged while the process checks: the compiler passes the
// open file, which a later rename of the cache file does not change. It
// must run before the first check; after it (check.ErrInstalled), or on a
// snapshot whose header or base does not decode, it returns an error and
// changes nothing. A package segment that does not read or decode later is
// checked from source, with every segment that names it.
func InstallStd(r io.ReaderAt, size int64) error {
	growStack(0)
	snap, base, err := openSnapshot(r, size)
	if err != nil {
		return err
	}
	return check.InstallBase(base, snap)
}

// growStack grows the calling goroutine's stack in one step, while few
// frames are on it, before the decode and the check that follows it
// recurse deeply. Go grows a stack by doubling and copying, and each copy
// reads the stack maps of every frame on the stack; when the stack grows
// inside the decoder's recursion, the copies read the maps of the
// decoder's and the checker's functions, pages of the compiler's binary
// that the process then holds. One early growth reads almost none of them
// (CHK1c, measured on a check with no std import: 192 fewer pages of the
// binary resident, docs/receipts/check-speed.md). The frame stays below the
// compiler's limit for a stack variable, so pad is on the stack.
// TestInstallGrowsTheStackOnce holds it: after the install, the first
// check, with its lazy decodes, moves the stack no more.
//
//go:noinline
func growStack(i int) byte {
	var pad [120 << 10]byte
	pad[i] = 1
	return lastByte(pad[:])
}

//go:noinline
func lastByte(b []byte) byte { return b[len(b)-1] }

// snapshot is an installed snapshot: its header, and the segments decoded
// so far. mu guards the decoding; it is taken after no other lock.
type snapshot struct {
	mu sync.Mutex
	// r holds the snapshot's bytes; read counts the bytes read from it
	// (Work.StdSnapshotBytesRead).
	r      io.ReaderAt
	read   atomic.Int64
	segs   []segment
	byPath map[string]int
	objs   objTable
	nodes  *syntax.NodeTable
	// done[i] is set once segment i is decoded (pkgs[i] its package);
	// failed[i] once it, or a segment it names, did not decode.
	done, failed []bool
	pkgs         []*check.Package
}

// BytesRead counts the bytes read from the snapshot (check.SnapshotSource).
func (s *snapshot) BytesRead() int64 { return s.read.Load() }

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

// openSnapshot reads a snapshot's header and decodes its base. It reads
// the header and the base alone; a package's segment is read when it is
// decoded (need).
func openSnapshot(src io.ReaderAt, size int64) (*snapshot, *check.Codec_stdBase, error) {
	malformed := fmt.Errorf("check: malformed std snapshot")
	s := &snapshot{r: src, byPath: map[string]int{}}
	if size < 0 {
		return nil, nil, malformed
	}
	// The prefix: the magic, the header's checksum and its length.
	data, err := s.readAt(0, min(uint64(size), uint64(len(snapshotMagic)+sha256.Size+binary.MaxVarintLen64)))
	if err != nil {
		return nil, nil, err
	}
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
	start := uint64(len(snapshotMagic) + sha256.Size + k)
	if k <= 0 || n > uint64(size)-start {
		return nil, nil, malformed
	}
	// The prefix may hold the header's first bytes; read only the rest.
	h := data[k:]
	if uint64(len(h)) >= n {
		h = h[:n]
	} else {
		rest, err := s.readAt(start+uint64(len(h)), n-uint64(len(h)))
		if err != nil {
			return nil, nil, err
		}
		h = append(h, rest...)
	}
	if sum := sha256.Sum256(h); !bytes.Equal(want, sum[:]) {
		return nil, nil, fmt.Errorf("check: std snapshot checksum mismatch")
	}
	bodyStart := start + n
	bodySize := uint64(size) - bodyStart
	r := headerReader{buf: h}
	count := r.uint()
	if count == 0 || count > uint64(len(h)) {
		return nil, nil, malformed
	}
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
		if size > bodySize-off {
			r.err = malformed
			break
		}
		seg.off, seg.size = bodyStart+off, size
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
	if r.err != nil || r.pos != len(h) || off != bodySize || s.segs[0].path != "" {
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
	s.pkgs = make([]*check.Package, len(s.segs))
	var base *check.Codec_stdBase
	if err := s.decode(0, func(d *decoder) { base = d.decPtrstdBase() }); err != nil {
		return nil, nil, err
	}
	return s, base, nil
}

// Load returns the package of segment key path from the snapshot, decoding
// its segment and the segments it names, or false when the snapshot does
// not hold it or a segment does not decode (the caller checks it from
// source). count receives each package segment decoded, with its package
// (check.SnapshotSource).
func (s *snapshot) Load(path string, count func(string, *check.Package)) (*check.Package, bool) {
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
func (s *snapshot) need(i int, count func(string, *check.Package)) bool {
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
	if err != nil || s.pkgs[i] == nil || s.pkgs[i].Path != check.SegmentPackage(s.segs[i].path) {
		s.failed[i] = true
		return false
	}
	count(s.segs[i].path, s.pkgs[i])
	return true
}

// readAt reads n bytes at off from the snapshot and counts them.
func (s *snapshot) readAt(off, n uint64) ([]byte, error) {
	if n > math.MaxInt-1 || off > math.MaxInt64 {
		return nil, fmt.Errorf("check: malformed std snapshot")
	}
	buf := make([]byte, n)
	k, err := s.r.ReadAt(buf, int64(off))
	s.read.Add(int64(k))
	if k == len(buf) {
		return buf, nil
	}
	return nil, fmt.Errorf("check: reading the std snapshot: %v", err)
}

// decode reads and verifies segment i and decodes it with read. On an
// error the segment stays undecoded. The decoded model holds no slice of
// the segment's bytes (the codec copies strings), so they are garbage
// once the segment is decoded.
func (s *snapshot) decode(i int, read func(*decoder)) (err error) {
	seg := s.segs[i]
	data, err := s.readAt(seg.off, seg.size)
	if err != nil {
		return err
	}
	if sum := sha256.Sum256(data); sum != seg.sum {
		return fmt.Errorf("check: std snapshot segment %q checksum mismatch", seg.path)
	}
	n, k := binary.Uvarint(data)
	if k <= 0 || n > uint64(len(data)-k) {
		return fmt.Errorf("check: malformed std snapshot segment %q", seg.path)
	}
	if err := s.nodes.Decode(i, data[k:k+int(n)]); err != nil {
		return err
	}
	d := &decoder{buf: data[k+int(n):], ext: externals(), nodes: s.nodes, table: &s.objs, seg: i, start: seg.objStart}
	// The segment holds exactly this many objects, each of at least one
	// byte; refs grows to it without copies.
	d.refs = make([]any, 0, min(s.objs.starts[i+1]-seg.objStart, uint64(len(d.buf))))
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

// exportError is a value the codec cannot carry, or malformed bytes.
type exportError struct{ error }

// externals are the process-wide singletons a snapshot names instead of
// copying: the basic types, NoValue and the std importer.
func externals() []any {
	out := []any{check.NoValue, check.Std}
	for _, b := range check.Typ {
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
	// crossRef, when set (UserExport), sees each reference to an object
	// of an earlier segment, with that segment's index.
	crossRef func(seg int, v any)
	// symbolic, when set (UserExport), names an object of an earlier
	// segment by its object path instead of its id (tag 4), when the
	// segment's package may be checked again (compile-speed/cs5b.md §2).
	symbolic func(seg int, v any) (string, bool)
	// symbolicNode, when set (UserExport), names a syntax node of an
	// earlier user segment by its node path (CS5b-4); every node
	// reference then carries a tag (encoder.node).
	symbolicNode func(x any) (seg int, p nodePath, ok bool)
}

// node writes a reference to the syntax object x, not nil: its id, or,
// with symbolicNode set, 1 and its id, or 2, the segment and the node
// path of a node of an earlier user segment. Nil is 0 in both forms.
func (e *encoder) node(x any) {
	if e.symbolicNode != nil {
		if seg, p, ok := e.symbolicNode(x); ok {
			e.uint(2)
			e.uint(uint64(seg))
			e.string(string(p))
			return
		}
		e.uint(1)
	}
	e.uint(e.nodes.ID(x))
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

// external is x's index among the encoder's externals, for an interface
// value of a type the codec has no case for.
func (e *encoder) external(x any) (uint64, bool) {
	if !reflectread.Comparable(x) {
		return 0, false
	}
	i, ok := e.ext[x]
	return i, ok
}

// ref writes a pointer's reference and reports whether it is complete: nil
// (0), an external (1, index), an object written before (2, id), or one of
// an earlier segment by its object path (4, segment, path; encoder.symbolic). Otherwise
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
			seg := segmentOf(e.objStarts, id)
			e.deps[seg] = true
			if e.crossRef != nil {
				e.crossRef(seg, v)
			}
			if e.symbolic != nil {
				if p, ok := e.symbolic(seg, v); ok {
					e.uint(4)
					e.uint(uint64(seg))
					e.string(p)
					return true
				}
			}
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
	checkers []*check.Codec_checker
	// imports, when set (UserExport.Decode), is the current package of an
	// earlier segment, in which a tag 4 reference's path resolves; paths
	// caches the resolved paths, so one path is one object.
	imports func(seg int) *check.Package
	paths   map[pathRef]any
	// userNodes reads node references in encoder.node's tagged form, and
	// nodePaths resolves their node paths, one walk per file.
	userNodes bool
	nodePaths nodeResolver
	nodeRefs  map[nodeRef]any
}

// nodeRef is a tagged node reference: a segment and a node path in it.
type nodeRef struct {
	seg  int
	path nodePath
}

// pathRef is a tag 4 reference: a segment and an object path in it.
type pathRef struct {
	seg  int
	path string
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
	case 4:
		r := pathRef{seg: int(d.uint()), path: d.string()}
		if x, ok := d.paths[r]; ok {
			return x, false
		}
		var pkg *check.Package
		if d.imports != nil {
			pkg = d.imports(r.seg)
		}
		if pkg == nil {
			d.failf("a path into segment %d, which has no current package", r.seg)
		}
		x, ok := check.ResolvePath(pkg, r.path)
		if !ok {
			d.failf("the path %s names nothing in %s", check.PathString(r.path), pkg.Path)
		}
		if d.paths == nil {
			d.paths = map[pathRef]any{}
		}
		d.paths[r] = x
		return x, false
	}
	d.failf("bad reference tag")
	return nil, false
}

// add records the object a fresh reference allocated.
func (d *decoder) add(v any) {
	d.refs = append(d.refs, v)
	if c, ok := v.(*check.Codec_checker); ok {
		d.checkers = append(d.checkers, c)
	}
}

// afterDecode rebuilds what the codec does not carry of each decoded
// checker (check.RestoreChecker).
func (d *decoder) afterDecode() {
	for _, c := range d.checkers {
		check.RestoreChecker(c)
	}
}

// external reads an external's index (encoder.external).
func (d *decoder) external() any {
	i := d.uint()
	if i >= uint64(len(d.ext)) {
		d.failf("external %d", i)
	}
	return d.ext[i]
}

func (d *decoder) node() any {
	id := d.uint()
	if id == 0 {
		return nil
	}
	if d.userNodes {
		switch id {
		case 1:
			id = d.uint()
		case 2:
			r := nodeRef{seg: int(d.uint()), path: nodePath(d.string())}
			if x, ok := d.nodeRefs[r]; ok {
				return x
			}
			var pkg *check.Package
			if d.imports != nil {
				pkg = d.imports(r.seg)
			}
			if pkg == nil {
				d.failf("a node path into segment %d, which has no current package", r.seg)
			}
			x, ok := d.nodePaths.resolve(pkg, r.path)
			if !ok {
				d.failf("a node path names nothing in %s", pkg.Path)
			}
			if d.nodeRefs == nil {
				d.nodeRefs = map[nodeRef]any{}
			}
			d.nodeRefs[r] = x
			return x
		default:
			d.failf("bad node reference tag %d", id)
		}
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
