//go:build !(linux && (amd64 || arm64))

package screen

// fastPango rebinds MyGo's Pango calls on Linux (pango_linux.go); MyGo's
// other engines bind their own.
func fastPango(func()) {}
