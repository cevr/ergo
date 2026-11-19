# query-table

A table of dinosaurs with a search box, sortable columns and two filters. Every control lives in the address's query, so the address alone says what the table shows. A hand-edited address never fails: a parameter the table cannot read becomes that control's default. It is a port of Foldkit's `query-sync` example (`conformance/upstream/foldkit-d21db423/examples/query-sync`, MIT). This is stage b of FK10: the model and its screen, on std ui's test host. The HTML host is stage c.

The program plays a script of the user's acts on the screen. After each act it shows what changed: the address, the controls, an open list, the sorting, the count and the rows, or the not-found page. With `-stats` it also prints the work of each act.

Tag: `default`.

```sh
query-table < cases/tour/stdin
```

The script's words are listed in `script/script.ergo`: `start`, `visit`, `search`, `sort`, `diet`, `period`, `press`, `choose`, `key`, `link` and `back`.

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
- Package `listbox` is the list box as a machine, `Listbox`.
  - `Box { Count, Open, Active }`: how many items, whether the list is open, and the active item.
  - Pressing the button opens the list at the selected item, or shuts it. While it is open, the arrows and the ends move the active item within the list. A pick selects the item, shuts the list and gives the focus back to the button. Escape does the same without a selection.
  - Its output is `[]Effect`: `Selected{Index}` is upstream's OutMessage, and `Focus` is its focus command.
  - It knows no table, so it can serve any list of choices (SP1's `examples/kit` when that lands).
- Package `explorer` is the program, the machine `Explorer`.
  - Its state is `Page`: the table's route and the two list boxes.
  - For a list box's signal, the step runs the list box's step and folds its effects into the page. A `Selected` becomes the table's `ChoseDiet` or `ChosePeriod`, so the table asks for the address. A `Focus` becomes a command for the host. Upstream does the same with `Update.foldChild` and `foldOutMessage`.
  - Its commands are `Replace{Path}`, `Push{Path}` (the not-found page's link) and `Focus{Filter}`.
- Package `screen` is upstream's view:
  - the heading, the search box (`Search by name…`), the two list boxes, and `Showing N of 22 dinosaurs`;
  - the table, a `Table` whose header row has a button per column, named as upstream's `aria-label` (`Sort by Length, currently ascending`). The rows are an `Each` keyed by name, each row a `Same` keyed by the dinosaur, so a keystroke runs only the rows that come in;
  - or the empty state, `No dinosaurs match your filters.`;
  - or the 404 page with `← Back to Dinosaur Explorer`.
  - A list box is a `Group` named `Diet` or `Period`. It holds the button, named by the item selected, which has a popup and says whether it is expanded. While the list is open, it shows a `Menu` of `MenuItem`s, each saying whether it is the selected one. `OnKey` gives the open list the keys.
  - `OnURL` gives the table `Navigated`.
- Package `dino` is the data, a constant, as upstream's `data.ts` is. Lengths are tenths of a metre, so the table needs no floats.
- Package `script` is the host. It runs `Explorer` as an actor on `ui.NewTestHost` and plays each line on the screen. The actor's output goes to a queue, and the host does what it asks:
  - a replacement replaces the history's entry and tells the table `Navigated`;
  - a push is followed with `ui.Navigate`;
  - a focus is shown.
- `cmd/query-table` chooses the clock and the `-stats` flag.

How the port differs from upstream:

- **The search is a string.** Upstream keeps `Option<string>`, and typing writes `None` for an empty box. Here `""` is no search, and `Encode` writes no parameter for it. The address is the same.
- **The list boxes are this example's.** `listbox` stands in for `@foldkit/ui`'s Listbox until SP1's `examples/kit` holds one. It has no typeahead and no pointer hover on the test host.
- **Menu roles for a list box.** std ui has no `listbox` or `option` role, so the list is a `Menu` of `MenuItem`s that say whether they are selected.
- **The host tells the table where it is after a replacement.** The test host has `Navigate` (a push) and Back, which report the change through `OnURL`, but no replacement that does. So after it replaces the history's entry, the host sends `Navigated` itself.
- **Focus is the host's.** The test host has no focus, so the host shows a `Focus` instead of moving one.
- **No server.** The data is a constant, as upstream's is (plan §18.3).

## Upstream tests

`origin/tests.txt` has a line for each of upstream's 7 story tests and 5 scene tests. No story or scene is an Ergo test, because a law or a case already kills every mutant that a test of it would. The scenes are the case `scenes`.

## Laws

- `query`:
  - `EncodeDecodeRoundTrips`: Decode reads back every query Encode writes.
  - `InvalidParamBecomesDefault`: generated junk, or a near miss in one parameter, becomes that control's default and keeps the others.
  - `OnlyTheRootIsTheTable`: any other path is NotFound and keeps the path.
  - `ClicksCycleTheSorting`.
- `table`:
  - `RowsAreTheMatches`: exactly the matches, each once, in the sorting's order, with ties in the listed order.
  - `UrlIsAFunctionOfTheControls`: a control changes nothing itself and asks for the address of the controls with that one changed, from the table or from a lost path.
- `listbox`:
  - `ActiveStaysInTheList`: over drawn signals, an open list's active item is always one of its items.
  - `OnlyAPickSelects`: over drawn signals, a step selects exactly when it picks an item of the open list, and then it shuts the list and gives the focus back. Escape only gives the focus back.
- `explorer`:
  - `AListChoiceIsTheTableChoice`: opening a list box and picking an item asks for the address the table asks for when that filter is chosen directly, then gives the focus back. The list is shut after, and the route waits for the address.

The mutants these laws and the cases kill are in `testdata/mutants.json`.

## Cases

- `tour`: it opens at an address with every parameter set. It then covers:
  - clearing the search;
  - three clicks on one header and a click on another;
  - the filters, through their list boxes;
  - a search in any case, and one with no match;
  - a hand-edited address with bad parameters;
  - a path the table is not at, then Back.
- `scenes`: upstream's five scene tests on the screen, and the list boxes:
  - the heading, the search box and the count;
  - a search typed, and one with no match;
  - the diet list by keyboard: arrows, Enter, End, Home and Escape;
  - the period list's button pressed twice;
  - the 404 page and its link back.
- `badscript`: an unknown column. The program says which line and exits 1.
