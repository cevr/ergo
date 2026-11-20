package screen

import (
	"log"
	"os"

	"github.com/egoist/mygo/drawing/platform"
	"github.com/egoist/mygo/drawing/scene"
)

// gpuRenderer draws scenes into a surface on the GPU: MyGo's renderer for
// the surface's native objects (render_*.go, split by platform as MyGo's
// ui/gpu_*.go are), which shows the frame itself.
type gpuRenderer interface {
	Render(s *scene.Scene) error
	Release()
}

// newGPU makes the GPU renderer of a surface: nil, and no error, where
// the platform has none for it. Tests replace it.
var newGPU = newGPURenderer

// renderGPU draws s with the surface's GPU renderer and reports whether
// it did. The first frame makes the renderer, from the surface's native
// objects (a frame is when they are current: a GtkGLArea's context, a
// canvas's). A renderer that fails is dropped and the frame drawn in
// memory; SurfaceRenew and SurfaceLowMemory have the next frame try
// again. MYGO_GPU=0 draws every frame in memory, as for MyGo's windows.
// The main thread calls it.
func (w *Window) renderGPU(s *scene.Scene) bool {
	if !w.gpuTried {
		w.gpuTried = true
		if n := w.conn.Surface.Native(); n != (platform.SurfaceNative{}) && os.Getenv("MYGO_GPU") != "0" {
			r, err := newGPU(n)
			switch {
			case err != nil:
				// r may be a nil pointer in a non-nil interface here.
				log.Printf("screen: drawing in memory: %v", err)
			case r != nil:
				w.gpu = r
			}
		}
	}
	if w.gpu == nil {
		return false
	}
	if err := w.gpu.Render(s); err != nil {
		log.Printf("screen: drawing in memory: %v", err)
		w.dropGPU()
		w.gpuTried = true // until the surface renews
		return false
	}
	return true
}

// dropGPU releases the GPU renderer, so that the next frame makes one
// again.
func (w *Window) dropGPU() {
	if w.gpu != nil {
		w.gpu.Release()
		w.gpu = nil
	}
	w.gpuTried = false
}
