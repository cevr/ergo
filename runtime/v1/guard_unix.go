//go:build linux || darwin

package v1

import (
	"os"
	"syscall"
	"unsafe"
)

// mapPages maps g's copy (guard.go): the bytes of g.s end at the last
// byte of the last read-only page, and the page after it is unmapped
// (mapped with no access). It reports false when the pages cannot be
// mapped; the caller takes the heap form.
func (g *BytesGuard) mapPages() bool {
	page := os.Getpagesize()
	n := len(g.s)
	pages := (n + page - 1) / page * page
	mem, err := syscall.Mmap(-1, 0, pages+page, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_PRIVATE|syscall.MAP_ANON)
	if err != nil {
		return false
	}
	copy(mem[pages-n:pages], g.s)
	if syscall.Mprotect(mem[:pages], syscall.PROT_READ) != nil || syscall.Mprotect(mem[pages:], syscall.PROT_NONE) != nil {
		_ = syscall.Munmap(mem)
		return false
	}
	g.mem, g.pages = mem, uintptr(pages)
	g.data = mem[pages-n : pages : pages]
	g.register(uintptr(unsafe.Pointer(&mem[0])))
	return true
}

// seal makes g's pages unreadable: a later access through a kept slice
// faults.
func (g *BytesGuard) seal() { _ = syscall.Mprotect(g.mem[:g.pages], syscall.PROT_NONE) }

// unmap gives g's pages back.
func (g *BytesGuard) unmap() { _ = syscall.Munmap(g.mem) }
