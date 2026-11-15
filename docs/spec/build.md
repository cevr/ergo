# Build

How `ergo build`, `ergo run` and `ergo check` reuse work between requests.
The emission cache and the direct path's action store are in
[tooling](../tooling.md#native-build-path); this file holds the rules that
decide when a request is answered without doing the work again.

## Warm answer

A request that changed nothing since its last answer is answered from the
store: it parses, checks, lowers, compiles, links and starts nothing
(compile-speed plan CS2; `internal/sketchfront/warm.go`,
`cmd/ergo/warm.go`).

1. **The request key.** A request is its command (`build`, `run` or
   `check`), the admitted package folder, the options that change its
   answer (target, platform, `--locked`, `--json`, `--seed`,
   `--eval-steps`, `--eval-bytes`), for a build the toolchain (the go
   command's resolved path, size and modification time, GOROOT, GOOS and
   GOARCH, GOFLAGS, tags, the reference path and the strip setting), the
   compiler's identity, and every `GO*`, `CGO_*` and `ERGO_*` variable but
   `ERGO_STAGE_REPORT` and `ERGO_BUILD`. The output path is not part of it.
2. **The manifest.** One file per request key in the Ergo cache
   (`Root/warm/KEY`) lists every input the request read: each file with the
   SHA-256 and size of its bytes (the `.ergo` files, `ergo.mod`, the
   embedded assets, `go.mod`, `go.sum`, binding files and the law ledger),
   each folder it listed with a digest of its names and their types, so a
   new file is seen, and each path it looked up with what it found
   (missing, a folder, a file). It holds the answer: the digest of the
   executable's blob in the direct path's store, or the bytes `check`
   printed. Only a request that succeeded and printed nothing on stderr
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
   build -work`, `ergo test` (its verdict cache is the test result cache,
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
   no space, no position), in name order. The environment is the module
   name, the platform, `go.mod` and `go.sum`, the module bindings the check
   read and the assets the package embeds. A comment or layout edit changes
   no fingerprint. A file that does not parse leaves the source fingerprint
   empty, and an empty fingerprint equals none.
2. **Export fingerprint** (`Fingerprint.Interface`). A hash of what an importer can see. It covers
   each file's token text with two kinds of span cut out: the body of each
   function that is not generic, and the body of each test. It also covers
   the rows the checker inferred from those bodies, the package lets a body
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
signature, an export added or removed, a written row, an inferred row, a
law, a `where`, a constraint, a file added or removed, and an import added.
At every step, each package the decision names has the check it had last
time: the same diagnostics, function types, expression types, lets, law runs
with their text, and tests. The from-scratch check is the reference. A
comment edit leaves every clean package reusable, and a body edit leaves
every one but its own. Two checks give a package the same implementation
fingerprint exactly when its import closure has the same sources. `go run
./scripts/buildcost reuse` counts what the decision would reuse after each
edit of generated programs and examples
([receipts](../receipts/compile-speed.md#incremental-check-cs5a)).
