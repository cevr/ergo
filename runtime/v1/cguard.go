package v1

import (
	"sync"
	"unsafe"

	"ergo.local/prototype/runtime/rt"
)

// CBufferGuard is one guard-page copy of a C FFI input or output buffer
// (CFFI-4; LAWFFI for C). Read-only buffers (GuardCRead, GuardCInSlice) end at
// the last byte of PROT_READ pages followed by a PROT_NONE guard page, so any
// 1-byte over-read or write to a const buffer faults immediately. Writable
// buffers (GuardCWrite, GuardCOutSlice) end at the last byte of
// PROT_READ|PROT_WRITE pages followed by a PROT_NONE guard page, so any 1-byte
// overflow past capacity faults immediately; Done copies written bytes back to
// the caller's slice and seals the pages PROT_NONE in a bounded ring so
// post-return retention faults too.
type CBufferGuard struct {
	writable      bool
	fn, param, at string
	src           []byte
	snapshot      string
	data          []byte
	mem           []byte
	id            uint64
	pages         uintptr
	done          bool
}

var (
	cGuardMu      sync.Mutex
	cGuardRing    []*CBufferGuard
	cGuardBytes   int
	cGuardsMapped int
	cGuardsFreed  int
)

// GuardCRead returns a read-only guard-page copy of b for a C in_buffer call.
// Empty slices return a no-op guard with a nil buffer and nil pointer.
func GuardCRead(b []byte, fn, param, at string) *CBufferGuard {
	g := &CBufferGuard{writable: false, fn: fn, param: param, at: at, src: b}
	if len(b) == 0 {
		return g
	}
	if !g.mapCPages(false) {
		g.snapshot = string(b)
		g.data = make([]byte, len(b))
		copy(g.data, b)
	}
	return g
}

// GuardCWrite returns a writable guard-page copy of b for a C out_buffer call.
// Initial bytes are copied in case C reads autofilled fields; Done copies the
// buffer back into b and seals the guard pages.
func GuardCWrite(b []byte, fn, param, at string) *CBufferGuard {
	g := &CBufferGuard{writable: true, fn: fn, param: param, at: at, src: b}
	if len(b) == 0 {
		return g
	}
	if !g.mapCPages(true) {
		g.data = make([]byte, len(b))
		copy(g.data, b)
	}
	return g
}

// GuardCInSlice returns a read-only guard-page copy of the element slice s.
func GuardCInSlice[T any](s []T, fn, param, at string) *CBufferGuard {
	if len(s) == 0 {
		return &CBufferGuard{writable: false, fn: fn, param: param, at: at}
	}
	var zero T
	nbytes := len(s) * int(unsafe.Sizeof(zero))
	if nbytes == 0 {
		return &CBufferGuard{writable: false, fn: fn, param: param, at: at}
	}
	raw := unsafe.Slice((*byte)(unsafe.Pointer(unsafe.SliceData(s))), nbytes)
	return GuardCRead(raw, fn, param, at)
}

// GuardCOutSlice returns a writable guard-page copy of the element slice s.
// Done copies the written elements back into s before sealing the guard pages.
func GuardCOutSlice[T any](s []T, fn, param, at string) *CBufferGuard {
	if len(s) == 0 {
		return &CBufferGuard{writable: true, fn: fn, param: param, at: at}
	}
	var zero T
	nbytes := len(s) * int(unsafe.Sizeof(zero))
	if nbytes == 0 {
		return &CBufferGuard{writable: true, fn: fn, param: param, at: at}
	}
	raw := unsafe.Slice((*byte)(unsafe.Pointer(unsafe.SliceData(s))), nbytes)
	return GuardCWrite(raw, fn, param, at)
}

// GuardCReadPtr wraps a raw (ptr, nbytes) in_buffer for bindc's SetCGuardHooks.
func GuardCReadPtr(ptr unsafe.Pointer, nbytes uintptr, fn, param string) (unsafe.Pointer, func()) {
	if ptr == nil || nbytes == 0 {
		return ptr, nil
	}
	raw := unsafe.Slice((*byte)(ptr), int(nbytes))
	g := GuardCRead(raw, fn, param, "cffi")
	return g.Pointer(), g.Done
}

// GuardCWritePtr wraps a raw (ptr, nbytes) out_buffer for bindc's SetCGuardHooks.
func GuardCWritePtr(ptr unsafe.Pointer, nbytes uintptr, fn, param string) (unsafe.Pointer, func()) {
	if ptr == nil || nbytes == 0 {
		return ptr, nil
	}
	raw := unsafe.Slice((*byte)(ptr), int(nbytes))
	g := GuardCWrite(raw, fn, param, "cffi")
	return g.Pointer(), g.Done
}

// Bytes returns the guarded slice with capacity equal to length, or nil when empty.
func (g *CBufferGuard) Bytes() []byte { return g.data }

// Pointer returns the address of the guarded buffer, or nil when empty.
func (g *CBufferGuard) Pointer() unsafe.Pointer {
	if len(g.data) == 0 {
		return nil
	}
	return unsafe.Pointer(unsafe.SliceData(g.data))
}

// Done finishes the guarded C call: for writable buffers it copies bytes back
// into the caller's slice, then seals the guard pages PROT_NONE and pushes the
// mapping into the bounded ring.
func (g *CBufferGuard) Done() {
	if g.data == nil || g.done {
		return
	}
	if g.writable {
		copy(g.src, g.data)
	}
	if g.mem == nil {
		wrote := !g.writable && string(g.data) != g.snapshot
		for i := range g.data {
			g.data[i] = 0xA5
		}
		g.done = true
		if wrote {
			panic(&ViewDefect{Func: g.fn, Param: g.param, At: g.at, Verb: "wrote to"})
		}
		return
	}
	g.seal()
	cGuardMu.Lock()
	g.done = true
	cGuardRing = append(cGuardRing, g)
	cGuardBytes += len(g.mem)
	var out []*CBufferGuard
	for len(cGuardRing) > guardRingLen || cGuardBytes > guardRingBytes {
		old := cGuardRing[0]
		cGuardRing = cGuardRing[1:]
		cGuardBytes -= len(old.mem)
		out = append(out, old)
	}
	cGuardMu.Unlock()
	for _, old := range out {
		old.free()
	}
}

func (g *CBufferGuard) fault(off uintptr, cause error) any {
	cGuardMu.Lock()
	done := g.done
	cGuardMu.Unlock()
	verb := "wrote to"
	switch {
	case done:
		verb = "kept"
	case off >= g.pages && g.writable:
		verb = "wrote past the end of"
	case off >= g.pages:
		verb = "read past the end of"
	}
	return &ViewDefect{Func: g.fn, Param: g.param, At: g.at, Verb: verb, Cause: cause}
}

func (g *CBufferGuard) register(base uintptr) {
	g.id = rt.GuardRegister(base, uintptr(len(g.mem)), g.fault)
	cGuardMu.Lock()
	cGuardsMapped++
	cGuardMu.Unlock()
}

func (g *CBufferGuard) free() {
	rt.MapUnregister(g.id)
	g.unmap()
	cGuardMu.Lock()
	cGuardsFreed++
	cGuardMu.Unlock()
}

func cGuardsLive() int {
	cGuardMu.Lock()
	defer cGuardMu.Unlock()
	return cGuardsMapped - cGuardsFreed
}
