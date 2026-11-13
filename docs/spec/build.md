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

## Interface fingerprint

After an edit, a package whose check would come out as it did last time need
not be checked again: its source is the same and so is everything it sees of
its imports. Three fingerprints per package decide it (compile-speed plan
§5.3, CS5a; `internal/sketch/check/fingerprint.go`). Today they decide only.
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
2. **Interface fingerprint.** A hash of what an importer can see. It covers
   each file's token text with two kinds of span cut out: the body of each
   function that is not generic, and the body of each test. It also covers
   the rows the checker inferred from those bodies, the package lets a body
   assigns, the cell conversions the package makes, and the interface
   fingerprint of each import. What stays covers every declaration's
   signature with its written rows, every type definition with its `where`,
   every operation type with the text of its laws, every top-level law,
   every generic constraint, every let with its initializer, and every
   generic function with its body. An importer's instance of a generic
   function may read its body. An importer's test build runs the laws of an
   operation type it imports on its own providers. The facts a function
   `gives` and `keeps` and record invariants are not built yet
   ([facts](facts.md)); when they are, they stand in the declarations' text.
   A package that was not checked, or that has a diagnostic, has no interface
   fingerprint.
3. **Why it includes the imports'.** A type an import declares reaches an
   importer's importer through a let, a result or a field, with no token of
   it in the middle package (`let Sig = a.Sig`). A law of an import runs in
   the least package that sees it and a provider, at any depth (types.md,
   Laws). So a change in an import's interface changes the interface of
   every package above it. An exported signature edit therefore makes every
   package above it check again. A rule that stops at a middle package whose
   own view did not change needs the references by name of CS5b.
4. **Implementation fingerprint.** A hash of the source fingerprint and the
   implementation fingerprint of each import. Evaluation (tests, laws) runs
   imported bodies, so its results are keyed by it, never by an interface.
5. **The decision** (`check.Reusable`). A package can be taken as it was when
   it was clean last time, its source fingerprint is the same, and each of
   its imports has the same interface fingerprint, which is not empty. Its
   own interface is then the same too, so the decision runs bottom up from
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
