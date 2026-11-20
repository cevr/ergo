//go:build !(linux || darwin)

package v1

// mapPages reports false where no pages are mapped: the guard takes the
// heap form (guard.go).
func (g *BytesGuard) mapPages() bool { return false }

func (g *BytesGuard) seal()  {}
func (g *BytesGuard) unmap() {}
