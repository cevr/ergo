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
