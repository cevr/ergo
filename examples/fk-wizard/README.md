# fk-wizard

An expedition permit, filled in step by step. You plan the trip, pack supplies (an overnight trip only), name the permit's leader, review the permit, and file it. The office confirms the permit a second later. You can cancel at any step on the way, and start over after either end. An inspector logs every transition. It is a port of Foldkit's `state-machine` example (`conformance/upstream/foldkit-d21db423/examples/state-machine`, MIT), whose checkout becomes the permit. This is stage a of FK7: the model, with no view yet.

The program runs the desk as an actor on a manual clock and plays a script of the user's acts. After each act it shows the stage when it changed and the log's new lines.

Tag: `default`.

```sh
fk-wizard < cases/tour/stdin
```

The script's words are listed in `script/script.ergo`: `trip`, `continue`, `back`, `leader`, `agree`, `code`, `apply`, `file`, `cancel`, `startover` and `wait`.

## The shape

- Package `permit` is the machine `Wizard` over `Stage = Plan | Supplies | Permit | Review | Filing | Confirmed | Cancelled`. Every stage keeps the trip's kind.
  - **Guards are conditions in the arms.** Upstream writes `when(guard, target, …)` and `otherwise(to(…))`. Here, `Continue` from the plan enters `Supplies` only for an overnight trip, `Back` from the permit returns there only for one, and `File` is ignored until a leader is named and the rules are agreed.
  - **Shared transitions are one arm.** Upstream writes `Machine.forStates([...]).on(...)`. Here, `(Plan, Cancel), (Supplies, Cancel), (Permit, Cancel), (Review, Cancel)` is one arm, and the two ends share `StartOver` in another.
  - The match names every stage, so a new stage stops the build until its arms are written. This replaces upstream's `deadTransitions()` and `unreachableStates()` checks.
  - **The office is a timer.** `Filing`'s timer gives `Filed` after `FilingTakes` (1 s), and the runner owns it. Upstream's `PlaceOrder` command sleeps a second and answers.
  - **A typed code is parsed at the edge.** `ParseCode` trims the code and reads it in any case. The machine takes `Applied{Allowance}` or `Unknown`, and the field's text stays the host's. So the signals can be listed, and `ergo test` decides the machine's laws on the whole graph (53 states).
- Package `desk` adds the inspector's log. `Track` steps the permit and logs "from -> to on signal", or "signal ignored in stage". An ignored signal is a `Stay` of the desk, so a filing's timer runs on through it. The log keeps the newest 20. Upstream's `update` wraps `checkoutMachine.step` the same way.
- Package `script` is the host. It starts `desk.Inspector` with `actor.StartMachine`, asks each act, and moves the manual clock on `wait`.

## The graph

Upstream draws its machine with `toMermaid()`. Here the graph comes from `ergo inspect machine ./permit`, and every upstream transition is one of its arms:

| Upstream | `ergo inspect machine` |
| --- | --- |
| Cart, SelectedEdition -> Cart | (Plan, Chose): Stay -> Plan |
| Cart, ClickedContinue: when shipping -> Shipping, otherwise -> Payment | (Plan, Continue): Enter -> Supplies, Enter -> Permit |
| Shipping, ClickedContinue -> Payment; ClickedBack -> Cart | (Supplies, Continue): Enter -> Permit; (Supplies, Back): Enter -> Plan |
| Payment, ToggledPaymentMethod -> Payment | (Permit, Led): Stay -> Permit |
| Payment, ClickedContinue -> Review | (Permit, Continue): Enter -> Review |
| Payment, ClickedBack: when shipping -> Shipping, otherwise -> Cart | (Permit, Back): Enter -> Supplies, Enter -> Plan |
| Review, ToggledPaymentMethod / ToggledTermsAccepted / UpdatedPromoCode -> Review | (Review, Led) / (Review, Agreed) / (Review, Edited): Stay -> Review |
| Review, SubmittedPromoCode: when a discount / otherwise -> Review | (Review, Applied) / (Review, Unknown): Stay -> Review |
| Review, ClickedPlaceOrder: when ready -> Placing | (Review, File): Ignore, Enter -> Filing |
| Review, ClickedBack -> Payment | (Review, Back): Enter -> Permit |
| Placing, SucceededPlaceOrder -> Confirmed | (Filing, Filed): Enter -> Confirmed; timer Filing -> Filed |
| shared: Cart, Shipping, Payment, Review, ClickedCancel -> Cancelled | (Plan\|Supplies\|Permit\|Review, Cancel): Enter -> Cancelled |
| shared: Confirmed, Cancelled, ClickedStartOver -> Cart | (Confirmed\|Cancelled, StartOver): Enter -> Plan |

How the port differs from upstream:

- **The code field is the host's.** Upstream keeps `promoCodeInput` in the `Review` state. Here, `Edited` tells the machine that the field changed, which clears a refusal as upstream does.
- **No edition radio group.** Upstream's `RadioGroup` child is part of the view. Here, `trip` sends `Chose` directly, and the radio group waits for FK7b.
- **No prices.** The order total and the discount line are the view's (FK7b). The allowance's percent shows in the inspector's line.

## Upstream tests

`origin/tests.txt` has a line for each of upstream's 8 story tests and 3 scene tests. No story is an Ergo test, because a law or the `tour` case already kills every mutant that a test of it would. The scene tests need a view and are stage b.

## Laws

- `permit`, on the machine `Wizard`, decided on the graph:
  - `Confirms`: some run reaches `Confirmed`.
  - `DayTripsSkipSupplies`: in every reachable stage, the supplies are an overnight trip's.
- `permit`, over every stage and signal:
  - `ReviewGatesFile`: File files exactly when a leader is named and the rules are agreed, and keeps the allowance.
  - `CancelFromEveryOpenStep`: Cancel leaves every open stage for `Cancelled`, keeping the trip's kind, and is ignored elsewhere.
  - `EditClearsOnlyARefusal`: editing the field clears a refused code and keeps an allowance.
  - `CodesIgnoreCaseAndSpace`: a code parses in any case, with spaces around it.
  - `FilingTakesASecond` (on a virtual clock): the permit is confirmed a second after the filing, not before.
- `desk`:
  - `LogKeepsTheNewest`: the log keeps the newest 20, newest first.

The mutants these laws and the cases kill are in `testdata/mutants.json`.

## Cases

- `tour`: the upstream stories in one script. It covers:
  - an overnight trip packing supplies, there and back;
  - a day trip skipping them;
  - a cancel, an ignored file, and a start over that keeps the trip's kind;
  - a known code, an unknown code and an edit that clears the refusal;
  - a file refused until the rules are agreed;
  - the office's answer, a second after the filing, with an ignored cancel meanwhile.
- `badscript`: a bad `leader` word. The program says which line and exits 1.
