//go:build !darwin && !(linux && !android && (amd64 || arm64))

package screen

import "testing"

// gpuFrame skips t: this platform has no GPU renderer that draws
// offscreen in a test.
func gpuFrame(t *testing.T, pw, ph int, draw func()) []byte {
	t.Skip("no offscreen GPU renderer on this platform")
	return nil
}
