# lookup

This program is the opening program of the repository's README: its first two go blocks are `hosts/hosts.ergo` and `cmd/lookup/main.ergo`, byte for byte. `TestReadmeSmoke` (cmd/ergo) fails when they differ. A lookup asks a resolver service for the address of a host name, inside a two-second timeout, and recovers from both of its expected failures.

Tag: `default`. It is an original program, not a port. Its golden is written by hand: `main` looks up `example.org` through the static provider, so it prints `192.0.2.1` (an address of TEST-NET-1, RFC 5737, kept for documentation).

## Layout

- `ergo.mod`: `module lookup`.
- `hosts/`: the domain. It declares the contract it needs, the operation type `Resolver` with its law, and the functions over it, `Lookup` and `Address`.
- `memory/`: in-memory providers of `hosts.Resolver`, `Static` and `Empty`, and the context `Listing`, which binds the contract to `Static`. Its tests run the law's check against both providers.
- `cmd/lookup/main.ergo`: the binary, the only place that chooses the providers.

## What it shows

- **Failures and services in the signature.** `Lookup` says that it can fail with `NotFound` or `timeout.Expired`, and that it needs `Resolver` and `Scheduler`. A caller that does not handle both failures, or that does not supply both services, does not compile.
- **An operation type with a law.** `Resolver` declares the `Resolve` operation and the law `Answered`: a lookup that succeeds returns an answer for the name asked, with an address. A provider that answers another name, or no address, breaks it. `ergo test` runs the law against each provider of the package, and `memory/tests.ergo` runs the law's check, `hosts.Answered`, again with fixed names against `Static` and `Empty`. A provider is a type with a function of the same name whose first parameter is that type (196m).
- **A timeout.** `timeout.After(2s) { … }` runs its block as a child task, and stops it at two seconds with `timeout.Expired`. It reads the clock from the `Scheduler` service, so a context chooses the clock.
- **Contexts across packages.** `Memory` binds `Scheduler` to a `time.Manual` and embeds `memory.Listing`, a context of another package (196b). `Missing` embeds `Memory` and replaces only `hosts.Resolver`, with `memory.Empty`.
- **Who chooses the host.** `main` lists `Ambient`, the authority to choose the host, so only `main` makes the host's scheduler: its local context `live` embeds `Memory` and replaces `Scheduler` with `NewLiveScheduler()`. A test cannot hold `Ambient`, so it runs on the `Manual`.
- **A total `catch`.** `Address` catches `NotFound` and `timeout.Expired`, so it fails with nothing, and its signature has no `fails`.
- **Tests.** `ResolveAnswers` runs `Lookup` in `Memory`. `NotFoundIsHandled` runs `Address` in `Missing`, with no mock framework.

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
./bin/ergo run examples/lookup/cmd/lookup   # prints: 192.0.2.1
./bin/ergo test examples/lookup/cmd/lookup  # prints: ok ResolveAnswers, ok NotFoundIsHandled, PASS (2 tests)
./bin/ergo test examples/lookup/memory      # prints: ok StaticAnswersTheName, ok EmptyAnswersNothing, ok law hosts.Resolver.Answered for Static and for Empty, PASS (4 tests)
go run ./scripts/examples lookup
```
