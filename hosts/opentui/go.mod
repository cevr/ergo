module ergo.local/prototype/hosts/opentui

go 1.27

toolchain go1.27.4

require github.com/ebitengine/purego v0.11.1

// The repository's module, for its C binder (internal/bindc), which writes
// ot/opentui.go. Go's internal rule is by import path, and this module's
// path is under ergo.local/prototype.
require ergo.local/prototype v0.0.0

replace ergo.local/prototype => ../..

// The examples whose terminal binaries live in cmd/ (Ergo modules: the
// model, the view and the script of each).
require (
	ergo.local/prototype/examples/countergui v0.0.0
	ergo.local/prototype/examples/starsgui v0.0.0
	ergo.local/prototype/examples/todogui v0.0.0
)

replace ergo.local/prototype/examples/countergui => ../../examples/counter-gui

replace ergo.local/prototype/examples/starsgui => ../../examples/stars-gui

replace ergo.local/prototype/examples/todogui => ../../examples/todo-gui
