package rt

import (
	"strconv"
	"sync"
	"sync/atomic"
)

// Faults on mapped files (wf10 MAPPED; docs/spec/stdlib.md, fs note 4).
//
// A file mapped into memory (fs.MapFile) is read by plain loads. When
// another process truncates the file while it is mapped, a load of a page
// past the new end faults (SIGBUS), which Go reports as a fatal fault and
// ends the process. The task that maps a file, and every task that starts
// while a mapping is live (Run), asks Go to panic on such a fault instead
// (debug.SetPanicOnFault), and a defect recorded from that panic names the
// file: MappedFault. A fault outside every live mapping stays the
// runtime's own error value. A task that starts while no mapping is live
// and maps none keeps Go's crash, so a program that maps no file reports a
// fault as Go reports it.
//
// Prior art: Go's own docs name SetPanicOnFault for memory-mapped files;
// Java turns a fault on a truncated MappedByteBuffer into an InternalError
// in the reading thread; Rust's memmap2 leaves it a SIGBUS and makes the
// map unsafe to construct.

// MappedFault is the defect of a load that faulted inside a live mapping
// of the file Path, Offset bytes from the mapping's start: the file
// changed under its mapping (another process truncated it).
type MappedFault struct {
	Path   string
	Offset uintptr
	// Cause is the runtime's panic value for the fault.
	Cause error
}

// Error names the file and the byte, then the runtime's text.
func (f MappedFault) Error() string {
	cause := "<nil>"
	if f.Cause != nil {
		cause = f.Cause.Error()
	}
	return "fs: the mapped file " + f.Path + " changed under its mapping (another process truncated it?): no page at byte " + strconv.FormatUint(uint64(f.Offset), 10) + " (" + cause + ")"
}

// mapping is one live mapping: its address range and its file, or, for a
// guarded copy (GuardRegister), the defect a fault inside it stands for.
type mapping struct {
	base, size uintptr
	path       string
	fault      func(off uintptr, cause error) any
}

var (
	mappingsMu sync.Mutex
	// mappings is made by the first MapRegister, so a program that maps
	// no file links no map of mappings (SIZE-HELLO).
	mappings  map[uint64]mapping
	mappingID uint64
	// mappingsLive counts the live mappings; Run reads it without the
	// lock.
	mappingsLive atomic.Int64
)

// MapRegister records a live mapping of size bytes at base, of the file
// path, and makes the calling goroutine panic on a fault from now on (its
// task maps the file, so it may read it). Its result is the key that
// MapUnregister takes.
func MapRegister(base, size uintptr, path string) uint64 {
	mappingsMu.Lock()
	if mappings == nil {
		mappings = map[uint64]mapping{}
	}
	mappingID++
	id := mappingID
	mappings[id] = mapping{base: base, size: size, path: path}
	mappingsMu.Unlock()
	faultValueOf.Store(&mappedFaultValue)
	mappingsLive.Add(1)
	panicOnFault(true)
	return id
}

// GuardRegister records the pages of a guarded copy (v1.GuardBytes;
// LAWFFI LF2b), size bytes at base, as MapRegister records a file: the
// calling goroutine, and every task that starts while the copy is
// registered, panics on a fault, and FaultValue gives fault's value for a
// fault off bytes into the pages. MapUnregister forgets it.
func GuardRegister(base, size uintptr, fault func(off uintptr, cause error) any) uint64 {
	mappingsMu.Lock()
	if mappings == nil {
		mappings = map[uint64]mapping{}
	}
	mappingID++
	id := mappingID
	mappings[id] = mapping{base: base, size: size, fault: fault}
	mappingsMu.Unlock()
	faultValueOf.Store(&mappedFaultValue) // I-FLIP: LF2b x SIZE-HELLO
	mappingsLive.Add(1)
	panicOnFault(true)
	return id
}

// MapUnregister forgets the mapping id, before it is unmapped.
func MapUnregister(id uint64) {
	mappingsMu.Lock()
	_, ok := mappings[id]
	delete(mappings, id)
	mappingsMu.Unlock()
	if ok {
		mappingsLive.Add(-1)
	}
}

// faultsPanic sets the calling goroutine to panic on a fault when a
// mapping is live, and returns the setting to restore. A task that starts
// while a mapping is live may read it: a text reaches a task only by a
// capture at its start (types.md, Borrows 8: a text is never sent).
func faultsPanic() (restore bool, set bool) {
	if mappingsLive.Load() == 0 {
		return false, false
	}
	return panicOnFault(true), true
}

// FaultValue is p, a recovered panic value, or the MappedFault it stands
// for: a runtime error with the faulting address (SetPanicOnFault's
// Addr) that lies inside a live mapping. It reaches the classifier only
// through faultValueOf, which the first MapRegister sets: a program that
// never maps a file never links the classifier, the iteration over the
// mappings or MappedFault's text (SIZE-HELLO), and a fault in it is the
// runtime's own value, as when no mapping is live.
func FaultValue(p any) any {
	if f := faultValueOf.Load(); f != nil {
		return (*f)(p)
	}
	return p
}

// faultValueOf is the mapped-fault classifier once a file was mapped.
var faultValueOf atomic.Pointer[func(any) any]

// mappedFaultValue is the classifier as a value faultValueOf can point to.
var mappedFaultValue = mappedFault

// mappedFault is FaultValue's classifier.
func mappedFault(p any) any {
	e, ok := p.(interface {
		error
		Addr() uintptr
	})
	if !ok {
		return p
	}
	addr := e.Addr()
	mappingsMu.Lock()
	defer mappingsMu.Unlock()
	for _, m := range mappings {
		if addr >= m.base && addr-m.base < m.size {
			if m.fault != nil {
				return m.fault(addr-m.base, e)
			}
			return MappedFault{Path: m.path, Offset: addr - m.base, Cause: e}
		}
	}
	return p
}
