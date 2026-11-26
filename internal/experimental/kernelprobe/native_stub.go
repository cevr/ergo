//go:build !cgo || !unix

package kernelprobe

import "fmt"

// NewPsychoscriptEngine returns ErrNativeUnavailable when built with CGO_ENABLED=0.
func NewPsychoscriptEngine(tools *ToolRegistry, slots *SlotPool) (KernelEngine, error) {
	return nil, fmt.Errorf("%w: psychoscript requires cgo or purego FFI", ErrNativeUnavailable)
}

// NewLuauEngine returns ErrNativeUnavailable when built with CGO_ENABLED=0.
func NewLuauEngine(tools *ToolRegistry, slots *SlotPool) (KernelEngine, error) {
	return nil, fmt.Errorf("%w: luau requires cgo or purego FFI", ErrNativeUnavailable)
}
