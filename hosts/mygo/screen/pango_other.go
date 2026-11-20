//go:build !(linux && (amd64 || arm64))

package screen

// fastPango rebinds MyGo's Pango calls on Linux (pango_linux.go); MyGo's
// other engines bind their own.
func fastPango(func()) {}

// shaping counts slow layouts on Linux only.
func shaping() {}

// PangoCalls is 0, 0 off Linux: no Pango.
func PangoCalls() (fast, slow uint64) { return 0, 0 }
