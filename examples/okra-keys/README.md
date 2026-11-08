# okra-keys

A port of `okra keys` from the owner's okra CLI (`origin`): it stores, lists, checks and removes the API keys in `~/.okra/keys.json`, a JSON object of provider name to key, and never prints a key. `okra keys set PROVIDER [KEY] [--stdin]` stores one (trimmed; merged with the others), `list` prints the stored names, `get PROVIDER` prints a masked preview and where the key resolves from (an environment variable wins: `OPENAI_API_KEY` for openai, `OKRA_KEY_<NAME>` for the rest), and `rm PROVIDER` removes one. A failure prints `[CODE] message` on standard error and exits 1; a usage error prints the command's help on standard output first, as okra's effect/cli does.

Tag: `default`. Its goldens come from the original: `go run ./scripts/examples -record okra-keys` runs `record.sh`, which runs the `okra` on `PATH`. Each case sets `HOME=.`, so both programs read and write only the `.okra/keys.json` that the case's `setup.sh` makes in the case's directory, and the path they print is `.okra/keys.json`. Recorded from okra v0.13.0. A wider comparison (35 sessions, each step's streams, status and resulting file) found the port's bytes equal to the original's.

## Layout

The program is a module of packages (`ergo.mod`: `module okrakeys`), in the Go layout:

| folder | what it holds |
| --- | --- |
| `keys/` | The domain. `keys.ergo` (`Keys` in a JavaScript object's order, `Set`, `Remove`, `Lookup`, `Names`, `IsIndex`), `store.ergo` (the contract `Store`, an operation type with the law `LoadSeesSave`; `StoreFailed`; `Same`; `Mask`), `commands.ergo` (the union `Cmd`, `Parse`, `Usage`), `help.ergo` (effect/cli's help texts), `run.ergo` (`Run`, `EnvVar`, `NotFound`, `InvalidInput`), `laws.ergo`, `tests.ergo`. |
| `keyfile/` | The file's text. `codec.ergo` (`Decode` as `JSON.parse` reads an object of strings, `Encode` as `JSON.stringify` writes it), `laws.ergo`, `tests.ergo`. Pure. |
| `files/` | `Home`, the host's `Store`: `.okra/keys.json` under the home directory, through the `fs.Files` and `fs.Writes` it holds. `tests.ergo` drives it over a file system held in a Map. |
| `memory/` | `Vault`, the in-memory `Store` for tests. |
| `cmd/okra-keys/` | The binary. `main.ergo` binds the host's `os.Process`, the standard streams and a `files.Home`, and is the only place that chooses them; it turns each failure into okra's report. Its tests run the commands over a `memory.Vault` and a process of their own. |

## What it shows

- **Typed failures for okra's error codes.** `Run` fails `Usage | NotFound | InvalidInput | StoreFailed`, one type per code where okra has one `KeysError` with a `code` string; `main` catches each by name, with no `case _`, and writes `[NOT_FOUND] …`, `[INVALID_INPUT] …` or `[STORE_FAILED] …`.
- **A contract in the domain, providers named after their dependency.** `keys` declares `Store` with the law `LoadSeesSave`; `files.Home` and `memory.Vault` provide it, and each one's tests check the law. okra's `KeyStoreService` is an Effect `Context.Service` with a `layer` and a `layerTest`; here the service is an operation type and the layers are two records.
- **The original's semantics, to the byte.** `Keys` keeps a JavaScript object's key order (array-index names first, by value), so a store written by `set` is the file okra writes; `Decode` keeps a name's first place and last value, decodes `\u` escapes and surrogate pairs, and turns anything that is not an object of strings into no keys, as okra's `Schema.Record` decode does.
- **Laws.** `keys` states five (`SetThenLookup`, `SetKeepsOthers`, `RemoveUndoesSet`, `IndexNamesComeFirst`, `MaskRevealsLittle`), `keyfile` three (`DecodeInvertsEncode`, `EncodeEscapesEveryControl`, `LastValueFirstPlace`) and `Store` one. Each package's tests restate them, and `cmd/okra-keys` checks that after a `set`, `get` shows the key's mask and `list` names it. Each law has a mutant that its own test kills.

## Run it

```sh
go build -o bin/ergo ./cmd/ergo
./bin/ergo build -o /tmp/okra-keys examples/okra-keys/cmd/okra-keys
HOME=/tmp/scratch /tmp/okra-keys keys list
./bin/ergo test examples/okra-keys/keys
go run ./scripts/examples okra-keys
```

## Cases

`nothing`, `list-empty`, `list`, `set`, `set-index` (a name that sorts first in the file), `set-stdin`, `set-empty`, `set-no-key`, `get-stored`, `get-short` (all bullets), `get-env`, `get-env-empty` (an empty variable does not win), `get-env-other`, `get-missing`, `rm`, `rm-missing`, `malformed`, `escapes`, `unknown-subcommand`, `missing-argument`, `extra-argument` and `bad-flag`.

## Divergences

- `Mask` and the order of `list` count bytes; okra counts UTF-16 units. They agree for ASCII keys and names.
- A key is trimmed with Go's `strings.TrimSpace`, which removes U+0085 and not U+FEFF; JavaScript's `trim` does the opposite.
- okra's global flags (`--help`, `--version`, `--wizard`, `--completions`, `--log-level`) are not ported: the port refuses them as unrecognized flags.
- okra makes the file 0600 after it writes it; the port cannot (G-ex-d-15).

## Limits found

- **G-ex-d-15** std `fs` has no chmod and no file mode on `WriteFile`, so a file of secrets is written 0666 less the umask.
- **G-ex-d-6** No std json: `keyfile` is a hand codec for one shape, and its byte loop needs **G-ex-d-1** (no rune to text) for `\u` escapes.
- **G-ex-d-4** Generated strings hold no quote, backslash or control byte, so the codec's laws `Spice` them.
- **G-ex-d-5** `Keys` holds a slice, so it has no `==`: `keys.Same`.
- Known from other lanes: a provider's state is a `Map` field (G-ex-b-3, `memory.Vault`, the tests' `disk`); a composite literal in an `if` header needs parentheses (G-ex-c-2); `try` stands only at the start of an expression, so `a + try f()` needs a `let`.
