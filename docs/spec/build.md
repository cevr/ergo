# Build

How `ergo build`, `ergo run` and `ergo check` reuse work between requests.
The emission cache and the direct path's action store are in
[tooling](../tooling.md#native-build-path); this file holds the rules that
decide when a request is answered without doing the work again.

## Warm answer

A request that changed nothing since its last answer is answered from the
store: it parses, checks, lowers, compiles, links and starts nothing
(compile-speed plan CS2, and CS8 for `ergo test`; `internal/sketchfront/warm.go`,
`cmd/ergo/warm.go`).

1. **The request key.** A request is its command (`build`, `run`, `check`
   or `test`), the admitted package folder (for `ergo test ./...`, the
   folder it walks, as its own request) and the path as spelled, the options that change its
   answer (target, platform, `--locked`, `--json`, `--seed`,
   `--eval-steps`, `--eval-bytes`, `--states`, `--law`, `--skip-law`,
   `--run`), for a test the bytes of the law ledger a full run writes, for
   a build or a test the toolchain (the go
   command's resolved path, size and modification time, GOROOT, GOOS and
   GOARCH, GOFLAGS, tags, the reference path and the strip setting), the
   compiler's identity, every `GO*`, `CGO_*` and `ERGO_*` variable that can
   change an answer, and the Go experiments
   `GOEXPERIMENT` turns on, from the environment or the go env file, since
   they choose a module's Go body files (docs/spec/interop.md, Trusted Go
   3). The output path is not part of it. A table classifies the
   variables (`warmEnv`, `internal/sketchfront/warm.go`). An answer
   variable is in the key: the target, the Go build's flags, the toolchain
   and the checker's switches. A location variable is left out: caches,
   temporary folders (the run budget gives each run its own `GOTMPDIR`),
   the stage report, the Ergo cache and the budget's scheduling. A variable
   the table does not name is in the key, and `ergo version` lists it until
   it is classified. `TestWarmEnvClassifiesTheScripts` holds the table to
   every such variable the repository's scripts set.
   For `check` and `test` the folder is written relative to its module's
   (the folder of the nearest `ergo.mod`: `@/` and the slash path,
   `lawcache.Within`), and the manifest names each input below that folder
   the same way, read under the module of the request that looks it up: so
   the Ergo cache, one per user, answers every workspace that holds the
   same tree, and a lane's first check of a package another lane checked is
   warm (SHARED-RESULT-CACHE). A path outside the module stays absolute. An
   answer whose text names the module's folder (as written or with links
   resolved) is not kept. A `build` or `run` stays its workspace's: its
   executable names its sources' paths.
2. **The manifest.** One file per request key in the Ergo cache
   (`Root/warm/KEY`) lists every input the request read: each file with the
   SHA-256 and size of its bytes (the `.ergo` files, `ergo.mod`, the
   embedded assets, `go.mod`, `go.sum`, binding files and the law ledger),
   each folder it listed with a digest of its names and their types, so a
   new file is seen, and each path it looked up with what it found
   (missing, a folder, a file). It holds the answer: the digest of the
   executable's blob in the direct path's store, the bytes `check`
   printed, or a passing test run's output (the test result cache's
   record, [testing](../testing.md)), which `test` replays with the
   replay note. For `ergo test ./...` it is each package's result in
   order, written only when every package passed and printed nothing on
   stderr, and its inputs include every folder discovery listed, so an
   added or removed package is seen. A failing run writes none. Only a request that succeeded and printed nothing on stderr
   writes one.
3. **The warm check.** The next request with the key reads the manifest,
   lists the folders, looks the paths up, and reads and hashes every input
   file once, up to eight at a time. Every hash, listing and lookup equal,
   and the executable's blob still in the store: `check` prints the stored
   bytes, and `build` makes sure the output is the blob (below). Anything
   different runs the normal path, which reads each file the check already
   read from those bytes, never from disk again, and writes a new manifest.
4. **Content only.** No modification time is read: the bytes are the only
   evidence. A file rewritten with the same bytes or touched is a hit; a
   file whose bytes changed is a miss whatever its size and time. A stat-first
   check (Zig's manifests, git's index) is a later candidate (CS2b), with this
   check kept as its reference.
5. **The output is a hard link.** A direct build places its output as a hard
   link of the executable's blob, read-only (mode 0555). The warm check of the
   output is "the store has the blob and the output path is that same file"
   (two stat calls). A deleted or replaced output, or one that is another file
   of the same size, is placed again as a link; nothing is built. The design
   model `models/warmstamp` proves that this answer is fresh in its scope with
   two builders, a trim at any time and reused inode numbers, and refutes the
   designs that record the placed file's inode number, keep one record per
   folder, or compile what is on disk instead of the bytes hashed. An output on
   another file system than the cache is a copy, written again on every
   request; that case is outside the warm budget.
6. **What takes the normal path every time.** A program of one file, `ergo
   build -work`, `ergo test` with `--count 1`, `--explore`, a schedule
   search or `--receipt`, `ergo test ./...` with `--law` or `--receipt`
   (each package then replays the test result cache,
   [testing](../testing.md)), a compiler with a process-scoped identity, a
   managed Go release not yet installed, and every request with
   `ERGO_BUILD=full`, the reference path, which neither reads nor writes a
   manifest.
7. **Trim.** The cache's trim removes a manifest no request used for its age
   limit, as it removes the direct path's files; a hit marks its manifest and
   its blob used.

`TestWarmRequestDoesNoWork` counts a second identical build and check: every
work counter 0, and the bytes read and hashed are the inputs' bytes.
`TestWarmAnswerAgreesWithFullBuild` runs seeded sequences of edits (the same
bytes again, a touch, a byte of a source or an asset with size and time kept,
a file added, removed or renamed, `--seed`, an `ERGO_*` variable, another
compiler, the output deleted or replaced, a blob trimmed, two builds at once)
against `ERGO_BUILD=full`: equal exits, check output and executable bytes
after every step, and the least work the model allows.

## Warm answer by tokens, `ergo check DIR/...`

`ergo check DIR/...` with one shared load of its module keeps its answer by
what a check reads, not by bytes (MONOREPO WS0; `cmd/ergo/checktree.go`).
The answer is kept in the law-verdict store (`laws/v1`), not in a warm
manifest.

1. **The package key.** A package's key is the request's options, the
   environment and the compiler (WarmKey's parts, with no request folder), its
   import path, its own folder relative to the module's (a skipped
   obligation's replay command names it; no workspace's folder is in a key,
   and the manifest's paths are relative too), whether the load checked it as `main` or as its path, each file's
   name and check text (`syntax.CheckText`: the token text, then each
   comment's text with the token after it and the line breaks between them,
   since the checker reads a binding's `// ergo:` lines by the line above a
   declaration), and the keys of the local packages it imports. An edit
   anywhere a package reaches changes its key; a layout edit (spaces, line
   breaks away from a comment) keeps it, and a comment edit does not.
2. **The request's manifest.** Under the request's folder and the same parts
   it lists every other input the load read, by bytes (`ergo.mod`, `go.mod`,
   `go.sum`, the law ledger, assets, folder listings, missing paths), the
   packages the checks reached with their imports, and each package's with
   work. The with budget is the one part of a check read from a file's size
   (`check.WithLimit`): a hit needs each package's work to fit its files'
   size now.
3. **The answer printed again.** A kept envelope is printed with the
   revision of the files as they are now, and each obligation's site where
   its token now is (`syntax.TokenSites`; a site is kept as the ordinal of
   the token it starts at, in the file that declares the test or law).
4. **What is not kept.** A request with a failing package, a diagnostic, a
   reason that names a site (`at FILE.ergo:LINE`, which a layout edit would
   move), advice (it may quote the source), a site at no one token, a
   package of a required
   module, an answer that does not print again as it printed, and a run that
   stored a verdict (its next run prints that verdict "cached").
5. **All or nothing, then the cutoff.** The request hits with no load when
   its manifest holds and every printed package's key finds its answer.
   Otherwise the shared load checks every package again, and each package's
   evaluation is taken from the store when nothing it can observe changed
   (MONOREPO WS0b, `cmd/ergo/checkcut.go`). That means four things are
   unchanged:
   - its source fingerprint: tokens, `// ergo:` markers, the path it is
     checked as, and the environment;
   - its folder;
   - the export fingerprint of each local import (item 2 of [the
     fingerprints](#interface-fingerprint));
   - the inputs that are not sources, by bytes.

   When its answer holds an obligation, the implementation fingerprint of
   each import must be unchanged too, because evaluation runs imported
   bodies. So a comment edit evaluates nothing again. A body edit evaluates
   again the edited package and every package above it that holds an
   obligation. A package that reaches no edited package is taken as it
   was. A package whose check now has a diagnostic is evaluated. An answer
   is kept for the cutoff by the rules of 4, from an evaluation that stored
   no law verdict. Which imported packages an evaluation entered (its read
   set) is WS0c. `ERGO_BUILD=full` and `ERGO_LAWCACHE=off` take the normal
   path.

`TestCheckTreeAnswersByTokens` (`cmd/ergo`) runs seeded modules and edit
scripts (none, layout, a comment, a body, a signature, a new file, a test, an
imported package outside a sub-folder request). After every edit it checks:
- the answer equals the normal path's;
- no file is parsed and no obligation evaluated exactly when nothing the
  request reaches changed in its tokens, comments or comments' line breaks;
- the packages the cutoff evaluates are exactly those the rule above names,
  by the fingerprints of a reference load.

## Pin archives

What a pin compiles the same way on every machine ships built, so a cold
build downloads it instead of compiling it (compile-speed plan §3.3, CS3;
`internal/pinset`, `internal/sketch/build/pin.go`,
`internal/toolchain/pin.go`). A pin is the Go release, the IR patch digest
(`toolchain/ir/lock`) and the flags a build compiles with.

1. **The sets.** Three kinds, each a set per pin and per target:
   - `std`: the archive and export data of every standard package the
     target builds (`go list -export -deps std`), per **target** OS/arch
     and level: objects do not depend on the host.
   - `runtime`: the archive of every Ergo runtime package (`runtime/v1`,
     `runtime/rt`, `runtime/explore`, `runtime/testgen`), keyed by its
     compile's inputs as the direct path keys a compile, with the set's
     name in place of the tool. Per target and host: its archives carry
     the direct path's build IDs, which hold the host compile's digest.
   - `tools`: ergo's patched `compile`, per **host** OS/arch. Go's `link`
     and `asm` come with the release, which the toolchain fetch verifies
     already ([tooling](../tooling.md#go-toolchain)).

   Each tag set Ergo builds with (none, and `ergoexplore` for `ergo test
   --explore`) is its own std and runtime set. Ergo std as Go packages
   (after CS4) is a fourth kind with no sets yet: until then std's bodies
   are lowered into each program.
2. **The name.** A set's name is its kind, release, target and level, then
   16 hex digits of a digest of the whole key (release, patch digest,
   flags: `-trimpath`, cgo off, `-shared` where the target is position
   independent, the tags, and the host for a runtime set). Two keys that
   differ in any field have two names (`TestPinSetNameKeysEveryInput`), so
   a set is never used for another pin or target.
3. **The lock.** `toolchain/pin/lock` names the release, the patch digest
   and every set a release ships by the SHA-256 of its tar. A set is a tar
   of an `index` (one `NAME DIGEST EXPORT` line per entry) and the entries'
   bytes, each named by its SHA-256, written in sorted order with fixed
   headers, so equal entries make equal bytes. It is served compressed
   (`NAME.tar.gz`), so the compressor is no input of the digest. `go run
   ./scripts/pinsets -o DIR -lock` builds the sets of this tree into DIR
   and rewrites the lock; `TestPinArchivesMatchLocalBuild` builds the
   linux/amd64 and darwin/arm64 sets (the lock's targets; the release
   step adds the rest of the platform list with `-target`) and checks the lock's digests, so a runtime, std or
   patch change that leaves the lock stale fails the full tier. Publishing
   the sets is a release step of its own.
4. **The fetch.** Sets install under ergo's cache as
   `toolchain/pins/NAME`. A missing set the lock names is fetched from
   `ERGO_PIN_URL/NAME.tar.gz` (http, https or file) when `ERGO_PIN_URL` is
   set; none is fetched otherwise. The download expands into `tmp-*` while
   the tar is hashed and each entry is checked against its name, and it is
   renamed into place only when the tar's SHA-256 is the lock's, so an
   installed set is a whole verified set or nothing.
5. **The use.** The direct path's plan (`go/plans`) takes each standard
   archive from the std set of its pin and target, listing the packages
   with `go list -deps`, which compiles nothing; a compile that misses the
   action store takes the runtime package's archive from the runtime set
   and records the action. `ergo build` takes the patched compile from the
   tools set before it would build one. With the sets installed and every
   other cache empty, a build compiles only the program's own packages
   (`TestColdBuildCompilesNoPinPackage`; the stage counter `std_exports`
   counts the standard archives the go command gave a plan).
6. **The fallback.** No set, no URL, or a set the lock does not name: the
   build is the build without sets, quietly. A set that fails to fetch,
   whose SHA-256 is not the lock's, or that lacks a package the build
   needs is refused with one line on stderr (`ergo: pin archives NAME
   refused: …; building … once instead`, or `… NAME lack PKG; …`), and the build compiles what the
   set would have held once on the machine, into the plan and the action
   store, as it did before sets. The executable is the same bytes either
   way (`TestRefusedArchiveFallsBack`).

## Interface fingerprint

After an edit, a package whose check would come out as it did last time need
not be checked again: its source is the same and so is everything it sees of
its imports. Three fingerprints per package decide it (compile-speed plan
§5.3, CS5a; `internal/sketch/check/fingerprint.go` computes them, `internal/sketch/fingerprint` compares them). Today they decide only.
The check still runs every package: a package's snapshot names its imports'
objects by global id, so it cannot outlive a new check of an import, and
reuse waits on references by name (CS5b).

1. **Source fingerprint.** A hash of the package's import path, the path it
   is checked as (`main` for the root), its environment, and each file's
   name and token text (`syntax.TokenText`: kinds and literals, no comment,
   no space, no position) with its `// ergo:` markers (`syntax.MarkerText`,
   from the parse: a binding's markers decide the facts its functions
   carry), in name order. The environment is the module name, the
   platform, `go.mod` and `go.sum`, the module bindings the check read and
   the assets the package embeds. Any other comment edit, or a layout edit,
   changes no fingerprint. A file that does not parse leaves the source fingerprint
   empty, and an empty fingerprint equals none.
2. **Export fingerprint** (`Fingerprint.Interface`). A hash of what an importer can see. It covers
   each file's token text and markers with two kinds of span cut out of the
   text: the body of each
   function that is not generic, and the body of each test. It also covers
   the package lets a body
   assigns, the cell conversions the package makes, and the export
   fingerprint of each import. What stays covers every declaration's
   signature with its written rows, every type definition with its `where`,
   every operation type with the text of its laws, every top-level law,
   every generic constraint, every let with its initializer, and every
   generic function with its body. An importer's instance of a generic
   function may read its body. An importer's test build runs the laws of an
   operation type it imports on its own providers. The facts a function
   `gives` and `keeps` and record invariants are not built yet
   ([facts](facts.md)); when they are, they stand in the declarations' text.
   A package that was not checked, or that has a diagnostic, has no export
   fingerprint.
3. **Why it includes the imports'.** A type an import declares reaches an
   importer's importer through a let, a result or a field, with no token of
   it in the middle package (`let Sig = a.Sig`). A law of an import runs in
   the least package that sees it and a provider, at any depth (types.md,
   Laws). So a change in an import's export fingerprint changes that of
   every package above it. An exported signature edit therefore makes every
   package above it check again. A rule that stops at a middle package whose
   own view did not change needs the references by name of CS5b.
4. **Implementation fingerprint.** A hash of the source fingerprint and the
   implementation fingerprint of each import. Evaluation (tests, laws) runs
   imported bodies, so its results are keyed by it, never by an export fingerprint.
5. **The decision** (`fingerprint.Reusable`). A package can be taken as it was when
   it was clean last time, its source fingerprint is the same, and each of
   its imports has the same export fingerprint, which is not empty. Its
   own export fingerprint is then the same too, so the decision runs bottom up from
   the leaves, as OCaml's and ReScript's stop at an unchanged `.cmi` and Go's
   at unchanged export data.

`TestIncrementalCheckDecides` runs seeded edit sequences over a
three-package program, `a ← b ← c`. The edits are a comment, a body, a
signature, an export added or removed, a written row, a helper's row that
a function value carries, a law, a `where`, a constraint, a file added or
removed, and an import added.
At every step, each package the decision names has the check it had last
time: the same diagnostics, function types, expression types, lets, law runs
with their text, and tests. The from-scratch check is the reference. A
comment edit leaves every clean package reusable, and a body edit leaves
every one but its own. Two checks give a package the same implementation
fingerprint exactly when its import closure has the same sources. `go run
./scripts/buildcost reuse` counts what the decision would reuse after each
edit of generated programs and examples
([receipts](../receipts/compile-speed.md#incremental-check-cs5a)).

## Workspace

A workspace is the modules below one folder, its root (MONOREPO WS1). A
command that names `DIR/...` (`check`, `test`, `fmt`, `lint`, `graph
export`) runs over the workspace at `DIR` when `DIR` holds an `ergo.work`
or no module holds `DIR`; inside a module it takes that module's packages
below `DIR`, as before. `internal/sketchfront.DiscoverWorkspace` finds it.
Inside a module, `DIR/...` uses the same admission rule as discovery: it
leaves out `testdata`, hidden and `_` folders and, since WS1, also
`node_modules` and `vendor`, and `ergo.bind`, the module's Go bindings,
which `import go` reads and no walk checks as a package
([interop](interop.md#go-modules) 2). It is one rule,
`sketchfront.AdmitFolder`, for every walk: `check`, `test`, `fmt`,
`lint`, the task runner's members and the gate task's key (which still hashes
the root's `ergo.bind` files, as inputs). A package named by its folder
(`ergo check app/vendor/x`) still checks: the rule chooses what a
pattern walks, never what a named folder is.

- **Members.** Every folder below the root that holds an `ergo.mod` is a
  member, the root too, and a module nested in another is a member of its
  own. Discovery never enters a folder whose name starts with `.` or `_`,
  `testdata`, `node_modules`, `vendor`, `ergo.bind` or a folder `ergo.work` skips,
  and it follows no directory link: those hold fixtures and copies.
- **Edges.** A member resolves another locally when a `replace` line of its
  `go.mod` names the other's folder (`./x`, `../x` or absolute). Each
  pair is one edge. A remote replacement, or a folder that is no member,
  is no edge.
- **Divergences.** A member whose `go.mod` requires another member's Go
  module by version with no `replace` builds against the module cache's
  copy, not the one beside it, and no edge joins them (a local edit does
  not reach it). `ergo lint DIR/...` at a root reports each as
  `workspace-divergent-require` at the `require` line, naming both
  members, the version and the fix (`replace PATH => ../member`), and
  exits 1.
- **Once each.** `DIR/...` at a root selects every member's packages once,
  a member after each member it resolves locally (members in a cycle keep
  folder order), and each member loads as its own module.

### ergo.work

The file is optional. Its grammar, in lines; `//` starts a comment that
runs to the end of the line, and blank lines are ignored:

```
file    = version { key } .
version = "version" N .          // the first line; N = 1, 2, ...
key     = "skip" FOLDER          // version 1 knows this key only
        | "gen" NAME WORD { WORD }      // version 2 (Generators)
        | "reads" NAME PATH { PATH }    // version 2 (Generators)
        | "task" NAME STEP              // version 3 (Tasks)
        | "after" NAME TASK { TASK }    // version 3 (Tasks)
        | "env" NAME VAR { VAR } .      // version 3 (Tasks)
```

`FOLDER` is a clean, slash-separated folder below the root (no `..`, no
`.` element) that exists; no member is found in it or below it. A key the
file's version does not define, or a version newer than the binary reads,
is an error naming the line, never ignored: a later key changes what a
run does, so a binary that cannot read it must not run as if it were
absent. Later versions only add keys; `skip` keeps its meaning in each.
`TestWorkFileRefusesWhatItCannotRead` holds the refusals and
`TestWorkspaceDiscoveryAgrees` the discovery, over seeded trees of
modules.

### Generators

A **generator** writes files of the tree that are checked in: a lowered
Go package, a codec, a snapshot, a ledger. Version 2 of `ergo.work`
declares one in two keys, and nothing else about it is written down:

- `gen NAME COMMAND...` names it (`NAME` is lower-case letters, digits
  and `-`, starting with a letter, once per file) and gives its command:
  `go run ./DIR ARGS...` or `go test ARGS... ./DIR ARGS...`, with one
  argument naming the package as `./DIR`. The words are split at white
  space and run with no shell. Another command is a foreign task (MONOREPO
  WT2), refused here.
- `reads NAME PATH...` names files and folders the generator reads that
  its Go build does not hold (`PATH` as `FOLDER` above, existing or not),
  after the `gen` line it is about; a folder counts with everything below
  it. A generator may have several `reads` lines, or none.

There is no key for outputs and none for the environment: the runner
observes the outputs, and the Go environment is in every key.

`ergo task gen [--check] [--fresh] [NAME...] [DIR]` runs the generators
of the `ergo.work` at `DIR` (default `.`); `ergo task --list [--json]
[DIR]` prints them with their edges and order (`internal/task`,
`cmd/ergo/task.go`).

A member's `ergo gen` packages are a generator with no line, named
`MEMBER:gen` (`MEMBER` its folder): every package below the member (not
in a nested member) whose folder holds `ergo gen`'s marker `ergo.gen`
(its kind and `subject PATH.NAME` lines; [api.md](api.md)) is
written again as `ergo gen KIND` writes it, from the contract in the
folder above, in the process. Its inputs are every file and folder below
the member and below each member it requires (the workspace's edges,
transitively), and the identity of the `ergo` binary; its class is
`derived`, since nothing in the key is declared by hand. Members come in
the workspace's order (each after the members it requires), so a first
run already respects the requires edges; after it, their order is by
what they read, as for every generator. A package whose marker names a
contract other than the folder above it was not written by `ergo gen` in
place (the `linked` generator writes std `code`'s Reader hub and command
line into `codetool`): it is no member's, and some generator's last run
must have written it, or `ergo task gen` fails naming it, since nothing
would keep it current; `--list` names its writer.

- **Inputs.** A generator reads its Go build, every file `go list -deps`
  (with `-test` for `go test`) names below the root, Go, assembly,
  header and embedded files of each package and its tests, and each
  module's `go.mod` and `go.sum`; each such package's folder and the
  folder of each embedded file, by the names it holds; the paths of its
  `reads` lines, by content, and their folders by names; and the Go
  environment (`go env GOVERSION GOOS GOARCH GOFLAGS CGO_ENABLED
  GOEXPERIMENT GOAMD64 GOARM64`).
- **Outputs** are the files whose stat (size, times, inode) a run
  changed, and those of its last run that still exist. So a generator
  writes each of its outputs on every run, the same bytes too (Go's
  build cache keys on content, so an unchanged rewrite costs no build): a
  generator that leaves an unchanged file alone shows that output, and
  its edges, only once the file changes, and until then the passes run
  again what read it.
- **Order.** A generator runs after each one that writes a file it reads,
  or a file in a folder it lists. The edges come from the generators'
  last runs; a generator never run has none, so after each pass the
  edges are read again and one whose inputs a later generator wrote runs
  again, until a pass runs none. Once every generator has run, a pass
  runs each at most once. Ties go by the order of the `gen` lines.
  Generators that read each other's outputs (a cycle: a compiler's
  generators, each built with the others' files) run in the order of
  their `gen` lines, pass after pass, until a pass runs none of them;
  `--list` names each cycle. A run settles in at most k+1 passes that
  run something, k the number of generators in its largest cycle (1 with
  no cycle: one pass to learn the edges, one in order); a generator that
  still misses after them keeps changing what another reads, and the run
  fails naming it rather than loop for ever.
- **Hits.** A generator's result is kept (the law-verdict store, under its
  own identity) under the content of its inputs after the run, with the
  digest of each output; the next run's key is over the files its last
  run read, so an edit that moves its Go build (an import, a new file in
  a package folder) is itself in the key. A generator is a hit, and does
  not run, when the key holds and every output hashes as kept. A failed
  run is never kept. Each hit prints its class: `vouched: declared
  reads`, since the key is only as complete as the `reads` lines. The
  last run is kept per tree (by its root), the results by content alone,
  so checkouts that share the cache share results but never read each
  other's last runs. Each miss prints its reason: no last run, the first
  line of the environment that changed, the first input file whose digest
  or folder whose names moved since the last run, or the output that no
  longer hashes as kept; a miss never goes without one.
- **`--check`** writes nothing in the tree: at the first miss the tree is
  copied (blocks shared where the file system can) and the generators
  that miss run in the copy; the run fails naming each file the copy's
  generators wrote that differs from the tree's. So it fails exactly when
  a fresh regeneration would change a generated file, as far as the
  `reads` lines are complete.
- **`--list --affected BASE [--why]`** runs nothing: it prints the
  generators a regeneration may run that BASE's would not (WT2a). Those
  are a generator whose key over its last run in this tree differs
  between the commit `BASE` and the working tree (a file it read changed,
  or the names in a folder it listed), one whose output differs, one with
  no last run, and every generator that runs after one of these. `BASE`
  is read from Git (`git diff`, `git ls-tree`), with no checkout; files
  Git ignores count as present on both sides. `--why` names each one's
  first moved input. Like the round tier it is advisory: a lane learns
  which regeneration its edit owes. The gate's `generated` step is
  `ergo task gen --check` in every tier, and `--check --fresh` in the
  full and merge tiers.
- **`--fresh`** ignores hits in its first pass: every generator runs. When
  a generator was a hit and the fresh run writes an output that differs
  from the kept one, its `reads` lines are incomplete, and the run fails
  naming it. Landings and batch heads run `ergo task gen --check
  --fresh`; lanes may use hits.
- **Budget.** With `$ERGO_BUDGET` set, the run takes `--jobs` cores (default
  4) from the host budget once (`internal/budget`; a grant lent by a parent
  `ergo budget` takes nothing) and gives each generator `GOMAXPROCS` and
  the lent grant.

`TestGenCheckIsFreshRegeneration` (`internal/task`) holds this, a seeded
property over workspaces of generators that read data, list folders and
read each other's outputs in a shuffled declaration order, and over edit
scripts, against the reference "run every generator until nothing
changes": `--check` writes nothing and is stale exactly where the
reference changes a file, `gen` leaves the reference and runs each
generator once and in order, a second `gen` runs none, and `--check
--fresh` names a generator that reads a file it does not declare.

`work.toml`'s `derived` lines say how `work derive` remakes a generated
file after a merge; for a file a `gen` line writes, the line's `by` is
`ergo task gen NAME`, so the merge tool and the runner run one
declaration.

### Tasks

A **task** is a list of steps that `ergo task NAME [DIR]` runs over the
workspace at `DIR` (default `.`; `cmd/ergo/task.go`, `taskgate.go`,
`gate.go`; the runner is `internal/task`, `verdict.go`). There is no
`ergo gate` command: `ergo task gate` is one task. Version 3 of
`ergo.work` declares tasks in three keys:

- `task NAME STEP` is one step of task `NAME`; a task has one line per
  step, in order. A step is a built-in verb (`check`, `fmt --check`,
  `lint`, `test`, `gen --check`), the name of a `gen` line (that
  generator runs, writing), `gen` (every generator runs, writing), or
  another task's name (that task's steps). No task is named `gen`, nor
  after a `gen` line.
- `after NAME TASK...` starts `NAME` once each `TASK` has passed: for an
  order no file shows.
- `env NAME VAR...` names host variables `NAME`'s steps read; their
  values are in each of its steps' keys, and its generators' children get
  them.

A name, a step or a variable the version does not define, a task that
waits for itself through steps or `after` lines, and an `after` or
`env` line of no task are errors naming the line. Every workspace has
`gate`, written or not: with no `task gate` line its steps are `check`,
`fmt --check`, `lint`, `test` and `gen --check`, and a written `task
gate` line replaces the default whole. There is no syntax for edges
between members: a member's edges are its `go.mod` replace lines, which
discovery reads, and each step's key holds what it reads of the members
it reaches.

- **Plan.** A task runs the tasks of its `after` lines first, then its
  steps in order, a step naming a task standing for that task's plan; a
  task runs once per run. A built-in step but `gen --check` runs once at
  each member, in the workspace's order (each member after the members
  it reaches); `gen --check` and the generator steps run once over the
  workspace (Generators). A step two tasks plan runs once and is
  reported for each. If a task fails, the steps that wait on it do not
  run and report which prerequisite blocked them. `ergo task --list
  [--json]` prints each task's plan.
- **Keys.** Each built-in step states what it reads (`gate.go`):
  `check`, `lint` and `test` at a member read every file of the member
  and of every folder its `go.mod` replaces a module with, transitively;
  its law ledger as the run left it; the toolchain, the options and the
  `GO*`, `CGO_*` and `ERGO_*` environment; and the task's `env`
  variables. `fmt --check` reads the formatter's identity and the names
  and bytes of the member's `.ergo` files outside generated trees with a
  current marker. A step whose key holds a pass that the runner kept (the
  law-verdict store, under `internal/task`'s identity) replays it and
  does not run; a failed or flaky verdict is never kept. A verdict is kept
  under the key as the run left the tree when only the law ledger moved
  (`test` writes it, `check` and `lint` read it). `gen --check`
  keeps no verdict of its own: the generators' store replays each
  generator whose inputs hold. Each miss names the first input that moved
  (`--why` prints it), never none.
- **`--affected BASE`** runs only the steps whose inputs hold a path that
  `git diff --name-only BASE` lists or that is untracked and not ignored
  (a folder of the key standing for every path below it; `fmt --check`'s
  inputs include `.ergo` files, marker paths, and all files a present
  marker covers): selection by the step's inputs, not by member. The others
  print `unaffected` and count as passed against `BASE`. Among the
  selected steps the keys decide, as in a plain run.
- **`--fresh`** ignores kept verdicts: every selected step runs; a kept
  pass that the fresh run fails is a contradiction (the step's key misses
  an input) and fails the run. Landings run `ergo task gate --fresh`.
- **Flaky.** A `test` step that fails runs once more; a pass then is
  `flaky`: never kept, appended to `task/flaky.txt` under the cache root
  with its failing seed (when its output names one) and its log. A plain
  run passes a flaky step and lists it; `--fresh` counts it a failure, so
  no flaky step reaches main as a pass.
- **The child's environment.** The runner starts every child (a
  generator's `go` command, `go list`, `go env`, `git` for
  `--affected`) through one helper (`internal/task/child.go`;
  `TestChildrenStartInOnePlace`) with an environment it states, and
  nothing else: the host's `GO*` and `CGO_*` variables, `PATH`, `HOME`
  and `TMPDIR`, the variables of the task's `env` lines, and the
  budget's `GOMAXPROCS` and lent grant. Every entry is in the key of the
  generator that starts the child (the lent grant by its presence: its
  value is this run's lease, not an input); a host variable no line names
  never reaches a child. The program is looked up on the host's `PATH`,
  which the stated `PATH` repeats, until std exec's lookup on the
  child's own `PATH` replaces it (STD-EXEC-ENV; row TASK-GATE-EXEC-POINT).
  Built-in steps run in the `ergo` process, not as children.
- **Output.** A step's whole output goes to a log file under the cache
  root (`task/logs`). In text, one line per failed step (`FAIL check
  app: <first diagnostic>`) or flaky one, then the summary `gate: 118
  passed (97 replayed, 21 unaffected), 1 failed, 1 flaky`. `--json`
  prints one object per step, `{"task", "step", "member", "verdict":
  "pass|fail|flaky|unaffected", "replay": "hit|miss", "why", "class",
  "ms", "log", "seed", "first"}`, and last `{"summary": {"verdict",
  "passed", "failed", "flaky", "replayed", "unaffected"}}`, its `failed`
  and `flaky` lists carrying each step with its seed and log.
- **Exit.** 0 when every step passed (a flaky one too, but with
  `--fresh`); 1 when a step failed or a kept pass was contradicted; 2 for
  an invalid invocation or an `ergo.work` that does not read.

`TestTaskGateAgreesWithRunningEverything` (`cmd/ergo`) holds this over
seeded workspaces of 3 to 6 members with random edges and seeded edits
(a layout break, a comment, a body, a signature, a law broken, a file no
package reads): each step's verdict equals the reference "run every
step on every member with no store", the steps that run are exactly
those whose inputs moved since they passed, and `--affected` selects
exactly the steps whose inputs hold a changed path.
`TestVerdictsAgreeWithRunningEverything` (`internal/task`) holds the
runner's kept verdicts, flaky retries, `--fresh` contradictions and
`--affected` selection against a reference model.

*Prior art:* Turborepo's `dependsOn` with `^` and declared `inputs`
globs, whose `affectedUsingTaskInputs` selects as `--affected` does
here; Nx's merged targets and `namedInputs`; Bazel's `test_suite`,
hermetic actions and `--flaky_test_attempts`; mise's `run` steps and
`depends`; go test's cache keyed on the files and environment a test
read. Each declares what a step reads, or keys on mtimes; here the
compiler states it, so no declaration can miss an input but a `gen`
line's `reads`.
