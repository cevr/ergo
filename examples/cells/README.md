# cells

The terminal cell host of `ui` programs (corpus wave T1, with NSH's NS6 folded in). It steps a view over an actor as `ui.Run` and `ui.TestHost` do. It lays the tree out in points with std `ui/layout`, as every host does, and draws it into a grid of cells from what each node means. Then it writes the difference from the grid the terminal shows, as one synchronized frame. Keys come from std `term`, and a script's taps go to a cell and through the host's hit test. The binary shows the views of `counter-gui` and `todo-gui` on it, and their scene scripts run on it unchanged.

```sh
cells gen counter -seed 7 -n 8 | cells counter -cols 40 -rows 7   # each step, then the screen as text
cells gen todo -seed 3 -n 10 | cells todo -frames                 # the frames, as a terminal reads them
cells live counter                                                 # on this terminal: Tab, Enter, + and -, Ctrl-C ends
```

Tag: `default`. It is an original program, not a port. Other programs use the host as `counter-gui`'s view is used here: a `go.mod` require of `ergo.local/prototype/examples/cells` and a replace to its folder.

## Layout

| folder | what it holds |
| --- | --- |
| `grid/` | `grid.ergo`: `Pen` (`Normal`, `Faint`, `Strong`, `Inverse`), `Cell`, `Grid`, `Changed`, `Resized`, `Text` (the text screenshot). `frame.ergo`: `Frame(old, next)`, the cells of `next` that differ from `old`, a run after one cursor move, a pen change once, inside `CSI ? 2026 h` … `CSI ? 2026 l`; `Replay`, the reference reader of what a frame writes; laws `DiffReplaysToGrid`, `FrameWritesTheChanges`, `FrameIsOneUpdate`. |
| `host/` | The host. `measure.ergo`: `Cells`, the `layout.Measure` of the grid (a rune one cell, a line one row; its three member laws run on it), and `Span`, the one rule from points to cells. `look.ergo`: the look of each node from its semantics (`styleOf`, `TheMarks`), the left-out table (`Left`, `LeftOut`, `leftOutName`), law `CellsShowEveryName`. `host.ergo`: `Host`, `New`, `show` (a patch applied whole, then one frame), drawing, `HitAt`, laws `HitMatchesDraw` and `PatchWritesItsChange`. `input.ergo`: `Feed` of a `term.Key`, `Tap` at a cell, the focus, `Settle`. `play.ergo`: `Play` of a `ui.Act` script through the cells. `run.ergo`: `Run` on a `term.Terminal` and `io.Stdout`, law `PatchIsOneFrame`. `sample.ergo`: the generated views of the laws and `Fake`, a terminal that logs every write and read. |
| `scenes/` | Laws `CounterAgrees` and `TodoAgrees`: each example's seeded script on the cell host and on `ui.TestHost`. Every step ends alike, the two trees are the same, and a terminal that read every frame shows the host's grid. |
| `cmd/cells/` | The binary. It is the only place that chooses: the terminal and its size, the clock and the app. |

## What it shows

- **Cells from semantics (NS6 item 1).** A control is drawn from its accessible name (`ui.Access`), and its state comes from one table, `TheMarks`: `[x]` and `[ ]` for a Check, `(•)` and `( )` for a Toggle, `[` `]` around a Pressable and an Input. A disabled control shows `Faint`, and the focused one `Inverse`. A Pressable with no Text inside is a leaf as wide as its label, so an icon button shows its name. `CellsShowEveryName`: on a generated view, every node that shows and has a name has that name's first cells in the grid, or its name is in the left-out table.
- **Left out, never imitated (NS6 item 2).** `LeftOut` is the table of what the host does not draw: opacity, shadows, radii and borders, pixels, a canvas's paint list beyond its label, sub-cell sizes, and the labels a node's content stands for. `Span` rounds each edge of a box to the nearest grid line, half a cell up. Drawing and hit testing use it alike. `HitMatchesDraw`: the cell a control is drawn in hits it, on views whose gaps and padding put edges anywhere between grid lines.
- **A patch is applied whole (NS6 item 3).** `show` applies every op of a pass to the host's tree, then draws once. A frame is one write of one synchronized update. `PatchIsOneFrame` runs `Run` on `Fake` with drawn keys and reads its op log. Each write is a whole frame, there are no more frames than patches and the host's own redraws, and replaying the writes gives the host's grid.
- **The cost of a patch in cells written.** `FrameWritesTheChanges` (each changed cell once, no other) and `PatchWritesItsChange` (a press that changes one digit writes one cell, on any generated view) count the cells, not time.
- **The scene scripts unchanged.** `CounterAgrees` and `TodoAgrees` play `script.Gen`'s scripts of both examples through the grid, the hit test and the focus.

## Mutants

`testdata/mutants.json` holds them. T1's mutant is a cell skipped at a line end. NS6's are: drawing rounds down while hit testing rounds to the nearest line; a Pressable with only a canvas inside drawn as empty cells; a frame after each op. The rest are a frame writing through unchanged cells, the update marks around each run, a lost check mark, and a tap that only focuses.

## Gaps and choices

- A rune is one cell wide. Wide and combining characters are not measured. examples/vt's width table is the one to use when the two meet.
- There is no mouse yet. `term` reads no mouse reports, so a tap comes from a script (`Tap`, `Play`). A Scroll scrolls by whole rows when the focus or a script reveals a node in it, not by a wheel.
- `live` runs on a manual clock, so a view's timers and frames (`Animate`) do not run there.
- The independent reader of the frames is `grid.Replay`. Holding the frames to examples/vt's DEC parser and screen is the stacked step (`wf10/ns6-vt`), on a head that holds both.
- MyGo's driver applies a patch whole too (`desktop.show` applies every op, then paints once). Its op-log law is the GP lane's.
