# till

A shop till: a basket, a payment of one or more tenders (cards and vouchers), and a receipt screen. The till is a machine run as an actor. Its `Paying` phase hosts a child, the payment runner, and the runner hosts one child per tender, so the charges run at once. Each child's answer climbs back up as a signal of the entry that started it. When a tender is declined, the till refunds every charge that was taken before the person pays again. A script on standard input plays the person.

Tag: `default`. It is an original program, a neutral re-creation of a common checkout pattern (survey E6, renamed `till` because `examples/checkout` is the README's opening program). Its goldens were recorded from the program and read by hand.

## Layout

- `ergo.mod`: `module till`.
- `till/`: the domain.
  - `payments.ergo`: the contract `Payments` (`Charge`, `Refund`) with the laws `ChargeTakesTheAmount` and `RefundGivesBack`, and the tenders.
  - `till.ergo`: the parent machine `Till` (`Basket | Paying | Voiding | Receipt`), its step, its receipt timer and its work.
  - `runner.ergo`: the child machine `Runner`; its work starts a `Charger` child per tender.
  - `charge.ergo`: the grandchild machine `Charger`, with a work that asks the gateway and a 30 s timer.
  - `laws.ergo`: the rules of the three machines as laws, and `refusing`, a gateway for the laws.
- `memory/`: `Gateway`, an in-memory provider of `till.Payments`, and properties that run the `Payments` laws against it.
- `counter/`: the line protocol: it parses the script, runs `Till` with `actor.StartMachine`, asks it each signal, waits with `actor.WaitFor` and prints the end with `actor.ExitOf`.
- `cmd/till/main.ergo`: the binary, the only place that chooses the providers.

## What it shows

- **Children per entry, nested (MU5).** `Paying`'s work calls `actor.Child(send, machines.Start(Runner{}, plan), 1, outcome)`. The runner's work calls `actor.Child` once per tender. `outcome` maps the runner's `Outcome` to a till `Signal`, and the `send` parameter makes the checker hold that map's result to the entry's signal type. The children belong to the entry that started them: when it ends they stop, the last started first, before the next entry opens, and an answer that comes after its entry ended is dropped.
- **Several children of one machine.** One `Charger` per tender: the children are kept apart by identity, with no names.
- **Work with typed failures.** `Charger`'s work fails `Refused`, which is a signal, so it needs no failure map. `Voiding`'s work refunds the charges that were taken.
- **Timers.** A charge gives up after 30 s with no answer (`NoAnswer`). The receipt sells after 60 s with no touch; a touch re-enters the receipt (`Enter`), so its timer starts again.
- **Order that does not depend on timing.** The charges answer in any order. `OutcomeOf` lists them in tender order, and the law `OutcomeIgnoresArrival` holds it, so the output is the same in every run.
- **Rules as guards.** `Pay` must pay the basket exactly, with positive amounts; a `void` while charges run is refused, because a charge may already be taken.

## Laws (machine laws as top-level laws; MU6 is not on this base)

| law | rule | mutant it kills (measured, `mutants.sh`) |
| --- | --- | --- |
| `TendersPayExactly` | Pay starts a payment exactly when the tenders sum to the total | `>=` for `==` |
| `ReceiptOnlyAfterPaid` | the receipt opens only after every tender was taken, or on a touch | a decline opens the receipt |
| `NoVoidWhilePaying` | a void while charges run is refused | void during Paying ends the sale |
| `TakenChargesAreRefunded` | after a decline, every taken charge goes to Voiding | refs dropped |
| `OutcomeIgnoresArrival` | the outcome is the same in any answer order | refs listed in arrival order |
| `RunnerWaitsForAll` | the runner ends only after every tender answered | the runner ends at the first answer |
| `ChargeAlwaysAnswers` | every signal ends a charge with its answer; its timer is 30 s | no answer counts as taken |
| `ReceiptTouchRestarts` | a touch re-enters the receipt; its timer sells the total | touch is a Stay |
| `Payments.ChargeTakesTheAmount`, `Payments.RefundGivesBack` | the gateway contract | run against `refusing` here and restated for `memory.Gateway` |

The children's lifetimes (the children stop with their entry, and a late answer is dropped) are the runner's rules, proved by MU5's Quint model and its model-based test. The cases run them.

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
./bin/ergo run examples/till/cmd/till < examples/till/cases/split/stdin
./bin/ergo test examples/till/till      # 10 laws
./bin/ergo test examples/till/memory
go run ./scripts/examples till
```

Cases: `sale` (one card, a touch, the receipt's timer), `split` (a card and a voucher charged at once), `declined` (a declined card; a spent voucher with a taken card, refunded; then a sale), `rules` (refused pays and prices, an unknown tender, void).

## effect-machine side by side

The same till in effect-machine would use `.spawn(State.Paying, ({ self }) => self.spawn("runner", runnerMachine) …)` with a child id per child, the child's output sent to the parent by `sendParent` or a forwarded stream, `.task` for the charge and the refund, `.timeout` for the charge and the receipt, `.reenter` for the touch, and `ActorHost` if a consumer must reach the current runner.

| concern | effect-machine | Ergo here | who checks |
| --- | --- | --- | --- |
| a child's output type | the parent's event type is not checked against the child's output map (MU5 request) | `toParent` must give the entry's signal type, through `send` | compiler |
| several children of one machine | distinct string ids in the actor system | values kept by identity; no id | runner (MU5 `byKind` mutant) |
| a late child answer | the program checks a generation by hand (`ActorHost`) | dropped by the entry's epoch | runner (MU5 model and test) |
| stop order | not specified | last started first, before the next entry | runner (MU5 model) |
| re-entry restarts a timer | `.reenter` vs `.on` | `Enter` vs `Stay`, written | compiler (the decision is a value) |
| every state × signal decided | unhandled events dropped | total `match`, `(State, _): Ignore` written | compiler |
| the services a work needs | `R` of the task's Effect | `Work[Signal, Payments \| Scheduler]`: the gateway is a capability the binary binds once (`cmd/till`), not a value in every state | compiler (rows) |

Where Ergo could not improve:

- **The driver cannot reach a child.** The counter talks to the till only; children are owned by their entry (MQ10: no ambient registry). A screen that shows a charge's progress would need the child's address handed out as a signal (the `ActorHost` pattern), which this program does not need.
- Written type arguments and `Ignore` arms, as in sign-in.

## Benchmark

em-parity §3 lists "child fan-out" as pending for MU5. This program's split payment is that shape (a parent, a child, N grandchildren, one answer each). The command for an end-to-end time of the cases is in the lane's `bench-pending.md` (not run).

## Limits found

- **F9** A tuple type whose first element is a slice type does not parse (`func f(in ([]int, string))` is S001 "expected a type"); `(string, []int)` parses. Measured.
- **F13** A machine whose `initial:` is a value (`initial: Open()`) starts with no state when another package starts it: the value is set by the package's initializer, which an importing program never runs, so the first `match` on the state is a defect. Measured. `Open` takes the starting lines instead.
- **F4** (sign-in) The `Payments` laws are not run against `memory.Gateway`; `memory/tests.ergo` restates them.
- **F7** (sign-in) The examples runner does not run the laws of `till/` (a package with laws and no `test`).
