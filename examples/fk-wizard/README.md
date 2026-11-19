# fk-wizard

An expedition permit, filled in step by step. You plan the trip, pack supplies (an overnight trip only), name the permit's leader, review the permit, and file it. The office confirms the permit a second later. You can cancel at any step on the way, and start over after either end. An inspector logs every transition. It is a port of Foldkit's `state-machine` example (`conformance/upstream/foldkit-d21db423/examples/state-machine`, MIT), whose checkout becomes the permit. This is stage b of FK7: the model and its view, on std ui's test host.

The program runs the desk as an actor on a manual clock, shows its screen on std ui's test host, and plays a script of the user's acts on the screen: clicks by role and name, and text typed in the code field. After each act it prints the screen in two lines: the permit's step, and the inspector with the newest log line.

Tag: `default`.

```sh
fk-wizard < cases/tour/stdin
```

The script's words are listed in `script/script.ergo`: `click LABEL`, `check LABEL`, `code TEXT`, `wait MS` and `stats`. A click on a control that is not on the screen prints `? no node`; one on a disabled control does nothing.

## The shape

- Package `permit` is the machine `Wizard` over `Stage = Plan | Supplies | Permit | Review | Filing | Confirmed | Cancelled`. Every stage keeps the trip's kind.
  - **Guards are conditions in the arms.** Upstream writes `when(guard, target, …)` and `otherwise(to(…))`. Here, `Continue` from the plan enters `Supplies` only for an overnight trip, `Back` from the permit returns there only for one, and `File` is ignored until a leader is named and the rules are agreed.
  - **Shared transitions are one arm.** Upstream writes `Machine.forStates([...]).on(...)`. Here, `(Plan, Cancel), (Supplies, Cancel), (Permit, Cancel), (Review, Cancel)` is one arm, and the two ends share `StartOver` in another.
  - The match names every stage, so a new stage stops the build until its arms are written. This replaces upstream's `deadTransitions()` and `unreachableStates()` checks.
  - **The office is a timer.** `Filing`'s timer gives `Filed` after `FilingTakes` (1 s), and the runner owns it. Upstream's `PlaceOrder` command sleeps a second and answers.
  - **A typed code is parsed at the edge.** `ParseCode` trims the code and reads it in any case. The machine takes `Applied{Allowance}` or `Unknown`, and the field's text stays the host's. So the signals can be listed, and `ergo test` decides the machine's laws on the whole graph (53 states).
- Package `desk` adds the inspector's log and the code field's text. Its signal is `Do{permit.Signal}` or `Typed{Text}`: a typed text is kept as the desk's `Code` and reaches the permit as `Edited`, so the permit's signals still carry no text.
- Package `desk`'s log: `Track` steps the permit and logs "from -> to on signal", or "signal ignored in stage". An ignored signal is a `Stay` of the desk, so a filing's timer runs on through it. The log keeps the newest 20. Upstream's `update` wraps `checkoutMachine.step` the same way.
- Package `screen` is upstream's view, the checkout made a permit:
  - each step with its heading, its controls, and Continue, Back and Cancel;
  - the trip's kind as two check boxes (upstream's radio group);
  - `Review permit` in a `ui.Fieldset` disabled until a leader is named, and `File permit · $FEE` disabled until the rules are agreed too;
  - the code field and Apply, the fee and the allowance's line;
  - the inspector: the stage and the log, newest first, each row a `ui.Same`.
- Package `script` is the host. It starts `desk.Inspector` with `actor.StartMachine`, puts `screen.Screen` on `ui.NewTestHost`, and plays each act with `ui.Play`.

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

- **The code field is the desk's.** Upstream keeps `promoCodeInput` in the `Review` state. Here the desk keeps it as `Code`, and the permit takes `Edited`, which clears a refusal as upstream does. The permit's signals carry no text, so its laws stay decided on the graph.
- **No radio group.** Upstream's `RadioGroup` child is a component. Here the trip's kind is two check boxes that send `Chose`.
- **Prices are the view's.** An overnight permit's fee is $40.00, a day trip's $25.00; the file button shows the fee less the allowance.
- **No graph on the screen.** Upstream's inspector shows `toMermaid()`'s text. Here the graph is `ergo inspect machine ./permit`.

## Upstream tests

`origin/tests.txt` has a line for each of upstream's 8 story tests and 3 scene tests. No story is an Ergo test, because a law or the `tour` case already kills every mutant that a test of it would. The scene tests are the case `scenes`.

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
  - `IgnoredIsAStay`: a signal the permit ignores is a Stay of the desk, so a timer runs on. The screen never sends one, so this law, not a case, holds it.
- `script`, over generated walks on the screen:
  - `ScreenOffersOnlyTransitions`: every enabled control the screen offers is a step of the permit, never a signal the machine ignores.

The mutants these laws and the cases kill are in `testdata/mutants.json`.

## Cases

- `tour`: the upstream stories in one script. It covers:
  - an overnight trip packing supplies, there and back;
  - a day trip skipping them;
  - a cancel, a file button that is gone, and a start over that keeps the trip's kind;
  - a known code, an unknown code and an edit that clears the refusal;
  - a review button disabled until the leader, and a file button disabled until the rules are agreed;
  - the office's answer, a second after the filing, with no Cancel meanwhile;
  - the work of the filing click (`stats`).
- `scenes`: upstream's three scene tests, on one screen in order.
- `badscript`: an unknown word. The program says which line and exits 1.
