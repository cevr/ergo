# draw-gui

A drawing app: freehand strokes simplified as they are drawn (Douglas-Peucker), rectangles, ellipses and lines on four layers; select, move and delete; pan and zoom; a uniform grid that finds the shape under the pointer; pictures saved as SVG and read back. This is the first stage of the program (corpus wave GP3a): the model and a headless driver. The canvas on `ui.TestHost` (stage b) and the desktop window (stage c) come when the `ui` host units land.

```sh
draw-gui gen [-seed S] [-n N]   # write a script of N strokes of up to 200 points
draw-gui play FILE              # play the script on standard input over the picture FILE
```

A script is one command a line: `down X Y`, `move X Y` and `up X Y` are the pointer, in screen units; `tool pen|rect|ellipse|line|select` picks a tool; `layer N`, `pan DX DY`, `zoom PERCENT`, `delete` and `escape`; `show` writes the frame a window would show, as text (a status line, then the paint list in screen units); `hit X Y` names the shape under a screen point; `export` writes the SVG and `save` saves it to FILE. A line that is no command writes `? "LINE": want ...` and the script goes on.

## What it shows

- **A machine with four modes.** `canvas.Tool` is `machine[State, Signal]`, `State = Idle | Drawing | Dragging | Selecting`, started on a picture (`Begin`, the machine's input). The step matches the mode and the signal exhaustively: a pointer moving over an idle canvas is `Ignore`, a signal a mode has no use for is `Reject`, and a finished shape or a deletion is the step's output (`Added`, `Removed`), which the driver reports. Laws: `DrawAndBack` (a path the search finds), `ShapesStayInWorld`, `EscapeUndoesADrag`.
- **A union in the hot loops.** `Shape = Stroke | Rect | Ellipse | Line` is matched exhaustively in the box, the hit test, the move, the paint list and the SVG. Coordinates are integers in world units bounded by `Limit`, so every hit test is exact (a squared distance scaled by a segment's squared length) and a picture's text reads back as the same picture (`ExportParsesBack`).
- **Laws justify the fast paths.** The grid answers a hit test from one cell; `HitEqualsBruteForce` holds it to testing every shape, at any point and at points on each shape's edge. A drag moves the grid's entries with the shapes; `MoveThenMoveBackRestores` holds that the shapes and the grid's cells come back. `SimplifyKeepsEndpoints` and `SimplifyWithinEpsilon` state what Douglas-Peucker promises: the ends stay, and every dropped point is within `Epsilon` of the chord that replaces it.
- **Typed failures.** A picture's text that is not in the form fails `BadSvg{Line, Want}`; the `Store` contract's operations fail `Missing`, `Unreadable` and `Unwritable`, and a missing picture is a new, empty one.
- **Contracts and providers.** `canvas.Store` (law `SaveThenLoad`): `files` keeps pictures on the host's disk, `memory` holds them in a `sync.Ref` for tests. `cmd/draw-gui/main.ergo` alone chooses them.

## Layout

- `canvas/`: the domain. `shape.ergo` (points, shapes, boxes, the hit test), `simplify.ergo`, `grid.ergo`, `drawing.ergo` (a picture: its shapes, ids and grid), `tool.ergo` (the view and the machine's step), `laws.ergo` (the machine and its laws), `frame.ergo` (a frame as text), `svg.ergo` (export and parse), `store.ergo`, `gen.ergo` (the seeded workload).
- `script/`: the headless driver: a line to a command (`LineParsesBack`), a script played on the machine.
- `files/`, `memory/`: the providers of `Store`.
- `cmd/draw-gui/main.ergo`: binds `files` over `fs.NewHost()`.

## The Go control

`cmd/ergo/testdata/draw-gui/control` is the same tool in hand-written Go: shapes in a slice of structs with a kind tag, a grid of cells in a Go map, frames written to one buffer. `TestDrawGuiAgreesWithControl` (cmd/ergo, full tier) runs both on seeded pictures and scripts and checks that each prints what a plain reference model in the test prints (every hit found by testing every shape), and that the saved file is the reference's SVG. The receipts per pointer move and per frame (`scripts/opcount`) wait for unit C0.
