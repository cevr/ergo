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
- The layout tree is kept across passes (`desktop/keep.ergo`, GPk): a
  pass's ops add and remove boxes, and restyle and reorder the children of
  each parent they touched, a style set only where it differs, so the
  kernel lays out again only those boxes and the ones around them up to the
  root (`desktop.LaidOut` counts them), and keeps what it measured. Only
  the texts a pass adds or changes are measured, and `Metrics` keeps its
  table in a `sync.Ref`, so binding it as the paint's `layout.Measure`
  copies nothing. The tree built anew at every paint, as before, stays the
  reference: `desktop.Reference` lays out only that one, and
  `desktop.Check` builds it beside the kept one at every paint and keeps
  the first box where they differ (`desktop.Differs`). hostagree always
  checks, and `TestStarsGuiOnTheDesktop` runs the window binary with
  `-check`. `TestDesktopKeepsItsLayout` is the growth guard: adding a row
  to the shelf app lays out the same few boxes after 10 rows as after 40.
- The paint is a damage paint (`desktop/paint.ergo`): a node no pass,
  scroll or focus change touched, itself or below it, whose box, offset,
  clip and line height are those of its last draw takes that draw again
  from the window's last list (`Window.Again`: its ops, its lines of text
  and its accessibility nodes, as `Mark` and `Seal` bounded them), with
  the spots it added. Only the rest is drawn anew (`desktop.Drawn` counts
  them). The full paint stays the reference: `desktop.Reference` draws
  every node, and `desktop.Check` draws the full list first, puts it aside
  (`Window.Hold`) and holds the damage paint's list and spots to it
  (`Window.Differs`). `TestDesktopDrawsOnlyWhatChanged` is the growth
  guard: adding a row to the shelf app draws the same few nodes after 10
  rows as after 40.
- A virtual list (`ui.Rows`, H5b) scrolls as a Scroll does, over its
  count of rows; each row it wrote is out of the flow at its index's place.
  A list with no height of its own takes the window below its top. After
  each paint the driver reports a list's viewport (its offset and its
  height) as `ui.Scrolled` when it moved, so the next pass writes the rows
  the window shows; `desktop.Viewport` is the last one reported. A pass
  whose list offset is not the window's (`ui.RowsKeeping` brought its row
  into view) scrolls the window there, and the next report says so.
  `ui.ScrollOn` in a script turns the wheel on the list to its offset; the
  height is the window's.
- A canvas (`ui.Canvas`, H5c) replays its paint list (`desktop/canvas.ergo`):
  paths are flattened under the transforms in effect; a fill is one
  rectangle when its path is one on the axes, else spans one DIP high
  under the nonzero rule; a stroke's contour is one polyline
  (`Window.StrokeBegin`, `StrokeTo`, `StrokeEnd`) that the frame
  rasterizes once into a mask of MyGo's atlas, keyed by its points, as
  MyGo's `Painter.StrokePath` does, with round caps and joins; a mask
  over 511 pixels a side is drawn as tiles of it, as `Painter.pathTiles`
  does (a `Line` a segment when the atlas cannot hold them); a clip is the box around its transformed rectangle. Edges are
  not smoothed: the drawing layer has no path fill a host can reach.

The driver is not a `ui.Host` provider: an operation type's method cannot
need `go.Foreign` (README Gap 17), so `Run` drives the session itself, as
`ui.TestHost` does. When the gap closes it becomes one. `Run` does not yet
state `needs MainThread` (H1, not built).

`TestMyGoHostAgreesWithTestHost` builds `cmd/hostagree` and plays seeded
scene scripts of eleven apps on `ui.TestHost` and, through a headless window,
on the driver: after every step both hold the same tree and app state, and
each filled node shows its color in the rendered frame where the driver
laid it out (a canvas's last opaque fill at its centre; a look's fill
below its top edge, and the colour under it outside a top-left corner of
9 or more: `desktop/look.ergo`). In the `list`
app TestHost takes the viewport the window reports, the window shows the
list at the offset its pass wrote (a cursor the list keeps in view moves
with Down and Up), and each row the window wrote must show at the list's
top plus its index's rows, less the offset. Taps are a press and a release at the node's centre, so they
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

## The editor's window: `cmd/editor-gui` (GP2d)

`examples/editor-gui` (module `ergo.local/prototype/examples/editorgui`)
is required the same way. `cmd/editor-gui [-headless [-check]] FILE`
shows its editor, app and view on the desktop driver, saving to the host's
files with a clipboard in memory. Headless, it plays the example's script
lines as a person gives them: a key after a click beside the toolbar (so
a focused field or button does not take it), a shortcut as the key with
Ctrl (`Window.Chord`; the window now gives a program a letter or digit
pressed with Ctrl, Alt or the system key, which it dropped before), a
click on each button, the text input and the replacement field typed
after a click, a press on the page where the scene presses; after each
line it settles the save or clipboard task the view asked for.

`controls/editor-gui` is its MyGo-direct control: the hand-Go model of
`cmd/ergo/testdata/editor-gui/control`, copied, with the window on
MyGo's widgets and the page's lines in a `ui.TextArea`. Headless, a line
goes to the model (a move of N as N moves, as a window's keys make it)
and `ui.Tester` lays out and paints the window after it.

`TestEditorGuiOnTheDesktop` holds both to the example's own
`editor-gui scene` on 60 seeded documents and scripts, output and saved
file (`-editor.seed=N` replays one).

## The drawing app's window: `cmd/draw-gui` (GP3c)

`examples/draw-gui` (module `ergo.local/prototype/examples/drawgui`) is
required the same way. `cmd/draw-gui [-headless [-check]] FILE` shows its
tool machine and view on the desktop driver, saving pictures to the
host's files. Headless, it plays the example's script as a person gives
it: the pointer pressed on the picture and moved and released anywhere,
past the picture's edge too; the toolbar's buttons clicked; Escape
pressed. A pressed pointer's moves go to the node it went down on, as its
release does (pointer capture; before, a move over another node or none
was lost, so a stroke dragged past the picture's edge missed points).

`controls/draw-gui` is its MyGo-direct control: the hand-Go model of
`cmd/ergo/testdata/draw-gui/control`, copied, with the window on MyGo's
widgets and the picture a box whose `Draw` strokes the shapes with
MyGo's `Painter`. `TestDrawGuiOnTheDesktop` holds both to the example's
`draw-gui scene` on 40 seeded pictures and scripts, output and saved file
(`-draw.seed=N` replays one).

## The dashboard's window: `cmd/dash-gui` (GP4c)

`examples/dash-gui` (module `ergo.local/prototype/examples/dashgui`) is
required the same way. `cmd/dash-gui [-headless [-check] [-frames N]]
[-seed S]` shows its app and view on the desktop driver over the seeded
synthetic sensors. The view animates, so the window's display gives it a
frame at each of its frames, each a tick of the session and a `Next` of
the app. Headless, the display is the script's: `frames N` draws N
frames of the window (`Window.Render`, one Frame input each), and the
notes each frame makes are printed after it; `tap LABEL` clicks a
panel's button; `show` prints the window's texts.

`controls/dash-gui` is its MyGo-direct control: the hand-Go model of
`cmd/ergo/testdata/dash-gui/control`, copied, with the window on MyGo's
widgets and each panel's plot painted with MyGo's `Painter` and its chart
package (`chart.MinMax`, `chart.LineInto`). `TestDashGuiOnTheDesktop`
holds both to the example's `dash-gui scene` on 24 seeded sensors, frame
counts and scene scripts (`-dash.seed=N` replays one).

Headless, `Drain` takes what the session posted (`Pump`) after each frame,
as the window's own loop does after every input and as `ui.Advance` does,
so both hosts take a work's reply at the same frame (F-GP4c-1). The
session's watcher reads `LatestChanges`, so a blocked watcher holds one
state (std ui's `WatcherHoldsOneState`).

## The flow's control: `controls/canvas-scenes` (FK12c)

`examples/canvas-scenes` makes the one perf claim of the Foldkit ports (plan
§18.3 FK12), so it has a MyGo-direct control. `controls/canvas-scenes` is the
hand-Go model, paint and script host of
`examples/canvas-scenes/testdata/control` (`model.go`, copied), with the window
on MyGo's widgets: the scene switch, the buttons and the count are
MyGo's `Button` and `Text`, and the canvas is a box whose `Draw` replays
the scene's paint with MyGo's `Painter`. A rectangle is `Fill`, a circle
a path of four cubics and `FillPath`, and a trail `StrokePath`. In a
window, a running scene asks for the next display frame
(`Painter.AnimationFrame`) and takes it as a frame of the program. A press
on the canvas is not wired, as draw-gui's picture is not. Headless, a
line goes to the model, and `ui.Tester` lays the window out and paints it
after each frame and act; it prints what `canvas-scenes` prints on
`ui.TestHost`, and with `-last` only the last screen.

`TestCanvasScenesControlAgrees` holds it to `examples/canvas-scenes` on 24 seeded
scripts, output and exit status (`-flow.seed=N` replays one), and holds
the two copies of the model to one file. `scripts/opcount` measures the
program against `examples/canvas-scenes/testdata/control` on its workload: a
frame of about 600 trails. That control is the same model with no ui
package, because opcount builds a control as a module of its own with no
dependencies. There is no Ergo window binary for canvas-scenes yet: the example
runs on `ui.TestHost` only.

## Pins and checks

- MyGo `github.com/egoist/mygo` at `v0.10.0` (`ebde939`, 2026-11-17, the
  first tag that runs apps on iOS and Android), Go 1.27.3. Bump it with
  `go get` in both modules and `go generate` in `drawing/`; never `go mod
  tidy` here: no Go file imports the example modules, so tidy drops their
  `require` lines and the Ergo build then fails with S104.
- `TestMirrorsInternal` checks the mirror on this host's build and on the
  phones' (android/arm64, ios/arm64, from source with cgo faked), so a file
  constrained to a phone (the Android EGL window) is mirrored too.
- **On a MyGo bump:** `screen/pango_linux.go` reaches MyGo's private Pango function table (`internal/text.pangoLib`) by `go:linkname` and rebinds the shaping path's calls to allocation-free SyscallN calls (PUREGO-CALL; MyGo's RegisterFunc binding allocates on every call).
  - `pangoTable` mirrors that table field for field, and `TestPangoTableMirrorsMyGo` fails on any drift. When it fails, copy the new declaration, and check each rebound call's C signature against MyGo's.
  - `pangoMirror` names the release the mirror is of, and `TestPangoMirrorIsThePinnedMyGo` fails when go.mod pins another, so a bump names the mirror before any window runs.
  - `fastPango` touches the table only when the binary links that release (build info). With another MyGo the window keeps MyGo's own binding: correct, only slower. The v0.10.0 bump wrote through a stale mirror and crashed (SIGSEGV, 81cc27328); `TestStalePangoMirrorKeepsMyGosBinding` holds that path.
  - `screen.PangoCalls` counts the layouts shaped while MyGo's own binding is back in the table (the slow count). It is 0 when the fast binding holds; the dash-gui receipt reports it a frame.
- `./check.sh` is the module's gate step: both modules' tests, uncached,
  and `go vet` for linux, darwin, windows and js/wasm, and for
  android/arm64 and ios/arm64 where their C compiler is (`CC_ANDROID`,
  Xcode; else it says "skipped: no toolchain"). The batch gate runs
  it after `scripts/gate.sh --merge`, on Linux and on the Mac. It needs the
  pinned modules in the module cache (the network once).
- `testdata/mutants.json` is the module's mutant ledger.
