// Package mygo hosts Ergo programs in MyGo windows. It draws through
// MyGo's drawing layer (github.com/egoist/mygo/drawing: the surface, its
// events, the display list, text and the renderers), never through MyGo's
// ui widgets: Ergo owns the element tree, layout, paint order, hit testing
// and focus.
//
// It is a Go module of its own, so the Ergo module stays free of
// dependencies. README.md says how it is pinned and checked.
package mygo
