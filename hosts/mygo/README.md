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
  `Text`, `Present`) that it records as display-list ops, measures text
  with MyGo's text layer, and carries the accessibility tree (`Access`)
  and the text-input client (`Editing`). A frame is one scene, drawn by
  the GPU renderer MyGo has for the surface's native objects
  (`render_*.go`, split by platform as MyGo's `ui/gpu_*.go`): Metal on
  macOS and iOS, OpenGL ES in an EGL window on Android, OpenGL in a
  GtkGLArea on Linux, WebGL 2 in a browser. Where there is none (the fake
  backend, Windows for now, a GPU that failed) the CPU renderer draws it
  and the surface presents its pixels. A phone's surface presents no
  pixels, so there only the GPU path shows a frame. `MYGO_GPU=0` draws in
  memory. The CPU renderer stays the reference.
  `TestFrameNeedsNoPixelPresent` holds the choice of renderer.
  `TestRenderersAgree` draws 40 seeded paint lists both ways (OpenGL
  offscreen through Mesa on Linux, Metal on a Mac) and compares the
  colours of fills and the ink of text; `-renderers.seed=N` replays one
  seed.
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

## The star table's window: `cmd/stars-gui` (GP1c)

`examples/stars-gui` (module `ergo.local/prototype/examples/starsgui`) is
required the same way. `cmd/stars-gui [-headless] [-rows H] CATALOG`
shows its table, app and view on the desktop driver; its 100,000 rows are
a `ui.Rows` list, so a pass writes only the rows the window's viewport
shows. Headless, it plays the example's script lines as a person gives
them: a key after a click on the list (so a focused button does not take
Enter), a click on each button, a click on the field and the typed text,
and after each line the wheel scrolls the list to the table's window.

`controls/stars-gui` is its MyGo-direct control: the hand-Go model of
`cmd/ergo/testdata/stars-gui/control`, copied, with the window on MyGo's
widgets and its rows in a `ui.List`. Headless, a line goes to the model
and `ui.Tester` lays out and paints the window after it, scrolled to the
table's window; at each `show` it writes a line for any row of the
table's window the frame does not show.

`TestStarsGuiOnTheDesktop` holds both to the example's own
`stars-gui scene` (the view on `ui.TestHost`) on 24 seeded catalogues and
scripts (`-stars.seed=N` replays one). The receipts per line, at 100,000
rows, are in the run's `receipts/stars-gui-c/`.

## Phones

The same program builds as an iOS or Android app:

```
go -C hosts/mygo run ./cmd/phone build -platform ios/simulator -ergo ERGO ./cmd/counter-gui
```

`-platform` is `ios` (a device; signing is the owner's), `ios/simulator`,
`android` (= `android/arm64`) or `android/amd64` (the emulator). The
command runs `ergo build --platform P` (P is `ios/arm64` for the phone
and the simulator, `android/ARCH` for Android), which writes the
program's Go module for the phone, links nothing, and prints `WORK=`,
`GO=`, `GOROOT=` and `GOFLAGS=`. It copies that module into
`-o DIR` (by default a new temporary folder; it never builds in the
source tree), and writes `mygo.json` there. If the program's folder has
its own `mygo.json` (`cmd/todo-gui` has one), it copies that file. Else
it names the app after the folder, with the identifier
`dev.ergo.<module>.<folder>`. Then it runs MyGo's own CLI in DIR. That
CLI is the pinned module's `cmd/mygo`, which `go.mod` names as a `tool`,
so there is no other copy to download. The CLI's `go build` is ergo's
`GO`, first on `PATH`, with ergo's `GOROOT` (the root of the patched
compile: without it the go command runs the stock one) and ergo's
`GOFLAGS`, whose per-package `-gcflags=…=-ergoir=…` name the module's IR
files, rewritten from `WORK` to `DIR`. The module's bodies are in those
files, so stock Go stops at "missing function body". MyGo's CLI
links the program as a c-archive (iOS) or c-shared library (Android)
with Xcode's or the NDK's clang, and packages it. We write no Swift,
Kotlin or Gradle. The command prints the `.app`, `.apk` and `.aab` it
made.

`ergo` reads the whole module graph, the CLI's modules among it, so a
fresh module cache needs `go mod download` here once. `check.sh` runs it.

`TestPhoneBuildsTheExamples` builds counter-gui and todo-gui for
`-phone=PLATFORM`. By default it builds for each phone whose toolchain
is on the host: Xcode, or `ANDROID_HOME` with an NDK and a JDK. It checks
the app executable's architecture, and that the executable holds
`main.main` and the desktop driver's functions. With no toolchain it
skips, which is not a pass. The batch's Mac leg runs it with
`-phone=ios/simulator`.

## Pins and checks

- MyGo `github.com/egoist/mygo` at `v0.10.0` (`ebde939`, 2026-11-17, the
  first tag that runs apps on iOS and Android), Go 1.27.3. Bump it with
  `go get` in both modules and `go generate` in `drawing/`; never `go mod
  tidy` here: no Go file imports the example modules, so tidy drops their
  `require` lines and the Ergo build then fails with S104.
- `TestMirrorsInternal` checks the mirror on this host's build and on the
  phones' (android/arm64, ios/arm64, from source with cgo faked), so a file
  constrained to a phone (the Android EGL window) is mirrored too.
- `./check.sh` is the module's gate step: both modules' tests, uncached,
  and `go vet` for linux, darwin, windows and js/wasm, and for
  android/arm64 and ios/arm64 where their C compiler is (`CC_ANDROID`,
  Xcode; else it says "skipped: no toolchain"). The batch gate runs
  it after `scripts/gate.sh --merge`, on Linux and on the Mac. It needs the
  pinned modules in the module cache (the network once).
- `testdata/mutants.json` is the module's mutant ledger.
