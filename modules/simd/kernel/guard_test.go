//go:build goexperiment.simd && amd64 && linux

package kernel

import (
	"runtime/debug"
	"syscall"
	"testing"
)

// TestNoReadPastTheEnd: no kernel reads a byte after its text. Each text
// (a []byte) ends at the last byte of a mapped page whose next page is unreadable, so
// a load of even one byte past the end faults, and the fault is a panic
// the test reports (debug.SetPanicOnFault). It runs every length from 0 to
// three vectors and a tail, with the needle absent so each kernel scans to
// the end. The laws of package scan cannot see such a read: past the end
// lies other memory of the process, which a wrong kernel may read without
// a wrong answer. Mutant: simd-reads-one-past (the vector loop runs while
// a vector ends one byte past the text).
func TestNoReadPastTheEnd(t *testing.T) {
	page := syscall.Getpagesize()
	mem, err := syscall.Mmap(-1, 0, 2*page, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_ANON|syscall.MAP_PRIVATE)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Munmap(mem)
	if err := syscall.Mprotect(mem[page:], syscall.PROT_NONE); err != nil {
		t.Fatal(err)
	}
	for i := range page {
		mem[i] = 'a'
	}
	defer debug.SetPanicOnFault(debug.SetPanicOnFault(true))
	for n := 0; n <= 4*32-1; n++ {
		// The slice's capacity runs on into the unreadable page, so a
		// kernel that reslices past len(s) reads it and faults.
		s := mem[page-n : page]
		for _, k := range []struct {
			name string
			run  func() int
			want int
		}{
			{"IndexByte", func() int { return IndexByte(s, 'z') }, -1},
			{"IndexAny2", func() int { return IndexAny2(s, 'y', 'z') }, -1},
			{"IndexOutside", func() int { return IndexOutside(s, 'a', 'a') }, -1},
			{"CountByte", func() int { return CountByte(s, 'a') }, n},
			{"IndexNeedsEscape", func() int { return IndexNeedsEscape(s, true) }, -1},
		} {
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Errorf("%s on %d bytes that end at an unreadable page: %v", k.name, n, r)
					}
				}()
				if got := k.run(); got != k.want {
					t.Errorf("%s on %d bytes = %d, want %d", k.name, n, got, k.want)
				}
			}()
		}
	}
}
