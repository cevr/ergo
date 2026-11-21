# permit-wizard and its original

What `examples/permit-wizard` ports, how it differs from the original, and how its upstream tests are restated (moved from its README, EX-NEUTRAL).

It is a port of Foldkit's `state-machine` example (`conformance/upstream/foldkit-d21db423/examples/state-machine`, MIT), whose checkout becomes the permit.
How the port differs from upstream:

- **The code field is the desk's.** Upstream keeps `promoCodeInput` in the `Review` state. Here the desk keeps it as `Code`, and the permit takes `Edited`, which clears a refusal as upstream does. The permit's signals carry no text, so its laws stay decided on the graph.
- **No radio group.** Upstream's `RadioGroup` child is a component. Here the trip's kind is two check boxes that send `Chose`.
- **Prices are the view's.** An overnight permit's fee is $40.00, a day trip's $25.00; the file button shows the fee less the allowance.
- **No graph on the screen.** Upstream's inspector shows `toMermaid()`'s text. Here the graph is `ergo inspect machine ./permit`.

## Upstream tests

`origin/tests.txt` has a line for each of upstream's 8 story tests and 3 scene tests. No story is an Ergo test, because a law or the `tour` case already kills every mutant that a test of it would. The scene tests are the case `scenes`.

## Comparisons moved from the README

- Upstream writes `when(guard, target, …)` and `otherwise(to(…))`.
- Upstream writes `Machine.forStates([...]).on(...)`.
- This replaces upstream's `deadTransitions()` and `unreachableStates()` checks.
- Upstream's `PlaceOrder` command sleeps a second and answers.
- Upstream's `update` wraps `checkoutMachine.step` the same way.
- Upstream draws its machine with `toMermaid()`.
