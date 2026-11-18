# triage

`triage` sorts the entries of a log by an order and prints them: the most severe first (`-by severity`, the default), or by host (`-by host`). Each line of the log on standard input is `SEVERITY HOST`, with a severity of `debug`, `info`, `warn` or `error`. `triage -gen N` prints a generated log of N entries.

```sh
printf 'info web-2\nerror db-1\nwarn web-2\n' | triage
# error db-1 2
# warn web-2 3
# info web-2 1
```

## What it shows

- **Orders as providers.** Package `entry` declares `Entry{Severity, Host, Line}` and two providers of std `cmp.Order[Entry]`: `BySeverity` and `ByHost`. Each is a record with no fields and a `Compare` that chains the field comparisons with `cmp.Then`, the first field first. The call names its order: `slices.SortBy(entries, entry.BySeverity)`.
- **Laws.** The package's test build runs `cmp.Order`'s laws (`Reflexive`, `Antisymmetric`, `Transitive`) against both orders, and `order.ergo` states what each order puts first (`MoreSevereFirst`, `OneHostTogether`, `LineBreaksTies`). Every order ends with the line, so no two entries of one log tie, and the output does not depend on the sort being stable.
- **The floor.** The build gives `slices.SortBy` one copy for each order, and in the copy `by.Compare(a, b)` is a direct call of the order's `Compare`, which Go inlines. In `Compare`, the IR evaluates the second argument of `cmp.Then` only when the first is 0, so a host is compared only on a severity tie, as a hand-written compare does (CONSTRAINTS C4; `docs/spec/stdlib.md`).

## Layout

- `entry/`: the entry, its parser and generator (`entry.ergo`), and its orders with their laws (`order.ergo`).
- `cmd/triage/main.ergo`: the binary. It reads the flags and the log, and it is the only place that chooses the providers.
- `workload`, `testdata/control/`: the counted receipt (`go run ./scripts/opcount -control triage`). The control is the same parse, the same sort algorithm with a concrete compare, and the same print, in hand Go.
- `testdata/mutants.json`: the example's mutants, each killed by its laws or cases.
