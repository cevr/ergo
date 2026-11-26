# Testing

`ergo test FILE|DIR [--seed N] [--states N] [--law NAME] [--skip-law NAME] [--receipt FILE]` builds the tests of one `.ergo` file or package and runs them. A test is a top-level declaration, beside its functions. The rules are in [grammar.md](spec/grammar.md#declarations) (declarations 11) and [runtime.md](spec/runtime.md); this page shows how to use them.

## Tests

A test has a name and a body. `assert` takes a `bool` expression and stands only in a test or a law body (S108 elsewhere).

```go
func double(n int) int { n + n }

test DoubleOfTwo {
	assert double(2) == 4
}
```

A test with parameters is a property. It runs by the same rule as a law ([Laws](#laws)): when every parameter's type can be listed (a `bool`, records and unions of such, a type defined over one, tuples; at most 256 values a type and 4096 cases), the runner takes every value once and reports the property proved; otherwise it draws 100 cases from a seed, and the test must hold for each:

```go
test DoubleIsEven(n int) {
	assert double(n) % 2 == 0
}
```

A test can use a context, so it swaps providers with no mock framework. The README's opening program ([examples/lookup](../examples/lookup/cmd/lookup/main.ergo)) tests the same `Lookup` with a provider that answers and one that answers nothing:

```go
test NotFoundIsHandled {
	assert with Missing { hosts.Address("example.org") } == "unknown"
}
```

A test declares no `needs` row (S102): the services it uses come from a `with`.

## Diagnostics

A test of what the checker reports is a fixture file, not a test: a program whose `// want CODE` markers name each diagnostic it must get, on its line, with an optional quoted text the message must hold. `ergo check --want FILE|DIR` checks each fixture alone and prints the ones that differ ([tooling](tooling.md)):

```go
func f(o Option[A | B]) {} // want S104 "A is not an error type"
```

The checker's own families live in `internal/sketch/check/testdata/fixtures`, one folder each with a README.md that names the rule and its mutants.

## Running

```sh
./bin/ergo test examples/lookup/cmd/lookup
```

The runner prints one line per test, in source order, then a summary:

```text
ok ResolveAnswers
ok NotFoundIsHandled
ok law hosts.Resolver.Answered under Memory (100 cases)
ok law hosts.Resolver.Answered under Missing (100 cases)
PASS (4 tests)
```

A property prints its case count, `ok DoubleIsEven (100 cases)`, or `ok FlipTwice (proved: all 2 cases)` for `test FlipTwice(b bool)`; a listed case that fails prints `FAIL FlipTwice (case 2 of 2, every value: b = true): …`. A failed assert prints its position and its text, and a failed comparison also prints its two operands as Ergo literals (`left`, `right`; a line diff for two multi-line strings). A refuted property also prints the case number, the seed, the size (the case number over four, which bounds generated collections and strings; [runtime](spec/runtime.md#tests), Tests 6) and each argument as an Ergo literal of its type, so the case can be pasted into a test:

```text
FAIL DoubleIsSmall (case 1 of 100, seed 1, size 0: n = 5; shrunk from: n = 3393137394070531532; replay: ergo test tests.ergo --seed 1): assert failed at tests.ergo:12:2: double(n) < 10: left 10, right 10
FAIL (1 of 3 tests)
```

The runner shrinks a failing case before it reports it: it replays the case with simpler draws while it still fails the same way, and prints the smallest case it found, then the drawn arguments after `shrunk from:` and the command that runs the cases again. Every type a property can draw shrinks: numbers toward 0, strings, slices and Maps toward empty, a union toward its first variant. A defect is not shrunk. `--seed N` (a decimal `uint64`, default 1) selects the cases, so a failure reproduces with the same seed.

Integers shrink by their value, not by the generator's choice of small values or edges: the runner records a sign and magnitude after narrowing to the integer's width. It searches toward zero, then checks adjacent values, and revisits every component of a composite value until no pass improves it. Thus a monotone integer predicate such as `n < 5` shrinks to its boundary, `5`, when the replay budget is enough. The budget is 500 replays per failed case; reaching that limit can leave a non-minimal case. Arbitrary predicates have no global-minimum guarantee.

### Two tiers: the evaluator and the test binary

`ergo test PKG` first asks the check-time evaluator (the one `ergo check`
runs, `internal/sketch/eval`). When it decides every test and law run of
the package, `ergo test` prints the native test binary's output from it,
line for line, with ` (evaluated)` after each verdict line, and exits with
the binary's status (and its `error: K of N tests failed` line). Nothing
is lowered, built or run. A passing run writes the law ledger as a native
one does. Both tiers draw a property's or a law's cases from the same
`--seed`, so a failure names the same case, shrunk the same way.

The test binary answers the whole package (tier 2) when the evaluator
leaves any obligation undecided: a run that outgrows its budget of steps,
a skip line or a provider note, a defect (the binary stops at one), a
machine's reachability law, a failure the evaluator does not write in the
binary's words, or a host it does not have. `--native` always runs the
binary; the gate and CI use it. So do `--receipt`, `--explore`, a
schedule search and the selections `--law`, `--skip-law` and `--run`.
`ergo test DIR/...` still runs the module's one test binary (TEST-one).
`TestTiersAgree` (internal/sketch/eval) holds `ergo test` to
`ergo test --native`: the same output but the marks, the same exit status
and error line. The stage report counts `obligations_evaluated` and
`eval_steps`.

The evaluator keeps each passed test's and law run's verdict in the law
verdict store (under the emission cache's root, `laws/`), keyed by its
reach digest: the tokens of the test's declaration and of every
declaration it reaches, in any package, with the seed. The next
`ergo test` prints a verdict whose key still holds with ` (cached)` and
does not run it, so after an edit of one function only the tests that
reach it run again; a comment or a moved declaration runs none. A failure
is never stored. When the evaluator leaves an obligation incomplete (its
budget of steps runs out), it stores a route note under that obligation's
key, and while the key holds the package goes straight to the test binary
with no evaluation. Every key holds the compiler's identity, the digest
of the `ergo` executable (its lowering, rewrite ledger, evaluator,
standard library and runtime), so another compiler replays nothing. A law
verdict the test binary wrote is also keyed by the Go toolchain that
linked it, so another toolchain does not read it, and the evaluator
(`ergo check`, tier 1) reads it only when that toolchain is the pinned
release `ergo` would build with now (not under `ERGO_GO=system` or a path).
`ergo test --native`, which the gate and CI run, reads no stored verdict:
every test and law runs in the binary (it still stores what held, for
the next default run). `TestStoredVerdictsAgree` (internal/sketchfront)
holds the stored answers to a full evaluation under seeded edits. The
stage report counts `obligations_stored` and `packages_routed`.

### A module of packages

From a directory in a module, run `ergo test ./...` to select every package
below it, in dependency order. `ergo check ./...` and `ergo fmt --check ./...`
use the same selection. A prefix such as `ergo test lib/...` selects only that
subtree; dependencies outside it are checked as usual but their tests are not
selected. The nearest `ergo.mod` sets the module boundary. Nested modules,
`testdata`, hidden/underscore directories and directory symlinks are not walked.
An empty selection is an error.

A passing run's output is cached in Ergo's build cache, as `go test` caches a
passing result. A later run with the same key prints the same output, then
`ergo: cached pass replayed; --fresh runs it again` on stderr, and neither
invokes the go command nor runs the binary. `--fresh` reads no stored
result, answer or verdict, runs every obligation again and keeps each pass,
so the run after it replays again; `--count 1` runs again and keeps
nothing. The Ergo cache is the user's, so a package folder whose files
another workspace of the same tree already passed is replayed there too: its
warm answer names its folder relative to its module (`ergo.mod`), never a
workspace ([build](spec/build.md#warm-answer)). The key holds everything the
verdict reads: the test binary (the emitted module, the Go toolchain, target,
flags, tags and strip setting), the binary's arguments (source, revision, `--seed`, `--states`, `--law`,
`--skip-law`, `--run`), the memory limit, the go command (its path, size and
modification time), every `GO*`, `CGO_*` and `ERGO_*` environment variable
but `ERGO_STAGE_REPORT` (where the stage counters go),
and the package's law ledger (`ergo.laws`), which a full run writes.
A test reads no host, since only a program's entry provides `Ambient` and
`go.Foreign` ([runtime](spec/runtime.md#explored-schedules), Explored
schedules 6), so no file or other variable can change a verdict. A failing run
is never cached. `--count 1` (the only count) always runs, and so do
`--receipt` and `--explore` runs.

Test output names each package by its module import path. A package with no
test and no law is neither built nor run, as `go test` reports `[no test
files]`: its initializers, such as a `cmd/<name>` main package's lets, never
run. Its run is the one its test binary would make: `PASS (0 tests)`, a
receipt with no law run under `--receipt`, the binary's usage error (exit 2)
for a `--run` name, and `FAIL no law NAME in this package (--law)` (exit 1)
for a `--law` name. This holds for one package and for `./...`; only a
schedule search (`--explore dfs|pct|random`) still builds it. An assertion or law
failure does not hide later packages: the command exits 1 if any selected package
failed. A defect or signal stops the run and keeps its status. `--seed`, `-work`
and target options keep their per-package meaning. Recursive check writes one
JSON check envelope per package, one per line, and exits 1 if any failed.

`ergo test DIR/...` and `ergo check DIR/...` load the module once: std is
imported once and each package is checked once, its one check serving both its
importers and its own root; while a package's own test build is lowered or its
obligations evaluated, it is named `main`, as checking it alone names it. A
selected test package (its path a package's path with `test` after it) that
another package imports is checked a second time, as `main`, because the
builder rule reads its path. `ergo test` links one test binary that
holds every selected package's test build, lowered through IR as its own build is, and runs it once per package, so each
package prints what `ergo test` of that package alone prints, with the same
receipt and the same exit status rule (TEST-one). When the store already holds
every package's own test binary (an earlier `ergo test DIR/...` linked them),
each package runs its own and nothing is lowered, compiled or linked. When the
build of the one binary fails, `ergo test` says so once on stderr with the go
error's first line, and with `ERGO_SHARED_FALLBACK=fail` (set by the tests and
the gate) the run fails instead. When the packages cannot share
one load or one build (a diagnostic, a lowering refusal, a Go build failure, a
module binding in `ergo.bind`), and for `-work`, `--explore` and a schedule
search, each package is checked and built alone, as before, so a failure is
reported where it belongs; `ergo check DIR/...` likewise checks each package
alone when they cannot share one load.
Formatting still uses syntax only; a type error does not prevent formatting.

### Affected tests

```
ergo test ./... --affected main          # run only the tests and law runs whose reach changed since main
ergo test ./... --affected main --list   # print them and run nothing
ergo test ./... --affected main --why    # one reason line per obligation
ergo test ./... --affected main --json   # the comparison as JSON; runs nothing
```

An obligation (a `test` block, or a law run under a provider, a context or a
machine search) is affected when its reach digest in the working tree differs
from its digest at BASE, and new when BASE has none of its name; both run. The
reach digest is the one the law ledger and the verdict store key on: the
obligation's declaration and every function, type, package `let` and context
it reaches at any depth and in any package, by their tokens. So a comment, a
reformat or a moved declaration affects nothing, and an edit in an imported
package affects every obligation that reaches the edited declaration. BASE's
sources are read from Git objects (no checkout) through the loader that reads
the working tree, so both sides admit their files by one rule. Without BASE,
`--affected` compares with the merge base of HEAD and the default branch
(origin's HEAD, else `main`, else `master`).

It fails closed: a changed `ergo.mod`, `go.mod`, `go.sum`, `ergo.laws` or
`ergo.cases` affects every obligation of the module; a changed file other than
an `.ergo` source in a package's folder (a binding, an embedded asset) every
obligation of that package; a package missing or not checking at BASE every
obligation of it. A test deleted since BASE is reported `gone` and runs
nothing. The compiler is not compared: `--affected` assumes one `ergo` built
both sides' answers.

A package with an affected obligation runs as `ergo test` runs it, and the
verdict store replays the law runs it already holds; the other packages are
left out. `--affected` picks, the store replays: it answers the same on a
runner with an empty cache. Reasons read `test Parses: new`, `law
Counter.Joins for Tally: reaches func lib.Tally (lib/tally.ergo:4), changed`,
`every obligation: ergo.mod changed` and `none: nothing it reaches changed`.
`--affected` takes no `--law`, `--skip-law`, `--run`, `--receipt`,
`--emitted`, `--explore` or `-work`; `--list` and `--why` need it.

## Exit status

| Status | Meaning |
| --- | --- |
| 0 | every test passed |
| 1 | at least one test failed; the other tests still ran |
| 2 | a defect (a Go panic, for example a division by zero) stopped the run |

A defect is not a failed assert. The runner prints the test's `FAIL` line, `FAIL Divides: a defect, which stops the run`, then the runtime's report of every reason with the Ergo frames, and stops: no later test runs.

## Builds

`ergo test` builds a test binary apart from the program's. `ergo build` and `ergo run` contain no Go from a test, so tests add nothing to the program. `-work` prints the directory of the generated Go module on standard error, as `WORK=DIR`.

## Laws

`ergo test` runs every law of the package after its tests ([#162](https://github.com/cevr/ergo/issues/162) law-run-1; [types.md](spec/types.md#laws) rule 10). A top-level law runs once. A member law runs once against each provider of its operation type, with a provider value per case, wherever the provider is declared: the laws that a domain package's `Store` declares run in the `memory` package that declares its provider, when `ergo test` tests that package (the least package that sees the law and the provider runs it; law-xpkg). A law whose parameter is an operation type runs once per provider of that type too. A helper that `needs` the type runs against that provider. One rule decides each run: when the providers and every parameter have a type that can be listed (a `bool`, records and unions of such, a type defined over one, tuples; at most 256 values a type and 4096 cases), and no provider is built by its constructor, the run takes every value and is reported proved; otherwise it takes 100 generated cases, as a property does, and is reported tested:

```text
ok law Flags.Merge (proved: all 8 cases)
ok law Store.PutCountsOne for Tally (100 cases)
FAIL law Store.PutCountsOne for Stuck (case 1 of 100, seed 7, size 0: provider = Stuck{at: 0}, n = 0; shrunk from: provider = Stuck{at: 3}, n = -9): law refuted at main.ergo:4:2: the law is false
	replay: ergo test main.ergo --law Store.PutCountsOne --seed 7
skip law Runner.Runs: its operation type Runner has the row parameter E, and a run binds only type parameters
```

A refuted law's replay is a line of its own, which reruns that law alone (`--law`, X1b); a test's replay stays in its `FAIL` line.

### `always` and `sometimes` in laws

Laws support two contextual property-testing forms (`always` and `sometimes` remain contextual words, never scanner keywords, so types and locals named `always` or `sometimes` keep working):

- **`always <bool>` and `sometimes <bool>` statements inside law bodies** (including inside function literals nested in a law, such as `gen.Check` callbacks):
  - `always e` evaluates `e` once and refutes the case immediately (with exact `file:line:col`, `<expr>`, shown comparison operands, and shrinking) if `false`.
  - Every `sometimes e` site in a law body — including inside `if`/`match` arms, loops, and nested closures — is **statically registered before case 1 runs**. Executing it increments its hit counter when `e` evaluates `true`. After all cases finish without refutation, if any statically registered `sometimes` site has zero hits (whether never `true` or in an unreached branch), the law fails naming that exact site and expression (`file:line:col: sometimes <expr> was never true across N cases`).
  - Use `always` / `sometimes` directly instead of hand-rolled `sync.NewRef(false)` witness flags — see for example [examples/csvreport/report/faulty_gen_laws.ergo](../examples/csvreport/report/faulty_gen_laws.ergo), [modules/infra/memory/faulty_model_concurrent_tests.ergo](../modules/infra/memory/faulty_model_concurrent_tests.ergo), and [modules/web/faulty_model_tests.ergo](../modules/web/faulty_model_tests.ergo).
- **Existential (`law Name sometimes(...)`) and universal (`law Name always(...)`) law headers:**
  - `law Name always(...)` has the ordinary universal semantics.
  - `law Name sometimes(...)` is existential: it searches across the law's listed domain or drawn case budget and passes immediately on the first case whose body returns `true` with no `always`, `assert`, or escaped failure (`witness found at ...`). Any `always` failure refutes and shrinks immediately; if every case returns `false`, the law fails (`no witness found across N cases`).
  - See `FilterSometimesKeepsReadings` in [examples/csvreport/report/faulty_gen_laws.ergo](../examples/csvreport/report/faulty_gen_laws.ergo), `CanResolveAppRelease` in [examples/deps/resolve/faulty_tests.ergo](../examples/deps/resolve/faulty_tests.ergo), `WebRouterSometimesMatchesItem` in [modules/web/faulty_model_tests.ergo](../modules/web/faulty_model_tests.ergo), and `WorkToolSometimesPinsWorkspace` in [modules/work/memory/faulty_model_tests.ergo](../modules/work/memory/faulty_model_tests.ergo).

Both forms run identically at check time (`ergo check`) and natively (`ergo test --native`).

### Stored regression cases (`ergo.cases`)

When a drawn property (a law or a parameterized test) is refuted during `ergo test`, its shrunk draw tape and current reach digest are appended to `ergo.cases` beside `ergo.laws`:

```text
property app.SortLaw tape h1:<reach-digest> 0,1,2
```

Every later `ergo test` replays all stored tapes for each property **before** drawing random cases (at case 0), regardless of `--seed`:

- When a stored tape's reach digest matches the property's current reach digest and fails, it is reported at `case 0, stored case`.
- When the property or its generator changed (the reach digest differs), the stored tape still runs as an extra case and the output explicitly marks it `extra case, digest changed` rather than naming it as the old failure.
- A stored tape that now passes stays in `ergo.cases` as a permanent regression case.
- Run `ergo test --prune-cases [TARGET]` to replay stored tapes and remove only those that now pass (plus entries for deleted properties).
- `ergo test --affected` treats a changed `ergo.cases` as affecting every obligation of the module.

A test that cannot run on this host says so at run time with the statement `skip "reason"`, as Go's `t.Skip` does:

```ergo
test ClonesFromHost {
	if !hasGit() {
		skip "git is not on this host"
	}
	assert clone("origin") == "ok"
}
```

`ergo test` prints `skip ClonesFromHost: git is not on this host`, runs no more of the test's cases, and counts it as a skip: `PASS (1 tests, 1 skipped)`. `ergo check` reports the obligation `skipped` with the reason and the statement's line. The statement stands only in a test or law body, and its reason is a string literal that is not blank. `skip` is a contextual keyword, so a function or a local named `skip` is still legal ([runtime](spec/runtime.md#tests), Tests 4).

A law no case can feed is a check error (S108), not a skip: a law parameter with no generator, or a provider with no generator, no constructor and no named context of a package of its module that builds it ([types](spec/types.md), Laws 10). In a module it is judged over the module, so `ergo check ./...` or `ergo test ./...` from the module's root reports it, and a check of one package does not. A provider whose state is a std handle, such as a `sync.Ref`, has no generator; declare its constructor `func NewTally() Tally` (no rows) and each case builds the provider with it, or bind it in a named context of a test file, `let overMemory = context { Store: NewTally() }`; a provider with a constructor is always built by it. A law that cannot run for another reason (an operation type with a row parameter, say) prints a `skip` line with the reason, and the summary counts it: `PASS (2 tests, 1 skipped)`. The package of an operation type with no provider of its own prints `law Store.PutCountsOne runs where Store has a provider: none is in this package or its imports`. A skip line that the module's `expected-skips` file (beside `ergo.mod`) does not list fails `ergo test`, exit 1, in `examples/` and everywhere else ([runtime.md](spec/runtime.md), Tests 4). A law of a generic operation type is checked and not run yet. So a program need not restate a law as a property test.

`ergo test DIR --receipt FILE` writes the run's law receipt to FILE: a JSON object with the schema version, the source, its revision, the seed, the target and one entry per law run, `proved` (a listed run, every value), `tested` with its case count, `refuted` with its counterexample, or `skipped` with the reason; a machine's reachability law has the search's report in place of a case count ([runtime](spec/runtime.md#tests), Tests 8). A drawn law that holds is tested, not proved. Without `--receipt` nothing is written.

For `ergo test DIR/... --receipt PATH`, PATH is a directory: each tested package writes its canonical receipt to `PATH/<module>/<package>.json`. The directory is created as needed; an existing file at PATH is an error. An earlier package's refutation is never overwritten by a later package. With `--law NAME`, packages that have no law of that report title are checked but not run. If no selected package has that law, the command fails once. Single-package `--law` keeps its missing-law error; `--skip-law` keeps its meaning in every package.

`ergo run` and `ergo build` contain no Go from a law. The [support table](spec/support.md) lists what each build lowers.

## Check-time evaluation

`ergo check FILE|DIR [--seed N] [--eval-steps N] [--eval-bytes N]` runs the tests and laws of the root package that are pure, in the checker's own process, with no Go build and no child process (L7). It runs only after the source checks: a source with a diagnostic is not evaluated. The result is the `evaluation` object of the check envelope ([tooling](tooling.md#check-envelope)): one record per obligation, in the order `ergo test` runs them (the tests in source order, then the package's law runs), and a summary status.

The rules are the rules of `ergo test`, not a second test language:

- A test with no parameters is an example and runs once (`evaluated`). A property or a law runs by the one rule of [Laws](#laws): every value of a type that can be listed (`proved`), else 100 cases drawn from the seed (`tested`). For one `--seed`, `ergo check` and `ergo test` draw the same cases, so a refuted law names the same first bad case at both. A refuted drawn case is shrunk as `ergo test` shrinks it (the same replays of its draws, at most 500), so both name the same smallest arguments, and the drawn ones beside them. The evaluator writes a failed assert's report as the test binary does: its site, its source text, and, for a comparison of printable values, `left` and `right` (with a line diff of strings over several lines).
- A machine's reachability law is decided by the same explorer as in `ergo test` ([types.md](spec/types.md#laws), Laws 13). A law that a run proves (`reaches`, `path`) may stop at the shortest witness before it has seen the whole graph (`router-model`'s `CanStop` sees 9 of its 16 states); `never reaches` is proved only by a complete search. An invariant law (`always p`, `sometimes p`; X2) is decided as `ergo test` decides it: `always p` never reaches a state where `p` is false, and `sometimes p` reaches one where `p` holds. The predicate is a pure Ergo function of the state, and check time calls it at each state the search visits.
- An imported package supplies definitions only: its own tests do not run. A law runs in the least package that sees the law and its provider, as in `ergo test`.
- A record that did not pass has `replay`: the `ergo test` command that runs the same cases natively, `ergo test limit --seed 1 --law RefillNeverOverfills`.

**Effects run through providers.** A capability runs through the provider the nearest `with` binds, as in `ergo test`: an inline, a local or a named context builds its providers (`With.Acquired`, in order) and binds each capability its block needs; a function that needs a capability takes its caller's provider; a value converted to an operation type (`via(t)` where `via` takes a `Store`) calls the provider's functions. A member law runs with its provider bound to its own operation type, and a law under a named context that binds its owner (types.md, Laws 10) builds that context for each of its 100 cases. A `with` that acquires a resource releases each provider it built when its block ends, on every exit (the normal end, a return, a typed failure, a defect), last built first, each once, in the environment outside it, and a failure while it builds releases the providers built before (resources.md, Context ownership as built); a law case under a context releases what its build acquired after the law's last operation. A release's defect is the outcome unless the block's own outcome is a defect. A `with` of a context value (`with log.Annotate(k, v) { … }`) binds its text for the block, and `log.Annotation` and the `trace` reads see the innermost binding; a `Context[R]` value builds its providers where it is made, and a `with` of it binds them. A provider is an Ergo value, so running one changes only the evaluator's values: `io.Recorder`, `io.Input`, `fstest.Memory`, `rand.Seeded` and a program's own memory providers run; a host provider (`io.NewHost()`, `fs.NewHost()`) reaches a host function, and that obligation is skipped at the call.

**Natives.** A std function with no Ergo body runs only when the evaluator has its own implementation of it (`internal/sketch/eval/natives.go`), written from the std's spec and Go's standard library, never by calling `runtime/v1`: the pure `strings`, `strconv`, `utf8`, `filepath`, `hex`, `cmp`, `slices` and `maps` functions the examples call, the pure `time` duration functions (`DurationText`, `Times`, `Divide`, `Count`, `ParseDuration`, and the `Sub` of two readings), `sync.NewRef`, `sync.Modify` and a Ref's operations, the stream of `rand.Seeded`, `bits` (Go's `math/bits`), `sha256.Sum` and `slices.Repeat` (`nativesbits.go`), and time's `Format` and `Parse` in UTC (`nativestime.go`). `embedded[T](pattern)` is the checker's asset snapshot: a string or a `[]byte` of the file, or an `embed.FS` read with Go's embed rules, and `embed.NewEmpty` is an empty one (`nativesembed.go`). `slices.WithCapacity`, `SortStableFunc` and `Max`, `math.Trunc`, arena's storage and a machine timer's defect are natives too (`nativesmore.go`). `timeout.After` races its body against a deadline on the run's Scheduler (`nativestimeout.go`), and the simulated process has no terminal: `term`'s modes fail `NotTerminal`. `encode` and `decode[T]` walk `T` at run time by the Codecs rules (`codecwalk.go`): the events of rule 2 on the `Writer` or `Reader` operations, the declared codec forms, by-name pairs and refinements through their functions. std json's `Text` and `Cursor` and `open` are natives (`nativesjson.go`): `open` parses the checked text into a tree and fails `Malformed` at the first byte that breaks a rule, and a `Cursor` walks the tree; `TestJSONAgreesWithRuntime` holds open's verdicts, a cursor's answers along a decode-shaped walk and a `Text`'s bytes to `runtime/v1`. std config's key spellings and `Redacted` are natives (`nativesconfig.go`). std `math` and a program's own extern of Go's `math.Log`, `math.Exp` or `strings.TrimRight` call the same Go function (`nativesgo.go`); any other program extern is a skip. So do the pure functions of Go's standard library that compiler/syntax calls through `import go`: `unicode.IsLetter` and `IsDigit`, and `strconv.Quote`, `QuoteRune`, `Itoa` and `FormatInt`. Any other `import go` function is a skip. A program's extern of Go's builtin `append` runs as append does, and std strings' `Builder` and `Bytes`/`FromBytes` and `cmp.Max`/`Min` are natives (`nativesbuilder.go`, `natives.go`). std http's checks before the network are natives (`nativeshttp.go`): a network policy's origins and limits, a fetch's malformed request or refused origin, and a `Listen` address that cannot bind (Go's own text); `TestHTTPPolicyAgreesWithRuntime` holds them to `runtime/v1/http`. The simulated process has no network: an exchange, a bound listener and a body file a fetch fills are skips. `fs.OpenRoot` opens a directory of the simulated file system; a read through the root is a skip. `TestNativesAgreeWithRuntime` holds each one to `runtime/v1`'s body on generated inputs. The task, queue and clock externs below are natives too.

**Owned values.** A value a frame owns outside a `with` (an owned local or taken parameter, a temporary a statement owns, a pattern's bindings, a task's moved captures) is released where the checker's release plan says, as in the built program: at its scope's end and at each exit that leaves it, last owned first, each once, masked. A use that hands it off (a `takes` parameter, a move, a returned value) ends the frame's ownership there, and an exit action (`scope.OnExit`) runs in the same last-first list as the function's owned values.

**Tasks.** The obligation's body is the root task; `task.Go`, a `tasks.Set` member and the like start tasks of their own, and every task runs on one evaluator thread. A seeded cooperative scheduler runs a task until it waits (a `Wait`, a queue's `Push` or `Pop`, a `Sleep`, an `Advance` settling the others) or reaches a loop's back-edge while another task can run, and then picks the next task by the obligation's schedule seed, which derives from `--seed`, the obligation's name and its case. One seed is one schedule, run again the same way, and a result that started a task records its schedule; exploring the schedules a program may take stays with `ergo test --explore`. A `testing.VirtualClock` moves to its nearest sleeper's deadline when no task can go on; a `time.Manual` moves only by its `Advance`, which returns once the tasks it woke have run to their next wait. A stop is cooperative: a stopped task ends at its next interruption point (a capability call or a loop's back-edge in a frame with effects, each blocking operation) unless it runs a release. When no task can go on and no clock can move, the obligation is a `defect` that names what each task waits for.

**Borrows.** A `*T` parameter holds a borrow of its caller's place (`&x`, or a dot call's implicit borrow of its receiver): a read through it reads the place as it is now, and a write through it writes the place, as the Go target's pointer does. The checker keeps a borrow from being stored, returned or captured, and two borrows of a call from overlapping ([types.md](spec/types.md#borrows)), so the evaluator's borrow is the place itself.

**Actors.** std actor's Go body `start` is run by the evaluator's own runner (`internal/sketch/eval/runner.go`), on the tasks of the schedule: one runner task steps one envelope at a time from one FIFO inbox (Sends, Asks, State reads, timer and work signals, Feeds); the sink gets an Emit's or a Done's output before anything else sees the decision; an entry's timer sleeps on the starter's Scheduler and its work runs on a task of its own, both stopped before the next entry opens, and a signal of an ended entry is dropped; Stop, Drain, Changes, ExitOf, Latest, Feed and Supervise's restarts follow the std package's contracts. A Work literal builds its `Start` from Run or Listen and its maps. Children (`Child`, `ChildIn`) and hosts (`NewHost`, `Get`) run as the std package states (`runnerkids.go`): a child's outputs are its owner entry's signals, its defect ends the parent, and the entry's end stops its children, last started first, before its timer and work; a host belongs to the entry its step enters or keeps, and the entry is shown once its work filled its hosts, waited or ended. A durable actor's recover runs with its hosts unbound, and the recovered actor's first entry binds each one its first state holds and closes the rest, as a supervised restart's fresh does (`runnerdurable.go`); a save runs in the step before the commit. A failed save or fresh (a defect that writes the failure's Go text), a `Child` no entry owns and a `ChildIn` on a host its entry cannot fill (defects whose text names the Go build's call site) are not run yet (a skip).

**A program's main (the reference).** `eval.RunMain` runs a program's `main` as the built program would, against a process the evaluator simulates: the arguments, environment and standard input it is given, the standard output and error it records, a working directory (also `$HOME`) in a file system of its own, empty or seeded with `Process.Files`, and a clock that moves only when every task waits. The host's `Console` and `Ambient` are bound as a build binds them, and the std host functions of `io`, `os`, `config`, `fs`, the host clock and the live `Sleep` run against that process; a terminal's raw mode, a child process and the host's random source stay skips; the network is a loopback of the process's own listeners (`http.Listen`, `http.Serve` and a Client's send, over Go's net/http on an in-memory pipe), and any other host, TLS or a streamed body stays a skip. An `os.Exit` that leaves `main` writes its message to standard error and exits with its code; a defect writes `panic: ` and its message (the built program adds a stack) and exits 2. `ergo check` never uses it: under check a host function is still a skip. Two tests hold it to the built program. `TestEvalAgreesWithLowering` runs seeded `main` programs (owned resources, tasks over a queue, sleeps on a virtual clock, a stopped task, standard input, arguments, standard error, an exit code) and requires the model's streams and status in every tier, and in the full tier the same from the program `ergo build` makes. `TestEvalAgreesOnExamples` (full tier) runs each example case, its `setup.sh` first (run with `sh` in an empty directory, whose tree, symbolic links included, seeds the simulated one; a special file there is a skip), and requires its recorded `expected.out`, `expected.err` and `expected.status`; a case that reaches a skip is listed with its reason and counted, never passed, and the log's census line gives the counts. `TestEvalSharesNoLowering` holds the evaluator to its own code: its packages reach no package of the lowering, and import `runtime/v1` nowhere.

**What is skipped.** The evaluator never runs Go code and never touches the host. An obligation that reaches any of these is `skipped`, with the reason, the site and the replay command, and `ergo test` still runs it:

- a capability that no `with` of the run binds, and a task that a package initializer starts (it has no obligation's scheduler to run on);
- a `tasks.Set` that logs its failures to standard error, `tasks.Race`, `tasks.Transform` and an actor's supervisor (Go functions with no native yet);
- a release through a generic `Close`, a `Close` that borrows its receiver, or the values a std operation type holds; a `Context` value whose provider is a resource;
- a Go function that has no native, an `extern go` function of the program (an extern of Go's `append` runs, as the prelude's `Append` and string concatenation do), or a host function (`a Go function (ParseDuration), even a pure one`). Every std extern marked pure, and every pure Go function a std body reaches through `import go`, has a native (`TestEveryPureStdExternHasANative`), so a std law over one runs at `ergo check`; a read-only Go variable std passes to one (`crc32.IEEETable`, base64's encodings) is a value only that native reads, and any other Go package variable is a skip;
- a function that borrows its receiver, a method, or a generic function whose type parameter an operation type constrains used as a value (a function literal, a package function and a generic one's instance are values, called where they are called), a call that passes its `CallSite`, and a trailing block of a Go function that has no native (a generic call whose type parameter an operation type constrains runs: each operation on a value of the parameter calls the function the instance's witness names, as the test build does);
- an outcome that the order of a `for` over a Map decides, and a `for` over a Map that the loop adds to (Go leaves the order unspecified, and may or may not visit an added entry). A run that ranged over a Map runs again with every Map visited in the opposite key order, and an outcome counts only when both runs reach it (`maporder.go`). Within a run the loop follows Go with no snapshot: each value is read when its entry is reached, and a deleted entry is not visited. A `for` over a Seq that a function makes, `Seq[T](f)`, calls it with a yield that runs one iteration and returns false once the body ended the loop, as Go's range over a function does;
- a write that replaces the slice an active `for` ranges over (its place or a place on its path), a refined literal, a `testing.Script` (a derived provider whose field type is a runtime Go type; one whose field type is declared in Ergo runs, its `NewF` and `Call` as the test build calls them) or pointer provider, and a machine timer that is not positive (a machine declaration's value `X{}` converts to the prelude `Machine` or `InputMachine`, with its step, after and work functions, and a fold runs it);
- a package initializer or a machine's initial value that cannot run: every obligation of the package is skipped, never run with invented values;
- a machine state that holds a std type, whose key the test build may take from its runtime value;
- a law with no binding in this package (`no binding of the law runs in this package`). A law parameter with no generator never gets here: the checker refuses it (LAW-UNFED).

A skip is not a pass. A package whose obligations are all skipped is listed in full, and its summary status is `skipped`.

**Budgets.** The work is bounded by counts, never by time. `--eval-steps N` is the number of steps, 1,000,000 by default: each evaluated node, call, loop turn, drawn value, copied element and machine step (each edge of a search is stepped once) costs a step before it is done, and a std function costs the bytes and elements Go's function reads (a substring reads none of its text, a prefix test its prefix). `--eval-bytes N` bounds the bytes of the values the evaluation builds, 64 MiB by default: a conservative count of the allocations, which is never freed, and not a measure of the process's memory. The call depth is at most 1024. `--seed N` selects the drawn cases, 1 by default, as for `ergo test`. Each option takes a positive decimal number (the seed may be 0); another value is an invalid invocation. The step and byte budgets bound the package's initializers and then each obligation apart, so an obligation that runs leaves the whole budget to the next. The step budget is each case's: a test with parameters or a law that runs n cases (100 drawn, or every listed value) may spend n times `--eval-steps` in all, spread over its cases as they need, so a property costs per case what a plain test costs (TP-BUILT-FAST); a test without parameters, a machine search and the initializers have one case's. When a budget runs out, the obligation in progress is `incomplete`, and the next one runs; when the initializers' budget runs out, the first obligation is `incomplete` and each later one is `not run`, with the budget in the reason. Neither is a pass. The `steps` and `bytes` of the evaluation are the whole run's; `stepsLimit` and `bytesLimit` bound each obligation.

**Statuses and exit.** An obligation is `passed`, `refuted` (a false assert or law body, or an escaping typed failure; the run goes on), `defect` (it stops the run, and the later obligations are `not run`), `skipped`, `incomplete` or `not run`. The summary status is the worst: `defect`, `refuted`, `incomplete`, `skipped`, `passed`, or `none` when the package has no obligation. `ergo check` exits 2 on a defect, 1 on a refutation (or when the source does not check), and 0 otherwise. A skipped or incomplete obligation leaves the exit status 0, but the summary never reads `passed` for it.

**Receipts, not timings.** Each record carries `steps`, and the evaluation its total `steps` and `bytes` of their limits. Steps are a reproducible count of the work, not a time: the same source, seed and compiler give the same steps on any host. No time is measured or claimed. The two example programs [`examples/pure-eval`](../examples/pure-eval/README.md) (a rate limiter: top-level laws, sampled and listed, and a std function run as a native) and [`examples/router-model`](../examples/router-model/README.md) (a router's server machine: reachability laws, body laws and a top-level law) record their check-time verdicts and steps in their READMEs, with a mutant that both `ergo check` and `ergo test` refute.

The examples runner checks every package at both seams in one run. Each check-time pass must agree with the native verdict, evidence and case count (machine searches compare verdict and evidence). Check-time skips and exhausted budgets remain explicit, with their reasons; the native test still runs. See the [test leg](../examples/README.md#test-leg).

A false law names the expression that returned false, including a chosen nested tail or explicit return, at both seams. Its declaration is related information: `declaration` beside `site` in check JSON, and an indented `law declared at file:line:column` line in the native report. A reachability law names its clause, since it has no false body expression.

## Privacy-preserving differential verification

When migrating a service, serializer, or LLM pipeline from a baseline implementation to a candidate implementation, `std/testing` provides a privacy-preserving checkpointed differential harness alongside deterministic LLM replay in `std/llm/memory`:

- **Redacted structural fingerprints (`testing.DiffFingerprint`, `testing.CompareRedacted`, `testing.CompareRedactedText`, `testing.FingerprintKey`)**: compares baseline and candidate bytes and returns `option.None` when identical, or a `DiffFingerprint` carrying only `(Step, Piece, FirstDiffOffset, BaselineLen, CandidateLen, Tags)` — storing and formatting zero payload bytes so customer prompts or secrets never leak into logs or grouping keys.
- **Deduplication and fair cohort comparison (`testing.GroupByFingerprint`, `testing.CohortCompare`, `testing.MatchesCohort`)**: buckets redacted fingerprints by canonical `FingerprintKey` (ordered by `Count` descending, then `Key` ascending) and compares before/after snapshots filtered strictly to records matching every required cohort tag (`["prompt_caching", "tools"]`, etc.).
- **Checkpointed dual execution (`testing.CheckpointLog`, `testing.Record`, `testing.RecordText`, `testing.CompareCheckpoints`, `testing.Differential`, `testing.RunShadow`)**: runs `baseline` and `candidate` with isolated checkpoint logs, appends the encoded final `"output"` checkpoint, and stops at the earliest diverging `(Step, Piece)` so input-stage bugs are separated from output-stage bugs.
- **Deterministic two-sided LLM inner loop (`llm/memory.ScriptedModel`, `llm/memory.NewScriptedModel`, `llm/memory.ScriptFromReplies`, `llm/memory.ForkScript`)**: replays an ordered script of `ScriptReply` (text, reasoning, tool calls, stop reason, usage), `ScriptRaw`, and `ScriptFault` steps while recording every incoming `llm.Request` (`RecordedRequests`, `CallCount`, `Remaining`) and failing closed with `llm.InvalidResponse` when exhausted. Forking a script with `ForkScript` lets both `baseline` and `candidate` execute against identical downstream model replies locally in sub-second runs without touching the network.

## Example programs

Each program of `examples/` runs its tests and laws in the gate: `go run ./scripts/examples` runs `ergo test <program>/main.ergo` for every program of one folder, and `ergo test <folder>` for every package folder of a program of packages, and fails the program when a test fails ([examples/README.md](../examples/README.md)).
