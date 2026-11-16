# lint-policy

A rule pack written in Ergo with std `lint`. `ergo lint` starts it for the rules of the namespace `policy`, writes one request frame to its standard input and reads one response frame from its standard output (the rule-pack process protocol v1, `docs/specs/custom-lint.md`). Its two policies are the policies of the Go pack `examples/lintpack`, and over the same program and configuration `ergo lint` reports both packs byte for byte the same (`cmd/ergo` `TestErgoPackAgreesWithGoPack`, a property over generated programs).

Tag: `default`. It is an original program, not a port. The goldens of `provider`, `failure`, `version` and `malformed` are the output of the Go pack (`examples/lintpack/cmd/policy-lint`) on the same standard input: the Go SDK is the independent model. `manifest` is the Ergo pack's own output, which `TestErgoPackAgreesWithGoPack` holds equal to the Go pack's manifest with this program's path. `usage` was written by hand.

## Layout

The program is a module of packages (`ergo.mod`: `module lintpolicy`), in the Go layout:

| folder | what it holds |
| --- | --- |
| `policy/` | The pack. `pack.ergo` (`Pack`, its preset `strict`, and the helpers that select the configured functions), `provider.ergo` (`ProviderBoundary`), `failure.ergo` (`ForbiddenFailure`). Pure: a rule is a function of the snapshot and its options. |
| `cmd/lint-policy/` | The binary: it binds the host's streams and calls `lint.Serve(policy.Pack)`, or prints `lint.Manifest(policy.Pack, "bin/lint-policy")` for `lint-policy manifest`. |

## The policies

- `policy/provider-boundary` (options `provider`, `allow`): only the configured composition functions may bind the configured provider. It reads the provider each `with` binds, as the checker resolved it, so a local of another name that holds the provider and a named context that binds it are found; a local spelled like the provider that holds another value, or a capability value passed on, are not. When the entry that binds it stands in a named context, the finding names that entry as a related place.
- `policy/forbidden-failure` (options `failure`, `functions`): the configured failure must not escape the configured functions. It reads each function's checked failure row: a failure a helper passes on escapes, one the body recovers does not. Each call whose row holds the failure is a related place.

A configured name that declares nothing fails the rule (`configured function "main.nonesuch" is not declared`), never an empty selection.

## What it shows

- **Facts as records and unions.** A rule reads `lint.Snapshot`: records with lists, never absent keys, and `Option[Span]` where a place may be missing. Each kind of fact (`CallableKind`, `CallKind`, `Family`, …) is a union, and `lint.FunctionNamed` selects functions with an exhaustive `match` over `CallableKind`.
- **Typed options.** Each rule reads its options as its own record (`providerOptions { Provider string, Allow []string }`) with `lint.OptionsOf`, through the derived codec.
- **Typed failures.** A rule fails `lint.RuleFailed`; a helper fails `Unselected`, which the rule turns into the runner's text. `lint.Serve` fails the named row `lint.ServeFailed`, which `lint.Reason` matches exhaustively; `main` turns it into `os.Exit`.
- **Explicit rows.** Every function writes its rows; only `cmd/lint-policy` binds `io.Stdin` and `io.Stdout`.

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
mkdir -p /tmp/pack/bin
bin/ergo build examples/lint-policy/cmd/lint-policy -o /tmp/pack/bin/lint-policy
/tmp/pack/bin/lint-policy manifest > /tmp/pack/policy.json
# a configuration beside the manifest: {"version":1,"packs":[{"manifest":"policy.json"}],"rules":{...}}
cp examples/lintpack/failure.lint.json /tmp/pack/
bin/ergo lint --lint-config /tmp/pack/failure.lint.json examples/lintpack/testdata/forbidden_failure.ergo
go run ./scripts/examples lint-policy
```
