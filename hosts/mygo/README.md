# hosts/mygo

The MyGo host of Ergo's `ui` programs: Ergo owns the element tree, layout,
paint order, hit testing and focus, and draws through MyGo's drawing layer
(the window's surface and its events, the display list, text, the
renderers). It never calls MyGo's `ui` widgets.

It is a Go module of its own, so the Ergo module stays free of
dependencies.

## The drawing layer: `drawing/`

MyGo keeps its drawing layer under `internal/`, and `mygo.Content`, the
interface a window's content implements, takes an `*internal/surface.Conn`.
So no package outside MyGo could implement it.

`drawing/` is the smallest patch that exports the layer (owner q65), as
MyGo would take it upstream: one new package tree,
`github.com/egoist/mygo/drawing`, and no edit of an existing MyGo file. For
each of 11 internal packages (`surface`, `platform`, `scene`, `text`, `vec`,
`raster`, `gpu`, `gpu/gl`, `gpu/metal`, `gpu/d3d11`, `fake`) it has a
package of the same name whose files mirror the internal files with their
build constraints: each exported type is an alias, each exported constant
the same constant, each exported function a function that calls it.
Exported variables are left out, since a copy would not see later writes.
The files are generated (`go generate` in `drawing/`, by
`drawing/internal/drawgen`), and two tests keep them true: they are what
drawgen writes from the pinned MyGo (`TestFilesCurrent`), and each mirrors
its internal package by `go/types` (`TestMirrorsInternal`).

Go's `internal` rule is by import path in module mode, so a module whose
path is under `github.com/egoist/mygo/` may import MyGo's internal packages.
`drawing/` is such a module, and `go.mod` here replaces its path with the
directory. So the host pins upstream MyGo and needs no fork. If MyGo takes
the package, the `replace` and the directory go, and the imports stay.

A content outside MyGo then drives a window as package `ui` does:
`TestContentOutsideMyGo` paints a frame with the display list and the CPU
renderer on the fake backend's surface, hears a press, and measures text.
Tests drive `AttachContent` themselves with a `surface.Conn` over a fake
window's surface; a desktop program passes the content to `mygo.NewWindow`.

## Pins and checks

- MyGo `github.com/egoist/mygo` at `afb7d5f` (`v0.8.4-0.20261109183017-afb7d5f170f7`), Go 1.27.3.
- `./check.sh` is the module's gate step: both modules' tests, uncached,
  and `go vet` for linux, darwin, windows and js/wasm. The batch gate runs
  it after `scripts/gate.sh --merge`, on Linux and on the Mac. It needs the
  pinned modules in the module cache (the network once).
- `testdata/mutants.json` is the module's mutant ledger.
