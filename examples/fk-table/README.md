# fk-table

A table of dinosaurs with a search box, sortable columns and two filters. Every control lives in the address's query, so the address alone says what the table shows. A hand-edited address never fails: a parameter the table cannot read becomes that control's default. It is a port of Foldkit's `query-sync` example (`conformance/upstream/foldkit-d21db423/examples/query-sync`, MIT). This is stage a of FK10: the model, with no view yet.

The program steps the table over a script of the user's acts. After each act it shows the address, the controls and the rows.

Tag: `default`.

```sh
fk-table < cases/tour/stdin
```

The script's words are listed in `script/script.ergo`: `start`, `visit`, `search`, `sort`, `diet` and `period`.

## The shape

- Package `query` is the address.
  - `Query { Search, Sorting, Diet, Period }`, where `Sorting = Unsorted | Ascending{Column} | Descending{Column}`. `Route = Browse{Query} | NotFound{Path}`.
  - `Encode` writes `/?search=…&sorting=Column:Direction&diet=…&period=…`. A control at its default writes no parameter.
  - `Decode` reads each parameter on its own, and one it cannot read is that control's default. This matches upstream's `optionFromValidParam` and `sortingFromParam`, which fall back field by field.
  - `Next` is a header click: unsorted, then ascending, descending, and back to unsorted.
- Package `table` is the machine `Table`. Its state is the `Route` and its input is the opening address.
  - **The state is the address.** `Navigated` enters the decoded route.
  - Every control `Emit`s `Replace{Path}`: the address of the controls with that one changed. The state stays as it is until the host replaces the address and says `Navigated`. This is upstream's `ReplaceFilters` (`replaceUrl`) followed by `ChangedUrl`. On a path the table is not at, a control starts from the defaults, as upstream's `routeToBrowseFields` does.
  - `Rows` filters and sorts. Ties keep the listed order, as JavaScript's stable `Array.sort` does, and descending uses the flipped order.
- Package `dino` is the data, a constant, as upstream's `data.ts` is. Lengths are tenths of a metre, so the table needs no floats.
- Package `script` is the host. The table has no timer and no work, so the host steps it with `machines.Next`.

How the port differs from upstream:

- **The search is a string.** Upstream keeps `Option<string>`, and typing writes `None` for an empty box. Here `""` is no search, and `Encode` writes no parameter for it. The address is the same.
- **No list boxes.** Upstream's diet and period filters are `@foldkit/ui` Listbox children. Here `diet` and `period` send the chosen value directly, and the list boxes come from SP1's `examples/kit` in FK10b.
- **No server.** The data is a constant, as upstream's is (plan §18.3).

## Upstream tests

`origin/tests.txt` has a line for each of upstream's 7 story tests and 5 scene tests. No story is an Ergo test, because a law or the `tour` case already kills every mutant that a test of it would. The scene tests need a view and are stage b.

## Laws

- `query`:
  - `EncodeDecodeRoundTrips`: Decode reads back every query Encode writes.
  - `InvalidParamBecomesDefault`: generated junk, or a near miss in one parameter, becomes that control's default and keeps the others.
  - `OnlyTheRootIsTheTable`: any other path is NotFound and keeps the path.
  - `ClicksCycleTheSorting`.
- `table`:
  - `RowsAreTheMatches`: exactly the matches, each once, in the sorting's order, with ties in the listed order.
  - `UrlIsAFunctionOfTheControls`: a control changes nothing itself and asks for the address of the controls with that one changed, from the table or from a lost path.

The mutants these laws and the cases kill are in `testdata/mutants.json`.

## Cases

- `tour`: it opens at an address with every parameter set. It then covers:
  - clearing the search;
  - three clicks on one header and a click on another;
  - the filters;
  - a search in any case, and one with no match;
  - a hand-edited address with bad parameters;
  - a path the table is not at, where a control starts from the defaults.
- `badscript`: an unknown column. The program says which line and exits 1.
