# checkout

This program is the opening program of the repository's README: its first two go blocks are `payments/payments.ergo` and `cmd/checkout/main.ergo`, byte for byte. `TestReadmeSmoke` (cmd/ergo) fails when they differ. A checkout charges a card through a payment service, inside a two-second timeout, and recovers from both of its expected failures.

Tag: `default`. It is an original program, not a port. Its golden is written by hand: `main` charges 100 through the paying provider, so it prints `paid`.

## Layout

- `ergo.mod`: `module checkout`.
- `payments/`: the domain. It declares the contract it needs, the operation type `Payments` with its law, and the functions over it, `Checkout` and `Purchase`.
- `memory/`: in-memory providers of `payments.Payments`, `Gateway` and `Decliner`, and the context `Paying`, which binds the contract to `Gateway`. Its tests run the law's check against both providers.
- `cmd/checkout/main.ergo`: the binary, the only place that chooses the providers.

## What it shows

- **Failures and services in the signature.** `Checkout` says that it can fail with `Declined` or `timeout.Expired`, and that it needs `Payments` and `Scheduler`. A caller that does not handle both failures, or that does not supply both services, does not compile.
- **An operation type with a law.** `Payments` declares the `Charge` operation and the law `Receipted`: a charge that succeeds returns a receipt with an ID, for the amount charged. A provider that receipts another amount breaks it. The compiler checks laws and does not run them yet (#162), so `memory/tests.ergo` runs the law's check, `payments.Receipted`, against `Gateway` and `Decliner`. A provider is a type with a function of the same name whose first parameter is that type (196m).
- **A timeout.** `timeout.After(2s) { … }` runs its block as a child task, and stops it at two seconds with `timeout.Expired`. It reads the clock from the `Scheduler` service, so a context chooses the clock.
- **Contexts across packages.** `Memory` binds `Scheduler` to a `time.Manual` and embeds `memory.Paying`, a context of another package (196b). `Declining` embeds `Memory` and replaces only `payments.Payments`, with `memory.Decliner`.
- **Who chooses the host.** `main` lists `Ambient`, the authority to choose the host, so only `main` makes the host's scheduler: its local context `live` embeds `Memory` and replaces `Scheduler` with `NewLiveScheduler()`. A test cannot hold `Ambient`, so it runs on the `Manual`.
- **A total `catch`.** `Purchase` catches `Declined` and `timeout.Expired`, so it fails with nothing, and its signature has no `fails`.
- **Tests.** `ChargeSucceeds` runs `Checkout` in `Memory`. `DeclineIsHandled` runs `Purchase` in `Declining`, with no mock framework.

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
./bin/ergo run examples/checkout/cmd/checkout   # prints: paid
./bin/ergo test examples/checkout/cmd/checkout  # prints: ok ChargeSucceeds, ok DeclineIsHandled, PASS (2 tests)
./bin/ergo test examples/checkout/memory        # prints: ok GatewayReceiptsTheAmount, ok DeclinerReceiptsNothing, PASS (2 tests)
go run ./scripts/examples checkout
```
