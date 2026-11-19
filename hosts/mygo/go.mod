module ergo.local/prototype/hosts/mygo

go 1.27.3

toolchain go1.27.4

require (
	github.com/egoist/mygo v0.10.0
	github.com/egoist/mygo/drawing v0.0.0
)

require (
	github.com/ProtonMail/go-crypto v1.4.1 // indirect
	github.com/cloudflare/circl v1.6.2 // indirect
	github.com/ebitengine/purego v0.11.1 // indirect
	github.com/go-text/typesetting v0.3.5 // indirect
	github.com/klauspost/compress v1.18.6 // indirect
	github.com/ulikunitz/xz v0.5.15 // indirect
	go.digitalxero.dev/rpm v0.3.4 // indirect
	golang.org/x/crypto v0.57.0 // indirect
	golang.org/x/image v0.46.0 // indirect
	golang.org/x/net v0.59.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.43.0 // indirect
)

// MyGo's own CLI, at the pinned version, packages a phone app
// (cmd/phone runs it as `go tool mygo`): no download of another copy.
tool github.com/egoist/mygo/cmd/mygo

// The drawing layer MyGo keeps internal, exported by a package of aliases
// at MyGo's own path (drawing/drawing.go): Go's internal rule is by import
// path, so it needs no fork. It goes when MyGo takes the package upstream.
replace github.com/egoist/mygo/drawing => ./drawing

// The Ergo examples whose window binaries are the cmd/*-gui folders:
// Ergo modules this one requires (grammar.md, Required
// modules), from their folders in this repository.
require (
	ergo.local/prototype/examples/countergui v0.0.0
	ergo.local/prototype/examples/editorgui v0.0.0
	ergo.local/prototype/examples/dashgui v0.0.0
	ergo.local/prototype/examples/drawgui v0.0.0
	ergo.local/prototype/examples/starsgui v0.0.0
	ergo.local/prototype/examples/todogui v0.0.0
)

replace ergo.local/prototype/examples/countergui => ../../examples/counter-gui

replace ergo.local/prototype/examples/todogui => ../../examples/todo-gui

replace ergo.local/prototype/examples/starsgui => ../../examples/stars-gui

replace ergo.local/prototype/examples/editorgui => ../../examples/editor-gui

replace ergo.local/prototype/examples/drawgui => ../../examples/draw-gui

replace ergo.local/prototype/examples/dashgui => ../../examples/dash-gui
