# checkout

This program is the opening program of the repository's README, byte for byte. `TestReadmeSmoke` (cmd/ergo) fails when the two differ. A checkout charges a card through a payment service, inside a two-second timeout, and recovers from both of its expected failures.

Tag: `default`. It is an original program, not a port. Its golden is written by hand: `main` charges 100 through the paying provider, so it prints `paid`.

## What it shows

- **Failures and services in the signature.** `Checkout` says that it can fail with `Declined` or `timeout.Expired`, and that it needs `Payments` and `Scheduler`. A caller that does not handle both failures, or that does not supply both services, does not compile.
- **An operation type.** `Payments` declares the `Charge` operation. A provider is a type with a function of the same name whose first parameter is that type (196m): `Gateway` pays, and `Decliner` declines every charge.
- **A timeout.** `timeout.After(2s) { … }` runs its block as a child task, and stops it at two seconds with `timeout.Expired`. It reads the clock from the `Scheduler` service, so a context chooses the clock.
- **Contexts.** `Live` binds `Payments` to `Gateway` and `Scheduler` to the host scheduler. `Declining` embeds `Live` and replaces only `Payments`.
- **A total `catch`.** `purchase` catches `Declined` and `timeout.Expired`, so it fails with nothing, and its signature has no `fails`.
- **Tests.** `ChargeSucceeds` runs `Checkout` in `Live`. `DeclineIsHandled` runs `purchase` in `Declining`, with no mock framework.

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
./bin/ergo run examples/checkout/main.ergo   # prints: paid
./bin/ergo test examples/checkout/main.ergo  # prints: ok ChargeSucceeds, ok DeclineIsHandled, PASS (2 tests)
go run ./scripts/examples checkout
```
