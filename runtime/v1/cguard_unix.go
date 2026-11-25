//go:build linux || darwin

package v1

import (
	"os"
	"syscall"
	"unsafe"
)

func (g *CBufferGuard) mapCPages(writable bool) bool {
	page := os.Getpagesize()
	n := len(g.src)
	pages := (n + page - 1) / page * page
	mem, err := syscall.Mmap(-1, 0, pages+page, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_PRIVATE|syscall.MAP_ANON)
	if err != nil {
		return false
	}
	off := pages - n
	copy(mem[off:off+n], g.src)
	if !writable {
		if syscall.Mprotect(mem[:pages], syscall.PROT_READ) != nil {
			_ = syscall.Munmap(mem)
			return false
		}
	}
	if syscall.Mprotect(mem[pages:], syscall.PROT_NONE) != nil {
		_ = syscall.Munmap(mem)
		return false
	}
	g.mem, g.pages = mem, uintptr(pages)
	g.data = mem[off : off+n : off+n]
	g.register(uintptr(unsafe.Pointer(&mem[0])))
	return true
}

func (g *CBufferGuard) seal() { _ = syscall.Mprotect(g.mem[:g.pages], syscall.PROT_NONE) }

func (g *CBufferGuard) unmap() { _ = syscall.Munmap(g.mem) }
