//go:build !(linux || darwin)

package v1

func (g *CBufferGuard) mapCPages(bool) bool { return false }

func (g *CBufferGuard) seal()  {}
func (g *CBufferGuard) unmap() {}
