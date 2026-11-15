# counter-gui

A counter with buttons, a choice of step and keys, written once against
std `ui` and shown by any ui host: `ui.TestHost` here, a MyGo window in
`hosts/mygo/cmd/counter-gui`. Half of #216's showcase (X-gui).

- `counter/` is the model: a machine (`Counter`) over a `Count`, with its
  laws. `Dec` stops at zero; a step outside 1 to 100 is ignored.
  - `IncThenDecRestores`, `NeverNegative`, `ResetKeepsTheStep`,
    `BadStepIgnored` (`ergo test counter`).
- `view/` is the screen: the count, `dec`/`inc`/`reset`, a check box per
  step (`ui.Each`, keyed by the step), and keys `+`, `-`, `0` (`ui.OnKey`).
  It reads the count and names signals; it never changes anything itself.
- `script/` reads scene scripts (`tap LABEL`, `key NAME`, `settle`) into
  `ui.Act`s any host plays, and writes seeded ones (`Gen`).
- `cmd/counter-gui` is the headless leg and the only place that chooses:
  the test host and its manual clock.

```sh
ergo build examples/counter-gui/cmd/counter-gui -o /tmp/counter-gui
/tmp/counter-gui gen -seed 7 -n 20 | /tmp/counter-gui -screen
```

It prints `N: count V by B` after each step (`no node L` for a label not on
the screen) and, with `-screen`, the screen as text. The window binary
prints the same lines for the same script (`-headless`):
`TestExamplesAgreeOnTheDesktop` in hosts/mygo holds them equal, byte for
byte, on seeded scripts. This module is a dependency there (its `ergo.mod`
names the module path `go.mod` requires), so the program itself needs no
Go and stays in the default gate.
