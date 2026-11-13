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

## The layout oracle

Ergo's layout kernel is std `ui/layout`, Ergo source. MyGo's flexbox is its
oracle (docs/spec/stdlib.md, UI layout): `TestLayoutMatchesMyGo` builds
`bin/ergo` from the repository, builds `testdata/layoutoracle` with it
(a program that reads trees on standard input and prints the kernel's
boxes), and compares every box with MyGo's under `ui.Tester`, read by
label, on 300 generated trees and three edits of each. A failing seed
prints the input and `go test -run TestLayoutMatchesMyGo -layout.seed=N`
replays it. `TestLayoutRelaysOutAlongThePath` counts the boxes the kernel
lays out again after one leaf changes. On Linux each program they start
runs under `prlimit --data=4GiB`.

## The desktop host: `screen/`, `desktop/`

This folder is also an Ergo module (`ergo.mod`, `module
ergo.local/prototype/hosts/mygo`, the Go module's path, so another Ergo
module can require it: grammar.md, Required modules) beside the Go module, as `internal/selfhostcheck/testdata/selfhost` is: an Ergo
program binds Go only through `ergo bind go`, and a host outside the
compiler cannot bind `runtime/v1` (STD-NOPRIV NP4e), so the Go half is a
package of this Go module and the Ergo half binds it.

- `screen/` (Go) is a window: `Open` makes a MyGo window whose content it
  is (`mygo.NewWindow`; nothing else of MyGo's root package, and nothing of
  `mygo/ui`), `Headless` the same content on no window, for tests. It turns
  surface events into `Input` records that Ergo reads (`Next` waits, `Poll`
  does not), takes a paint list (`Begin`, `Fill`, `Border`, `Clip`,
  `Text`, `Present`) that it records as display-list ops and renders with
  the CPU renderer, measures text with MyGo's text layer, and carries the
  accessibility tree (`Access`) and the text-input client (`Editing`).
- `ergo.bind/` is its committed binding (`ergo bind go
  ergo.local/prototype/hosts/mygo/screen`, with Go 1.27.3: an `ergo` built
  with an older Go cannot read 1.27.3 export data).
- `desktop/` (Ergo) is the driver: a `ui.Session` on one side, the window
  on the other. It keeps the tree from the session's patches, lays it out
  with std `ui/layout` (text measured by `Metrics`, a `layout.Measure`
  provider that holds the widths the window measured, so its laws run
  without Go), paints it in tree order, and owns hit testing (the node
  painted last under the point), the focus (Tab and Shift+Tab in paint
  order, Enter and space press), typing (into the focused field, as
  `ui.Type` with the field's whole text; else each rune to `OnKey`),
  scrolling and pointer capture. `desktop.Run` runs a program in a window;
  call it from `main`, whose task runs on the main thread.
- A virtual list (`ui.Rows`, H5b) scrolls as a Scroll does, over its
  count of rows; each row it wrote is out of the flow at its index's place.
  A list with no height of its own takes the window below its top. After
  each paint the driver reports a list's viewport (its offset and its
  height) as `ui.Scrolled` when it moved, so the next pass writes the rows
  the window shows; `desktop.Viewport` is the last one reported.
  `ui.ScrollOn` in a script turns the wheel on the list to its offset; the
  height is the window's.
- A canvas (`ui.Canvas`, H5c) replays its paint list (`desktop/canvas.ergo`):
  paths are flattened under the transforms in effect; a fill is one
  rectangle when its path is one on the axes, else spans one DIP high
  under the nonzero rule; a stroke is one `Window.Line` a segment, round
  capped; a clip is the box around its transformed rectangle. Edges are
  not smoothed: the drawing layer has no path fill a host can reach.

The driver is not a `ui.Host` provider: an operation type's method cannot
need `go.Foreign` (README Gap 17), so `Run` drives the session itself, as
`ui.TestHost` does. When the gap closes it becomes one. `Run` does not yet
state `needs MainThread` (H1, not built).

`TestMyGoHostAgreesWithTestHost` builds `cmd/hostagree` and plays seeded
scene scripts of seven apps on `ui.TestHost` and, through a headless window,
on the driver: after every step both hold the same tree and app state, and
each filled node shows its color in the rendered frame where the driver
laid it out (a canvas's last opaque fill at its centre). In the `list`
app TestHost takes the viewport the window reports, and each row the
window wrote must show at the list's top plus its index's rows, less the
offset. Taps are a press and a release at the node's centre, so they
pass the layout, the paint list and the hit test. `-agree.seed=N` replays
one seed. `TestMyGoHostPrimitivesOnly` scans this module's Go with
`go/types` and refuses any MyGo object but the drawing layer's and
`App`, `NewWindow`, `Window`, `WindowOptions` and `Content`.

## The examples' windows: `cmd/counter-gui`, `cmd/todo-gui`

`examples/counter-gui` and `examples/todo-gui` are Ergo modules this one
requires (`go.mod`: `require` and a `replace` to their folders; their
`ergo.mod` names the same module path), so their model and view run here
unchanged on the desktop driver. Each window binary opens a MyGo window
(`counter-gui`), or, with `-headless`, plays a scene script from standard
input through a window with no screen and prints what the example's own
binary prints on `ui.TestHost`. A step on a row below a Scroll's fold
wheels the Scroll until the row shows (`desktop.Play`), as a person would.

`controls/todo-gui` is the MyGo-direct control: the same list on MyGo's
own widgets, in Go, with the same labels; `-headless` plays the same
scripts on `ui.Tester`. It is the yardstick for the binary-size receipt,
and it is outside `TestMyGoHostPrimitivesOnly`'s scan (a control uses
widgets by design).

`TestExamplesAgreeOnTheDesktop` builds the four Ergo binaries and the
control and holds their output equal, byte for byte, on 20 seeded scripts
of 120 steps each (`-examples.seed=N` replays one).

## Pins and checks

- MyGo `github.com/egoist/mygo` at `afb7d5f` (`v0.8.4-0.20261109183017-afb7d5f170f7`), Go 1.27.3.
- `./check.sh` is the module's gate step: both modules' tests, uncached,
  and `go vet` for linux, darwin, windows and js/wasm. The batch gate runs
  it after `scripts/gate.sh --merge`, on Linux and on the Mac. It needs the
  pinned modules in the module cache (the network once).
- `TestExamplesCrossBuild` builds the window binaries for darwin/arm64,
  windows/amd64 and js/wasm (screen's binding states those platforms), and
  runs the js/wasm ones headless under Node (Go's `wasm_exec_node.js`) on
  three seeded scripts, each printing what `ui.TestHost` prints. Node runs
  them without `GOMAXPROCS`: `wasm_exec_node.js` hands node's environment
  to the program, and Go's runtime on js/wasm dies in its own start
  ("newosproc: not implemented") when it is over 1, before any program
  code runs, so no fix in the program can help
  (`TestWasmRunsUnderAnInheritedGOMAXPROCS`). A browser passes no
  environment. Run a js/wasm binary under node with `GOMAXPROCS` unset.
- `testdata/mutants.json` is the module's mutant ledger.
