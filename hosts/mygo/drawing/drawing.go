// Package drawing is MyGo's drawing layer for hosts outside MyGo: a
// program that draws a window's content itself, with its own element tree
// and layout, in place of package ui.
//
// Its packages re-declare the packages under internal/ that package ui
// draws with, with the same names: the seam between a window and its
// content (surface.Conn, which mygo.Content's AttachContent takes), the
// surface and its events (platform), the display list (scene), text
// shaping and measuring (text), paths (vec), the CPU renderer (raster),
// the GPU renderers (gpu, gpu/gl, gpu/metal, gpu/d3d11) and the backend
// of tests (fake). Types are aliases, so a value passes between MyGo and
// a host with no conversion; constants are the same constants, and each
// function calls the internal one. Exported variables are left out.
//
// The packages are generated from the internal ones and follow them: they
// make no promise of stability beyond MyGo's own.
package drawing

//go:generate go run ./internal/drawgen
