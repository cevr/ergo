# todo-gui

A to-do list written once against std `ui` and shown by any ui host:
`ui.TestHost` here, a MyGo window in `hosts/mygo/cmd/todo-gui`. Half of
#216's showcase (X-gui).

- `todo/` is the model: a machine (`Todo`) over a `List` of items, which
  keeps the count of open items itself, and the filter. A blank add, and a
  signal that names no item, change nothing.
  - `AddThenRemoveRestores`, `LeftCountsOpenItems` (the kept count is the
    count of open items after any run of signals), `ClearDoneKeepsOpen`,
    `BlankAddsNothing` (`ergo test todo`).
- `view/` is the screen: a field (a `ui.Draft`, the view's own cell) and
  `add` (`ui.Submit`, which empties the draft), the items the filter shows
  in a scroll (`ui.Each` keyed by id; a check box labelled with the item's
  text, a `del TEXT` button), the open count, `all`/`open`/`done` and
  `clear`.
- `script/` reads scene scripts (`tap LABEL`, `type LABEL<TAB>TEXT`,
  `settle`) into `ui.Act`s any host plays, and writes seeded ones (`Gen`:
  adds, then marks and removals of the words added).
- `cmd/todo-gui` is the headless leg and the only place that chooses: the
  test host and its manual clock.

```sh
ergo build examples/todo-gui/cmd/todo-gui -o /tmp/todo-gui
/tmp/todo-gui gen -seed 3 -n 40 | /tmp/todo-gui -screen
```

It prints `N: I items, L left, FILTER: [x] a; [ ] b` after each step and,
with `-screen`, the screen as text. The window binary prints the same lines
for the same script (`-headless`): `TestExamplesAgreeOnTheDesktop` in
hosts/mygo holds them equal, byte for byte. The MyGo-direct control,
`hosts/mygo/controls/todo-gui`, is the same list written on MyGo's own
widgets; it prints the same lines too.

#216's webview mode (a web page in the window, talking to the model) is
not here yet: it needs typed IPC between the page's TypeScript and the
program's operation types, which H3w builds (q67) as a second binary of
this example. Until then the window is drawn by Ergo through MyGo's
primitive layer only.
