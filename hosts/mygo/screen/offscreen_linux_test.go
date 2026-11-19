//go:build linux && !android && (amd64 || arm64)

package screen

import (
	"runtime"
	"testing"

	"github.com/egoist/mygo/drawing/gpu/gl"
	"github.com/egoist/mygo/drawing/platform"
)

// gpuFrame runs draw with newGPU making OpenGL renderers that draw into
// an offscreen framebuffer of pw by ph pixels (Mesa's surfaceless EGL,
// llvmpipe where there is no GPU), and returns its pixels: premultiplied
// BGRA rows from the top, as the CPU renderer's. With no EGL it skips t.
func gpuFrame(t *testing.T, pw, ph int, draw func()) []byte {
	t.Helper()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	o, err := gl.NewOffscreen(false, pw, ph)
	if err != nil {
		t.Skipf("no OpenGL context offscreen: %v", err)
	}
	defer o.Release()
	var made []gpuRenderer
	withGPU(t, func(platform.SurfaceNative) (gpuRenderer, error) {
		r, err := gl.New()
		if err == nil {
			made = append(made, r)
		}
		return r, err
	})
	draw()
	pix, err := gl.ReadFramebuffer(pw, ph)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range made {
		r.Release()
	}
	return pix
}
