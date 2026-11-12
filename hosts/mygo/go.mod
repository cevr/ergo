module ergo.local/prototype/hosts/mygo

go 1.27.3

require (
	github.com/egoist/mygo v0.8.4-0.20261109183017-afb7d5f170f7
	github.com/egoist/mygo/drawing v0.0.0
)

require (
	github.com/ebitengine/purego v0.11.1 // indirect
	github.com/go-text/typesetting v0.3.5 // indirect
	golang.org/x/image v0.46.0 // indirect
	golang.org/x/net v0.59.0 // indirect
	golang.org/x/text v0.43.0 // indirect
)

// The drawing layer MyGo keeps internal, exported by a package of aliases
// at MyGo's own path (drawing/drawing.go): Go's internal rule is by import
// path, so it needs no fork. It goes when MyGo takes the package upstream.
replace github.com/egoist/mygo/drawing => ./drawing
