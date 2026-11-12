package rt

import (
	"fmt"
	"runtime/debug"
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
	return fmt.Sprintf("fs: the mapped file %s changed under its mapping (another process truncated it?): no page at byte %d (%v)", f.Path, f.Offset, f.Cause)
}

// mapping is one live mapping: its address range and its file.
type mapping struct {
	base, size uintptr
	path       string
}

var (
	mappingsMu sync.Mutex
	mappings   = map[uint64]mapping{}
	mappingID  uint64
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
	mappingID++
	id := mappingID
	mappings[id] = mapping{base: base, size: size, path: path}
	mappingsMu.Unlock()
	mappingsLive.Add(1)
	debug.SetPanicOnFault(true)
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
	return debug.SetPanicOnFault(true), true
}

// FaultValue is p, a recovered panic value, or the MappedFault it stands
// for: a runtime error with the faulting address (SetPanicOnFault's
// Addr) that lies inside a live mapping.
func FaultValue(p any) any {
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
			return MappedFault{Path: m.path, Offset: addr - m.base, Cause: e}
		}
	}
	return p
}
