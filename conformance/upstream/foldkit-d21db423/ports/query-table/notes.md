# query-table and its original

What `examples/query-table` ports, how it differs from the original, and how its upstream tests are restated (moved from its README, EX-NEUTRAL).

It is a port of Foldkit's `query-sync` example (`conformance/upstream/foldkit-d21db423/examples/query-sync`, MIT).
How the port differs from upstream:

- **The search is a string.** Upstream keeps `Option<string>`, and typing writes `None` for an empty box. Here `""` is no search, and `Encode` writes no parameter for it. The address is the same.
- **The list boxes are this example's.** `listbox` stands in for `@foldkit/ui`'s Listbox until SP1's `examples/kit` holds one. It has no typeahead and no pointer hover on the test host.
- **Menu roles for a list box.** std ui has no `listbox` or `option` role, so the list is a `Menu` of `MenuItem`s that say whether they are selected.
- **The host tells the table where it is after a replacement.** The test host has `Navigate` (a push) and Back, which report the change through `OnURL`, but no replacement that does. So after it replaces the history's entry, the host sends `Navigated` itself.
- **Focus is the host's.** The test host has no focus, so the host shows a `Focus` instead of moving one.
- **No server.** The data is a constant, as upstream's is (plan §18.3).

## Upstream tests

`origin/tests.txt` has a line for each of upstream's 7 story tests and 5 scene tests. No story or scene is an Ergo test, because a law or a case already kills every mutant that a test of it would. The scenes are the case `scenes`.

## Comparisons moved from the README

- This matches upstream's `optionFromValidParam` and `sortingFromParam`, which fall back field by field.
- Upstream does the same with `Update.foldChild` and `foldOutMessage`.
