//go:build !(linux || darwin)

package v1

// mapPages reports false where Go's syscall package has no Mprotect (every
// platform but linux and darwin, the BSDs among them): no pages are
// mapped, and the guard takes the heap form (guard.go). The copy is on the
// heap, Done compares it with s, so a write by the callee is the same
// ViewDefect, and poisons it, so a late read reads poison; a read past the
// end does not fault there.
func (g *BytesGuard) mapPages() bool { return false }

func (g *BytesGuard) seal()  {}
func (g *BytesGuard) unmap() {}
